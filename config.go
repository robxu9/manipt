package main

import (
	"log"
	"net"
	"strconv"
	"sync"
)

type AgentConfig struct {
	sync.RWMutex

	Agent *Agent

	Ports map[int]*PortListener
	Bind  string

	synctrigger chan struct{}
}

type PortListener struct {
	Listener net.Listener
	To       int // internal port to dial and proxy to

	quit chan struct{}
}

func NewConfig(a *Agent, bind string) *AgentConfig {
	return &AgentConfig{
		Agent:       a,
		Ports:       make(map[int]*PortListener),
		Bind:        bind,
		synctrigger: make(chan struct{}),
	}
}

func (a *AgentConfig) Add(fromport, toport int, protocol string) error {
	a.Lock()
	defer a.Unlock()

	if l, ok := a.Ports[fromport]; ok {
		delete(a.Ports, fromport)

		err := l.Listener.Close()
		if err != nil {
			log.Printf("[warn] failed to close old listener on %d: %s", fromport, err)
		}

		l.quit <- struct{}{}
		close(l.quit)
	}

	listener, err := net.Listen(protocol, a.Bind+":"+strconv.Itoa(fromport))
	if err != nil {
		log.Printf("[err] failed to bind to %s/%s:%d: %s", protocol, a.Bind, fromport, err)
		return err
	}

	a.Ports[fromport] = &PortListener{
		Listener: listener,
		To:       toport,
	}

	a.synctrigger <- struct{}{}

	return nil
}

// get from->to ports
func (a *AgentConfig) Map() map[int]int {
	a.RLock()
	defer a.RUnlock()

	m := make(map[int]int)
	for k, v := range a.Ports {
		m[k] = v.To
	}

	return m
}

func (a *AgentConfig) Get(from int) *PortListener {
	a.RLock()
	defer a.RUnlock()

	return a.Ports[from]
}

func (a *AgentConfig) Remove(from int) {
	a.Lock()
	defer a.Unlock()

	if l, ok := a.Ports[from]; ok {
		delete(a.Ports, from)

		err := l.Listener.Close()
		if err != nil {
			log.Printf("[warn] failed to close listener for removal on %d: %s", from, err)
		}

		l.quit <- struct{}{}
		close(l.quit)
	}

	a.synctrigger <- struct{}{}
}

func (a *AgentConfig) Updater() {
	for {
		<-a.synctrigger
	}
}

func (a *AgentConfig) syncIncoming() {
	for {

		a.synctrigger <- struct{}{}
	}
}
