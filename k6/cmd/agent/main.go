// Copyright reqfleet contributors. Licensed under Apache-2.0.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/reqfleet/engine-adapters/k6/adapter"
	"github.com/reqfleet/engine-adapters/k6/metrics"
	"github.com/reqfleet/engine-sdk/agent"
)

func labelLimit(value string, present bool) (int, error) {
	if !present {
		return 10, nil
	}
	if value == "" {
		return 0, fmt.Errorf("label_limit must be a nonnegative integer")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("label_limit must be a nonnegative integer")
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("label_limit: %w", err)
	}
	return n, nil
}

func main() {
	limit, err := labelLimit(os.LookupEnv("label_limit"))
	if err != nil {
		log.Fatal(err)
	}
	exporter, err := metrics.New(limit)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:6566")
	if err != nil {
		log.Fatalf("bind metrics listener: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", exporter)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("metrics server: %v", err)
		}
	}()
	agent.MakeAgentServer(adapter.New(exporter).Options()).Run()
}
