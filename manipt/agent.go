package manipt

import (
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/armon/consul-api"
)

var (
	AgentWaitTime = time.ParseDuration("10m")
)

type IncomingConn struct {
	ListenerAddr net.Addr
	Connection   net.Conn
}

type Agent struct {
	Client *consulapi.Client
	Leader *consulapi.Node

	PortMap map[int]int // proxy our listener port to app port (for local)

	leaderchan chan *consulapi.Node
	connchan   chan *IncomingConn
}

func NewAgent(c *consulapi.Client) {
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
	for {
		incoming := <-a.connchan

		select {
		case node := <-a.leaderchan:
			a.Leader = node
			log.Printf("[info] new leader detected: %s", node)
			fallthrough
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

	proxy, err := net.Dial(conn.ListenerAddr.Network(), "localhost:"+port)
	if err != nil {
		log.Printf("[err] failed to dial local port; dropping incoming conn from %s: %s", conn.ListenerAddr, err)
		conn.Connection.Close()
		return
	}

	go proxyStream(conn.Connection, proxy)
	go proxyStream(proxy, conn.Connection)
}

func (a *Agent) proxyTo(port string, conn *IncomingConn, to *consulapi.Node) {
	proxy, err := net.Dial(conn.ListenerAddr.Network(), to.Address+":"+port)
	if err != nil {
		log.Printf("[err] failed to proxy to leader, doing local proxy instead: %s", err)
		proxyLocal(port, conn)
		return
	}

	go proxyStream(conn.Connection, proxy)
	go proxyStream(proxy, conn.Connection)
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
	var lastindex uint64

	self, err := a.Client.Agent().Self()
	if err != nil {
		log.Fatalf("[fatal] can't get information about self agent: %s", err)
	}

	selfhost := self["Member"]["Addr"].(string)
	selfport := self["Member"]["Port"].(int)

	catalog := a.Client.Catalog()
	for {
		nodes, meta, err := catalog.Nodes(&QueryOptions{
			WaitIndex: lastindex,
			WaitTime:  AgentWaitTime,
		})

		if err != nil {
			log.Printf("[err] failed to get catalogue: %s", err)
		}

		if !meta.KnownLeader {
			// let's handle it then
			a.leaderchan <- nil
		} else {
			if lastindex == meta.LastIndex {
				// nothing's changed
				continue
			}

			lastindex = meta.LastIndex

			leader, err := a.Client.Status().Leader()
			if err != nil {
				log.Printf("[err] failed to get leader: %s", err)
			}

			host, port, err := net.SplitHostPort(leader)
			if err != nil {
				log.Printf("[err] bad response from leader? str: %s, err: %s", leader, err)
			}

			if host == selfhost && port == selfport {
				// we're the leader
				a.leaderchan <- nil
				continue
			}

			found := false

			for _, v := range nodes {
				if v.Address == host {
					// we got our leader node
					a.leaderchan <- v
					found = true
					break
				}
			}

			if found {
				continue
			}

			// we couldn't find a leader node?
			log.Printf("[err] couldn't find leader %s in our catalogue, assuming no known leader", leader)
			a.leaderchan <- nil
		}
	}
}
