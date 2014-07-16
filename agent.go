package main

import (
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/armon/consul-api"
)

const (
	MANIPT_KEY = "service/manipt/leader"
)

var (
	AgentWaitTime = time.Duration(10) * time.Second
)

type IncomingConn struct {
	ListenerAddr net.Addr
	Connection   net.Conn
}

type Agent struct {
	Client *consulapi.Client
	Leader *consulapi.Node

	Session string

	PortMap map[int]int // proxy our listener port to app port (for local)

	leaderchan chan *consulapi.Node
	connchan   chan *IncomingConn
}

func NewAgent(c *consulapi.Client) *Agent {
	return &Agent{
		Client:     c,
		Leader:     nil,
		leaderchan: make(chan *consulapi.Node),
	}
}

func (a *Agent) AddListener(rport, lport int, l net.Listener) {
	a.PortMap[rport] = lport
	go a.proxyConns(l)
}

func (a *Agent) proxyConns(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			log.Printf("[err] failed to accept conn: %s", err)
			continue
		}
		a.connchan <- &IncomingConn{
			ListenerAddr: l.Addr(),
			Connection:   c,
		}
	}
}

func (a *Agent) Run() {
	var incoming *IncomingConn
	for {

		if incoming != nil {
			incoming = <-a.connchan
		}

		select {
		case node := <-a.leaderchan:
			a.Leader = node
			log.Printf("[info] new leader detected: %s", node)
		default:
			// handle update
			_, port, err := net.SplitHostPort(incoming.ListenerAddr.String())
			if err != nil {
				log.Printf("[err] failed to handle incoming conn %s, dropping", incoming.ListenerAddr)
				incoming.Connection.Close()
				continue
			}

			if a.Leader == nil {
				go a.proxyLocal(port, incoming)
			} else {
				go a.proxyTo(port, incoming, a.Leader)
			}
			incoming = nil
		}
	}
}

func (a *Agent) proxyLocal(port string, conn *IncomingConn) {

	iport, err := strconv.Atoi(port)
	if err != nil {
		log.Printf("[err] got strange port %s from incoming conn %s, dropping", port, conn.ListenerAddr)
		conn.Connection.Close()
		return
	}

	local, ok := a.PortMap[iport]
	if !ok {
		log.Printf("[err] failed to map port %d, dropping incoming conn from %s", iport, conn.ListenerAddr)
		conn.Connection.Close()
		return
	}

	proxy, err := net.Dial(conn.ListenerAddr.Network(), "localhost:"+strconv.Itoa(local))
	if err != nil {
		log.Printf("[err] failed to dial local port; dropping incoming conn from %s: %s", conn.ListenerAddr, err)
		conn.Connection.Close()
		return
	}

	go a.proxyStream(conn.Connection, proxy)
	go a.proxyStream(proxy, conn.Connection)
}

func (a *Agent) proxyTo(port string, conn *IncomingConn, to *consulapi.Node) {
	proxy, err := net.Dial(conn.ListenerAddr.Network(), to.Address+":"+port)
	if err != nil {
		log.Printf("[err] failed to proxy to leader, doing local proxy instead: %s", err)
		a.proxyLocal(port, conn)
		return
	}

	go a.proxyStream(conn.Connection, proxy)
	go a.proxyStream(proxy, conn.Connection)
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
			Key:     MANIPT_KEY,
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
			pair, meta, err := kv.Get(MANIPT_KEY, &consulapi.QueryOptions{
				WaitIndex: lastindex,
				WaitTime:  AgentWaitTime,
			})

			if err != nil {
				log.Printf("[err] failed to get kv for current leader node: %s", err)
				break
			}

			if pair.Session == "" {
				// whoever was no longer is
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
