package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/armon/consul-api"
)

var (
	addr = flag.String("addr", "127.0.0.1:8500", "set the address of the consul agent")
	bind = flag.String("bind", "0.0.0.0", "bind to specified host")
)

func main() {
	flag.Parse()

	client, err := consulapi.NewClient(&consulapi.Config{
		Address:    *addr,
		HttpClient: http.DefaultClient,
	})

	if err != nil {
		log.Fatalf("[fatal] couldn't activate consul api client: %s", err)
	}

	agent := NewAgent(client, *bind)

	go agent.Config.Updater()
	go agent.LeaderUpdater()
	agent.Run()
}
