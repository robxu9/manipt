package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/armon/consul-api"
)

const (
	MANIPT_KEY = "service/manipt/leader"
)

var (
	AgentWaitTime = 50 * time.Millisecond
)

type Agent struct {
	Client *consulapi.Client
	Leader *consulapi.Node

	Bind *net.TCPAddr

	PubListener    *net.TCPListener
	WebAppListener *net.TCPListener
	Server         *http.Server

	Session string

	leaderchan chan *consulapi.Node
	connchan   chan *net.TCPConn
	proxychan  chan *net.TCPConn

	quitupdater chan struct{}
	quitproxy   chan struct{}
	quitupresp  chan struct{}
}

func NewAgent(c *consulapi.Client, bind string, port int, server *http.Server) *Agent {
	tcpaddr, err := net.ResolveTCPAddr("tcp", bind+":"+strconv.Itoa(port))
	if err != nil {
		panic(err)
	}

	return &Agent{
		Client:     c,
		Leader:     nil,
		Bind:       tcpaddr,
		Server:     server,
		leaderchan: make(chan *consulapi.Node),
		connchan:   make(chan *net.TCPConn, 1024),
		proxychan:  make(chan *net.TCPConn, 1024),

		quitupdater: make(chan struct{}),
		quitproxy:   make(chan struct{}),
		quitupresp:  make(chan struct{}),
	}
}

func (a *Agent) Quit() {
	log.Printf("[info] agent called to quit")

	a.quitupdater <- struct{}{}
	a.quitproxy <- struct{}{}

	a.PubListener.Close()
	a.WebAppListener.Close()

	<-a.quitupresp
	log.Printf("[info] agent quit successfully")
}

func (a *Agent) proxyConns() {
	for {
		c, err := a.PubListener.AcceptTCP()
		if err != nil {
			select {
			case <-a.quitproxy:
				log.Printf("[info] proxy called to quit")
				return
			default:
				log.Printf("[err] failed to accept conn: %s", err)
				continue
			}
		}
		a.connchan <- c
	}
}

func (a *Agent) setup() {
	publisten, err := net.ListenTCP("tcp", a.Bind)
	if err != nil {
		log.Fatalf("[fatal] failed to setup public listener")
	}

	a.PubListener = publisten

	webapp, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	})
	if err != nil {
		log.Fatalf("[fatal] failed to setup webapp listener")
	}

	a.WebAppListener = webapp

	go a.Server.Serve(a.WebAppListener)
}

func (a *Agent) Run() {
	// set our Listener
	a.setup()

	// get the current status first
	a.Leader = <-a.leaderchan

	if a.Leader == nil {
		log.Printf("[info] starting as leader")
	} else {
		log.Printf("[info] starting as proxy to %s @ %s", a.Leader.Node, a.Leader.Address)
	}

	// start proxying connections
	go a.proxyConns()

	for {
		select {
		case node, ok := <-a.leaderchan:
			if !ok {
				return
			}

			log.Printf("[info] received leader node: %s", node)

			if node == nil && a.Leader != nil {
				log.Printf("[info] becoming leader")
			} else if node != nil {
				log.Printf("[info] becoming proxy to %s @ %s", node.Node, node.Address)
			}

			a.Leader = node
		case incoming := <-a.connchan:
			if a.Leader != nil {
				log.Printf("[info] proxying %s", incoming.RemoteAddr())
				a.proxyTo(a.Leader, incoming)
			} else {
				a.proxyWebApp(incoming)
			}
		}
	}
}

func (a *Agent) proxyWebApp(conn *net.TCPConn) {
	waddr := a.WebAppListener.Addr().(*net.TCPAddr)
	proxy, err := net.DialTCP("tcp", nil, waddr)
	if err != nil {
		log.Printf("[err] failed to send to webapp, dropping connection: %s", err)
		conn.Close()
		return
	}

	go a.proxyStream(conn, proxy)
	go a.proxyStream(proxy, conn)
}

func (a *Agent) proxyTo(to *consulapi.Node, conn *net.TCPConn) {
	proxy, err := net.Dial("tcp", to.Address+":"+strconv.Itoa(a.Bind.Port))
	if err != nil {
		log.Printf("[err] failed to proxy to leader, will drop connection: %s", err)
		conn.Close()
		return
	}

	tcpproxy, ok := proxy.(*net.TCPConn)
	if !ok {
		log.Fatalf("[fatal] assert failed - tcpproxy should be a *net.TCPConn")
	}

	go a.proxyStream(conn, tcpproxy)
	go a.proxyStream(tcpproxy, conn)
}

func (a *Agent) proxyStream(from, to *net.TCPConn) {
	for {
		buf := make([]byte, 1024)
		_, err := from.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("[err] error reading from %s to %s: %s", from, to, err)
			}
			from.CloseRead()
			to.CloseWrite()
			return
		}

		_, err = to.Write(buf)
		if err != nil {
			log.Printf("[err] error writing from %s to %s: %s", from, to, err)
			return
		}
	}
}

func (a *Agent) LeaderUpdater() {

	nodename, err := a.Client.Agent().NodeName()
	if err != nil {
		log.Fatalf("[fatal] can't get our own node name: %s", err)
	}

	if a.Session == "" {
		str, _, err := a.Client.Session().Create(&consulapi.SessionEntry{
			LockDelay: 1000 * time.Nanosecond, // minimum value accepted by consul
		}, nil)
		if err != nil {
			log.Fatalf("[fatal] failed to create session in consul: %s", err)
		}
		a.Session = str
	}

	var lastindex uint64

	kv := a.Client.KV()
	for {
		res, _, err := kv.Acquire(&consulapi.KVPair{
			Key:     MANIPT_KEY + "/" + strconv.Itoa(a.Bind.Port),
			Value:   []byte(nodename),
			Session: a.Session,
		}, nil)

		if err != nil {
			log.Fatalf("[fatal] couldn't contact consul for acquiring lock: %s", err)
		}

		if res {
			// we're leader!
			a.leaderchan <- nil
		}

		for {
			// who's leader? let's check
			pair, meta, err := kv.Get(MANIPT_KEY+"/"+strconv.Itoa(a.Bind.Port), &consulapi.QueryOptions{
				WaitIndex: lastindex,
				WaitTime:  AgentWaitTime,
			})

			if err != nil {
				log.Printf("[err] failed to get kv for current leader node: %s", err)
				break
			}

			select {
			case <-a.quitupdater:
				log.Printf("[info] called to quit")
				if string(pair.Value) == nodename && pair.Session != "" { // give up leadership
					log.Printf("[info] giving up leadership")
					kv.Release(pair, nil)
					log.Printf("[info] released leadership successfully")
				}
				close(a.leaderchan)
				a.quitupresp <- struct{}{}
				log.Printf("[info] updater shutdown complete")
				return
			default:
			}

			if pair.Session == "" {
				// whoever was no longer is
				// handle until we get a new leader
				a.leaderchan <- nil
				// set the last index
				lastindex = meta.LastIndex
				// wait for a moment (lockdelays)
				time.Sleep(10 * time.Second)
				// so we're going to try getting it
				break
			}

			if lastindex == meta.LastIndex {
				// nothing's changed, continue
				continue
			}

			if string(pair.Value) != nodename { // non-leaders
				// find this person
				catalog := a.Client.Catalog()
				node, _, err := catalog.Node(string(pair.Value), nil)
				if err != nil {
					log.Printf("[err] failed to get current leader node: %s", err)
					continue
				}
				a.leaderchan <- node.Node // send new leader off
			}

			lastindex = meta.LastIndex
		}
	}

}
