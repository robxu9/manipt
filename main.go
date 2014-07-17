package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/armon/consul-api"
)

var (
	addr = flag.String("addr", "127.0.0.1:8500", "set the address of the consul agent")
	bind = flag.String("bind", "0.0.0.0", "bind to interface")
)

type HelloWorld struct{}

func (h *HelloWorld) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	name, err := os.Hostname()
	if err != nil {
		http.Error(rw, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(rw, "Hello World from %s!", name)
}

func init() {
	// when you use -h
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] (PORT)\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Embedded webapp will be started on specified PORT\n")
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

	if flag.NArg() != 1 {
		log.Fatalln("[fatal] only one argument allowed. run with -h for info.")
	}

	port, err := strconv.Atoi(flag.Arg(0))

	if err != nil {
		log.Fatalf("[fatal] bad port passed: %s", err)
	}

	http.DefaultServeMux.Handle("/", &HelloWorld{})
	serv := &http.Server{}

	agent := NewAgent(client, *bind, port, serv)

	go agent.LeaderUpdater()
	agent.Run()
}
