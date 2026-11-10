//go:build integration && linux

package conformance

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
)

func init() {
	if len(os.Args) < 2 {
		return
	}
	var run func() error
	switch os.Args[1] {
	case "--runtime-fixture-adapter":
		run = runFixtureAdapter
	case "--runtime-fixture-workload":
		run = runFixtureWorkload
	case "--runtime-fixture-descendant":
		run = runFixtureDescendant
	default:
		return
	}
	if os.Geteuid() != 65532 {
		fmt.Fprintln(os.Stderr, "runtime fixture must run as the container adapter user")
		os.Exit(1)
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		fmt.Fprintln(os.Stderr, "runtime fixture requires a disposable Docker container")
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// This small adapter uses the real SDK and real child HTTP requests. Its result
// file is intentionally simple: the response count from one completed request.
func runFixtureAdapter() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := writeFixturePID("adapter.pid"); err != nil {
		return err
	}
	var mu sync.Mutex
	var current agent.RunContext
	options, err := agent.LoadAgentServerOptions(agent.AgentServerOptions{
		TestFileName:        "fixture.txt",
		NativeMetricsSchema: agent.MetricsSchemaShibuyaV1,
		BeforeStart: func(_ context.Context, run agent.RunContext) error {
			mu.Lock()
			current = run
			mu.Unlock()
			return nil
		},
		BuildCommand: func(_ context.Context, run agent.RunContext) (agent.Command, error) {
			mode, err := os.ReadFile(run.TestFile)
			if err != nil {
				return agent.Command{}, err
			}
			return agent.Command{
				Command: self,
				Args:    []string{"--runtime-fixture-workload", strings.TrimSpace(string(mode)), filepath.Join(run.Workspace.Results, run.RunID+".requests")},
				Env:     os.Environ(),
			}, nil
		},
		MetricsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			run := current
			mu.Unlock()
			if run.RunID == "" {
				return
			}
			body, err := os.ReadFile(filepath.Join(run.Workspace.Results, run.RunID+".requests"))
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, "# TYPE %s counter\n%s{collection_id=%q,plan_id=%q,run_id=%q,engine_no=\"7\",zone=\"local\",label=\"fixture\",status=\"200\"} %s\n",
				metricschema.StatusCounter, metricschema.StatusCounter, run.CollectionID, run.PlanID, run.RunID, body)
		}),
	})
	if err != nil {
		return err
	}
	return agent.NewAgentServer(options).Run()
}

func runFixtureWorkload() error {
	if err := writeFixturePID("engine.pid"); err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: time.Second}).Get(TargetURL + "/fixture")
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fixture request returned HTTP %d", response.StatusCode)
	}
	if err := os.WriteFile(os.Args[3], []byte("1"), 0600); err != nil {
		return err
	}
	switch os.Args[2] {
	case "complete":
		return nil
	case "failed":
		return fmt.Errorf("intentional workload failure after a real request")
	case "cleanup":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(self, "--runtime-fixture-descendant", "127.0.0.1:18081")
		// Even a new session is still owned by the scenario subreaper.
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return err
		}
	case "reuse":
	default:
		return fmt.Errorf("unknown workload fixture %q", os.Args[2])
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runFixtureDescendant() error {
	listener, err := net.Listen("tcp", os.Args[2])
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := writeFixturePID("descendant.pid"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENT_ROOT"), "descendant.address"), []byte(listener.Addr().String()), 0600); err != nil {
		return err
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		connection.Close()
	}
}

func writeFixturePID(name string) error {
	return os.WriteFile(filepath.Join(os.Getenv("AGENT_ROOT"), name), []byte(strconv.Itoa(os.Getpid())), 0600)
}
