package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/armon/consul-api"
)

const (
	MANIPT_KEY = "service/manipt/leader"
)

var (
	AgentWaitTime = time.Duration(10) * time.Second
)

type Agent struct {
	sync.RWMutex

	Client *consulapi.Client
	Leader *consulapi.Node

	Bind string
	Port int

	Listener net.Listener
	Server   *http.Server

	IsServing bool

	Session string

	leaderchan chan *consulapi.Node
	connchan   chan net.Conn
	closechan  chan struct{}
}

func NewAgent(c *consulapi.Client, bind string, port int, server *http.Server) *Agent {
	return &Agent{
		Client:     c,
		Leader:     nil,
		Bind:       bind,
		Port:       port,
		Server:     server,
		leaderchan: make(chan *consulapi.Node),
		connchan:   make(chan net.Conn),
		closechan:  make(chan struct{}),
	}
}

func (a *Agent) proxyConns() {
	for {
		c, err := a.Listener.Accept()
		if err != nil {
			select {
			case <-a.closechan:
				return
			default:
				log.Printf("[err] failed to accept conn: %s", err)
				continue
			}
		}
		a.connchan <- c
	}
}

func (a *Agent) resetListener() {
	if a.Listener != nil {
		a.Listener.Close()
	}

	listener, err := net.Listen("tcp", a.Bind+":"+strconv.Itoa(a.Port))
	if err != nil {
		log.Fatalf("[fatal] failed to bind to %s:%d", a.Bind, a.Port)
	}
	a.Listener = listener
}

func (a *Agent) Run() {
	// set our Listener
	a.resetListener()

	// get the current status first
	a.Leader = <-a.leaderchan

	if a.Leader == nil { // so we're leader
		// ask http server to start
		log.Printf("[info] becoming leader")
		go a.Server.Serve(a.Listener)
	} else {
		log.Printf("[info] becoming proxy")
		go a.proxyConns()
	}

	for {
		select {
		case node := <-a.leaderchan:
			log.Printf("[info] new leader detected: %s", node)

			if node == nil {
				// we're becoming the leader
				a.closechan <- struct{}{}
				a.resetListener()

				log.Printf("[info] becoming leader")
				go a.Server.Serve(a.Listener)
			} else if a.Leader == nil {
				// we're no longer the leader
				log.Printf("[info] becoming proxy")

				a.resetListener()
				go a.proxyConns()
			}

			a.Leader = node
		case incoming := <-a.connchan:
			log.Printf("[info] proxying %s", incoming.RemoteAddr())
			a.proxyTo(a.Leader, incoming)
		}
	}
}

func (a *Agent) proxyTo(to *consulapi.Node, conn net.Conn) {
	proxy, err := net.Dial("tcp", to.Address+":"+strconv.Itoa(a.Port))
	if err != nil {
		log.Printf("[err] failed to proxy to leader, will drop connection: %s", err)
		return
	}

	go a.proxyStream(conn, proxy)
	go a.proxyStream(proxy, conn)
}

func (a *Agent) proxyStream(from, to net.Conn) {
	for {
		buf := make([]byte, 1024)
		_, err := from.Read(buf)
		if err != nil {
			if err == io.EOF {
				// we're done here
				return
			}
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
		str, _, err := a.Client.Session().Create(nil, nil)
		if err != nil {
			log.Fatalf("[fatal] failed to create session in consul: %s", err)
		}
		a.Session = str
	}

	var lastindex uint64

	kv := a.Client.KV()
	for {
		res, _, err := kv.Acquire(&consulapi.KVPair{
			Key:     MANIPT_KEY + "/" + strconv.Itoa(a.Port),
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
			pair, meta, err := kv.Get(MANIPT_KEY+"/"+strconv.Itoa(a.Port), &consulapi.QueryOptions{
				WaitIndex: lastindex,
				WaitTime:  AgentWaitTime,
			})

			if err != nil {
				log.Printf("[err] failed to get kv for current leader node: %s", err)
				break
			}

			if pair.Session == "" {
				// whoever was no longer is
				// handle until we get a new leader
				a.leaderchan <- nil
				// so we're going to try getting it
				break
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
