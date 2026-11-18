// Copyright reqfleet contributors. Licensed under Apache-2.0.
package main

import (
	"log"

	"github.com/reqfleet/engine-adapters/k6/adapter"
	"github.com/reqfleet/engine-adapters/k6/metrics"
	"github.com/reqfleet/engine-sdk/agent"
)

func main() {
	options, err := agent.LoadAgentServerOptions(agent.AgentServerOptions{})
	if err != nil {
		log.Fatal(err)
	}
	exporter := &metrics.Exporter{}
	options.CustomIngestMetrics = exporter.Collect
	options = adapter.New(exporter).Options(options)
	if err := agent.NewAgentServer(options).Run(); err != nil {
		log.Fatal(err)
	}
}
