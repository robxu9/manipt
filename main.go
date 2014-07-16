package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/armon/consul-api"
)

var (
	addr = flag.String("addr", "127.0.0.1:8500", "set the address of the consul agent")
)

func init() {
	// when you use -h
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [arguments]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Arguments are in the form of \"fromport:toport:protocol\"\n")
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	client, err := consulapi.NewClient(&consulapi.Config{
		Address:    *addr,
		HttpClient: http.DefaultClient,
	})

	if err != nil {
		log.Fatalf("[fatal] couldn't activate consul api client: %s", err)
	}

	if flag.NArg() == 0 {
		log.Fatalln("[fatal] no arguments specified. run with -h for info.")
	}

	agent := NewAgent(client)

	for k, v := range flag.Args() {
		ports := strings.Split(v, ":")
		from, err := strconv.Atoi(ports[0])
		if err != nil {
			log.Fatalf("[fatal] in argument %d: from port is invalid: %s", k, err)
		}
		to, err := strconv.Atoi(ports[1])
		if err != nil {
			log.Fatalf("[fatal] in argument %d: to port is invalid: %s", k, err)
		}
		protocol := ports[2]

		listener, err := net.Listen(protocol, "0.0.0.0:"+strconv.Itoa(from))
		if err != nil {
			log.Fatalf("[fatal] in argument %d: failed to listen: %s", k, err)
		}

		agent.AddListener(from, to, listener)
	}

	go agent.LeaderUpdater()
	agent.Run()
}
