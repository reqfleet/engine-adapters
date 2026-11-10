//go:build integration && linux

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

func requireRuntimeContainer(t *testing.T) {
	t.Helper()
	require.Zero(t, os.Geteuid(), "run the container-only conformance target")
	_, err := os.Stat("/.dockerenv")
	require.NoError(t, err, "runtime regression tests own fixed ports; use a disposable Docker container")
}

func fixtureContract(t *testing.T, complete string) Contract {
	t.Helper()
	requireRuntimeContainer(t)
	self, err := os.Executable()
	require.NoError(t, err)
	directory, err := os.MkdirTemp("/tmp", "conformance-fixture-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	require.NoError(t, os.Chmod(directory, 0755))
	executable := filepath.Join(directory, "adapter")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(self, "'", "'\"'\"'") + "' --runtime-fixture-adapter\n"
	require.NoError(t, os.WriteFile(executable, []byte(script), 0755))
	return Contract{
		Executable: executable,
		Artifacts:  map[string]string{"complete.txt": complete, "reuse.txt": "reuse", "cleanup.txt": "cleanup"},
		Target: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "real fixture response")
		}),
		LabelLimit: 1,
		Basic: BasicScenarios{
			Complete:          Fixture{File: "complete.txt"},
			Reuse:             Fixture{File: "reuse.txt"},
			CompletionTimeout: 10 * time.Second,
			AssertMetrics: func(t *testing.T, runtime *Runtime, id string) MetricsSnapshot {
				body, err := os.ReadFile(filepath.Join(runtime.Root, "test-result", id+".requests"))
				require.NoError(t, err)
				count, err := strconv.ParseUint(string(body), 10, 64)
				require.NoError(t, err)
				require.Equal(t, uint64(1), count)
				return AssertMetrics(t, runtime.Scrape(t), MetricsExpectation{
					Identity: MetricIdentity{CollectionID: CollectionID, PlanID: PlanID, RunID: id, EngineNo: "7", Zone: "local"},
					Requests: map[string]RequestExpectation{"fixture": {Statuses: map[string]uint64{"200": count}}},
				})
			},
		},
	}
}

// Delay delivery, not execution or status generation: the first progress response
// the harness sees is the real SDK's terminal result from a short-lived process.
// This makes missing the transient running state deterministic.
type terminalFirstTransport struct {
	base   http.RoundTripper
	cancel func()
	first  protocol.RunState
}

func (transport *terminalFirstTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path != "/progress" || request.URL.Query().Get("run_id") != "8901" || transport.first != "" {
		return transport.base.RoundTrip(request)
	}
	for {
		response, err := transport.base.RoundTrip(request)
		if err != nil || response.StatusCode != http.StatusOK {
			return response, err
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		var status protocol.RunStatus
		if err := json.Unmarshal(body, &status); err != nil {
			return nil, err
		}
		if status.State.Terminal() {
			transport.first = status.State
			fmt.Printf("first delivered progress state=%s\n", status.State)
			response.Body = io.NopCloser(bytes.NewReader(body))
			return response, nil
		}
		if status.State == protocol.RunRunning && transport.cancel != nil {
			transport.cancel()
			transport.cancel = nil
		}
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeBasicAcceptsCompletedFirstProgress(t *testing.T) {
	contract := fixtureContract(t, "complete")
	var requests atomic.Int64
	contract.Target = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "real fixture response")
	})
	runtime := startRuntime(t, contract)
	transport := &terminalFirstTransport{base: http.DefaultTransport}
	runtime.Client.Transport = transport
	runBasic(t, runtime, contract.Basic)
	require.Equal(t, protocol.RunCompleted, transport.first)
	require.Equal(t, int64(3), requests.Load(), "one completed request per executed fixture, including both reuse runs")
}

func TestRuntimeBasicRejectsUnsuccessfulCompletion(t *testing.T) {
	requireRuntimeContainer(t)
	const marker = "CONFORMANCE_REJECT_TERMINAL"
	if state := os.Getenv(marker); state != "" {
		mode := "failed"
		if state == string(protocol.RunCanceled) {
			mode = "reuse"
		}
		contract := fixtureContract(t, mode)
		runtime := startRuntime(t, contract)
		transport := &terminalFirstTransport{base: http.DefaultTransport}
		if state == string(protocol.RunCanceled) {
			transport.cancel = func() { runtime.Stop(t, "8901") }
		}
		runtime.Client.Transport = transport
		runBasic(t, runtime, contract.Basic)
		return
	}
	self, err := os.Executable()
	require.NoError(t, err)
	for _, state := range []protocol.RunState{protocol.RunFailed, protocol.RunCanceled} {
		t.Run(string(state), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, self, "-test.run=^TestRuntimeBasicRejectsUnsuccessfulCompletion$", "-test.timeout=35s")
			command.Env = append(os.Environ(), marker+"="+string(state))
			command.WaitDelay = time.Second
			output, err := command.CombinedOutput()
			require.NoError(t, ctx.Err(), "%s", output)
			require.Error(t, err, "the lifecycle suite must reject %s: %s", state, output)
			require.Contains(t, string(output), "first delivered progress state="+string(state))
		})
	}
}

func readFixturePID(t *testing.T, root, name string) int {
	t.Helper()
	var body []byte
	require.Eventually(t, func() bool {
		var err error
		body, err = os.ReadFile(filepath.Join(root, name))
		return err == nil && len(body) != 0
	}, processCleanupWait, 10*time.Millisecond)
	pid, err := strconv.Atoi(string(body))
	require.NoError(t, err)
	return pid
}

func TestRuntimeCleanupStopsActiveRunAfterRejectedOverlap(t *testing.T) {
	requireRuntimeContainer(t)
	var finalized atomic.Int64
	t.Run("scenario", func(t *testing.T) {
		contract := fixtureContract(t, "complete")
		contract.Artifacts["graceful.txt"] = "graceful"
		contract.Target = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/finalize" {
				finalized.Add(1)
			}
			_, _ = io.WriteString(w, "real fixture response")
		})
		runtime := startRuntime(t, contract)
		runtime.Start(t, "8901", Fixture{File: "graceful.txt"})
		runtime.Running(t, "8901")
		runtime.AwaitRequests(t, "8901", 1)
		runtime.Start(t, "8902", contract.Basic.Reuse)
		require.Eventually(t, func() bool {
			return strings.Contains(runtime.Logs(), "run 8901 is still active")
		}, proofWait, 25*time.Millisecond, "overlapping start must reach SDK admission")
		require.Zero(t, finalized.Load(), "workload must remain active until registered cleanup")
		// Do not explicitly Stop: exercise the registered runtime cleanup.
	})
	require.Equal(t, int64(1), finalized.Load(), "cleanup must gracefully finalize the admitted workload despite the rejected overlap")
}

func TestRuntimeCleanupCancelsArtifactPreparation(t *testing.T) {
	requireRuntimeContainer(t)
	for _, verb := range []string{protocol.VerbStart, protocol.VerbPrepare} {
		t.Run(verb, func(t *testing.T) {
			runtime := startRuntime(t, fixtureContract(t, "complete"))
			payload := Payload(verb, "8901", runtime.BlockedFile(), nil)
			want := protocol.RunStarting
			if verb == protocol.VerbPrepare {
				payload.PlanMessage[proofPlan].DependencyFile = runtime.BlockedFile()
				want = protocol.RunPreparing
			}
			runtime.Send(t, payload)
			runtime.AwaitDownload(t)
			status, err := runtime.Progress("8901")
			require.NoError(t, err)
			require.Equal(t, want, status.State, "the SDK must enter the requested lifecycle, not a substituted Start")

			// Exercise the same graceful phase as registered cleanup, while
			// leaving forced supervisor teardown pending until this test ends.
			runtime.stopSubmitted(proofWait)
			runtime.AwaitDownloadCancellation(t)
			assertFixtureCanceledWhileAlive(t, runtime, verb == protocol.VerbPrepare)
		})
	}
}

func TestRuntimeCleanupCancelsDependencyPreparationHook(t *testing.T) {
	contract := fixtureContract(t, "complete")
	dependency, err := protocol.DependencyFilePath(proofCollection, proofPlan, "dependencies.txt")
	require.NoError(t, err)
	contract.DataArtifacts = map[string]string{
		path.Join(protocol.ArtifactPathPrefix, dependency): "block preparation",
	}
	runtime := startRuntime(t, contract)
	payload := Payload(protocol.VerbPrepare, "8901", "complete.txt", nil)
	payload.PlanMessage[proofPlan].DependencyFile = "dependencies.txt"
	runtime.Send(t, payload)
	require.Eventually(t, func() bool {
		body, err := os.ReadFile(filepath.Join(runtime.Root, "preparing"))
		return err == nil && string(body) == "block preparation"
	}, proofWait, 10*time.Millisecond, "the SDK hook must read the downloaded dependency artifact")
	status, err := runtime.Progress("8901")
	require.NoError(t, err)
	require.Equal(t, protocol.RunPreparing, status.State)
	require.Equal(t, int64(1), runtime.Downloads(), "Prepare downloads dependencies, not the execution test file")

	runtime.stopSubmitted(proofWait)
	body, err := os.ReadFile(filepath.Join(runtime.Root, "preparation-canceled"))
	require.NoError(t, err, "the SDK hook must observe cancellation before any forced teardown")
	require.Equal(t, context.Canceled.Error(), string(body))
	assertFixtureCanceledWhileAlive(t, runtime, true)
}

func assertFixtureCanceledWhileAlive(t *testing.T, runtime *Runtime, preparing bool) {
	t.Helper()
	status, err := runtime.Progress("8901")
	require.NoError(t, err, "the adapter must still serve its terminal state before forced teardown")
	require.Equal(t, protocol.RunCanceled, status.State)
	require.Equal(t, protocol.StopCanceled, status.StopReason)
	if preparing {
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/prepare-progress", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+proofKey)
		response, err := runtime.Client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var preparation protocol.PreparationProgress
		require.NoError(t, json.NewDecoder(response.Body).Decode(&preparation))
		require.Equal(t, "8901", preparation.RunID)
		require.Equal(t, protocol.PreparationStateCanceled, preparation.Status)
	}
	runtime.AssertAlive(t)
	require.NoError(t, syscall.Kill(readFixturePID(t, runtime.Root, "adapter.pid"), 0))
}

// Cap individual pipe stalls so reverting the whole-map cleanup deadline fails
// in hundreds of milliseconds rather than waiting seconds for each submitted ID.
type cleanupDeadlineConn struct {
	net.Conn
	writes int
	stall  time.Duration
}

func (connection *cleanupDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	if cap := time.Now().Add(connection.stall); cap.Before(deadline) {
		deadline = cap
	}
	return connection.Conn.SetWriteDeadline(deadline)
}

func (connection *cleanupDeadlineConn) Write(body []byte) (int, error) {
	connection.writes++
	return connection.Conn.Write(body)
}

func TestRuntimeCleanupSharesDeadlineAcrossSubmittedIDs(t *testing.T) {
	for _, test := range []struct {
		name      string
		blocked   string
		submitted int
		stall     time.Duration
	}{
		{"writes/accumulated", "writes", 16, 25 * time.Millisecond},
		{"progress/accumulated", "progress", 16, 25 * time.Millisecond},
		{"writes/single", "writes", 1, 400 * time.Millisecond},
		{"progress/single", "progress", 1, 400 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				<-request.Context().Done()
			}))
			defer server.Close()
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				},
			}
			defer transport.CloseIdleConnections()
			writer, reader := net.Pipe()
			defer writer.Close()
			defer reader.Close()
			connection := &cleanupDeadlineConn{Conn: writer, stall: test.stall}
			if test.blocked == "progress" {
				go func() { _, _ = io.Copy(io.Discard, reader) }()
			}
			runtime := &Runtime{
				peer:    &proofPeer{connection: connection},
				Client:  &http.Client{Transport: transport, Timeout: test.stall},
				done:    make(chan struct{}),
				started: make(map[string]struct{}),
			}
			for id := range test.submitted {
				runtime.started[strconv.Itoa(id)] = struct{}{}
			}
			started := time.Now()
			runtime.stopSubmitted(75 * time.Millisecond)
			elapsed := time.Since(started)
			require.Less(t, elapsed, 250*time.Millisecond, "one budget must include every Stop write and progress request")
			if test.blocked == "writes" {
				require.Positive(t, connection.writes)
				if test.submitted > 1 {
					require.Less(t, connection.writes, test.submitted, "check the shared deadline between Stop IDs")
				}
				require.Zero(t, requests.Load(), "Stop writes already exhausted the shared budget")
			} else {
				require.Equal(t, test.submitted, connection.writes)
				require.Positive(t, requests.Load())
				if test.submitted > 1 {
					require.Less(t, requests.Load(), int64(test.submitted), "check the shared deadline between progress IDs")
				}
			}
		})
	}
}

func TestRuntimeCleanupOwnsSeparateProcessGroups(t *testing.T) {
	requireRuntimeContainer(t)
	// A sibling with identical credentials must survive scenario cleanup.
	self, err := os.Executable()
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "conformance-unrelated-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.NoError(t, os.Chown(root, 65532, 65532))
	unrelated := exec.Command(self, "--runtime-fixture-descendant", "127.0.0.1:0")
	unrelated.Env = append(os.Environ(), "AGENT_ROOT="+root)
	unrelated.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}, Setsid: true}
	require.NoError(t, unrelated.Start())
	unrelatedDone := make(chan error, 1)
	go func() { unrelatedDone <- unrelated.Wait() }()
	t.Cleanup(func() {
		_ = unrelated.Process.Kill()
		select {
		case <-unrelatedDone:
		case <-time.After(processCleanupWait):
			t.Error("unrelated fixture did not exit after cleanup")
		}
	})
	require.Equal(t, unrelated.Process.Pid, readFixturePID(t, root, "descendant.pid"))

	for _, mode := range []string{"unresponsive-adapter", "exited-adapter"} {
		t.Run(mode, func(t *testing.T) {
			var pids []int
			var cleanupStarted time.Time
			t.Run("scenario", func(t *testing.T) {
				contract := fixtureContract(t, "complete")
				runtime := startRuntime(t, contract)
				runtime.Start(t, "8901", Fixture{File: "cleanup.txt"})
				runtime.Running(t, "8901")
				runtime.AwaitRequests(t, "8901", 1)
				adapter := readFixturePID(t, runtime.Root, "adapter.pid")
				engine := readFixturePID(t, runtime.Root, "engine.pid")
				descendant := readFixturePID(t, runtime.Root, "descendant.pid")
				pids = []int{adapter, engine, descendant}
				adapterGroup, err := syscall.Getpgid(adapter)
				require.NoError(t, err)
				engineGroup, err := syscall.Getpgid(engine)
				require.NoError(t, err)
				descendantGroup, err := syscall.Getpgid(descendant)
				require.NoError(t, err)
				require.NotEqual(t, adapterGroup, engineGroup, "SDK engine has its own process group")
				require.NotEqual(t, engineGroup, descendantGroup, "descendant escapes engine group")
				connection, err := net.DialTimeout("tcp", "127.0.0.1:18081", time.Second)
				require.NoError(t, err)
				connection.Close()
				cleanupStarted = time.Now()
				if mode == "unresponsive-adapter" {
					// Stop cannot be received and progress cannot reply. The registered
					// runtime cleanup must escalate without an unbounded Cmd.Wait.
					require.NoError(t, syscall.Kill(adapter, syscall.SIGSTOP))
				} else {
					// Orphans must remain owned even when the adapter disappears first.
					require.NoError(t, syscall.Kill(adapter, syscall.SIGKILL))
				}
			})
			require.Len(t, pids, 3)
			require.Less(t, time.Since(cleanupStarted), proofWait+processCleanupWait+10*time.Second, "forced cleanup must be bounded")
			for _, pid := range pids {
				require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "scenario process %d survived cleanup", pid)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:18081")
			require.NoError(t, err, "a descendant listener contaminated the next scenario")
			listener.Close()
			require.NoError(t, unrelated.Process.Signal(syscall.Signal(0)), "cleanup killed an unrelated sibling")
		})
	}
}

func TestRuntimeReapsOrphansWhileAdapterLives(t *testing.T) {
	requireRuntimeContainer(t)
	var cleanupPIDs []int
	var cleanupStarted time.Time
	t.Run("scenario", func(t *testing.T) {
		contract := fixtureContract(t, "orphan")
		runtime := startRuntime(t, contract)
		adapter := readFixturePID(t, runtime.Root, "adapter.pid")
		runtime.Start(t, "8901", Fixture{File: "complete.txt"})
		runtime.Terminal(t, "8901", protocol.RunCompleted, 10*time.Second)
		runtime.AwaitRequests(t, "8901", 1)

		adapterStatus, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", adapter))
		require.NoError(t, err)
		var orphans []int
		for index := range 3 {
			name := fmt.Sprintf("orphan-%d", index)
			pid := readFixturePID(t, runtime.Root, name+".pid")
			parent := readFixturePID(t, runtime.Root, name+".parent")
			require.Contains(t, string(adapterStatus), fmt.Sprintf("PPid:\t%d\n", parent), "orphan must be adopted by the adapter's supervisor")
			require.NoError(t, syscall.Kill(pid, 0), "orphan must still be alive before release")
			orphans = append(orphans, pid)
		}
		require.NoError(t, os.WriteFile(filepath.Join(runtime.Root, "orphan.exit"), nil, 0644))
		for _, pid := range orphans {
			require.Eventually(t, func() bool {
				return syscall.Kill(pid, 0) == syscall.ESRCH
			}, processCleanupWait, 10*time.Millisecond, "orphan %d must be reaped before adapter cleanup", pid)
		}
		select {
		case <-runtime.done:
			t.Fatal("reaping an adopted child was mistaken for adapter exit")
		default:
		}
		require.NoError(t, syscall.Kill(adapter, 0), "adapter must survive adopted child exits")

		// The same SDK adapter must still launch a real workload. Leave its
		// separate-session descendant for the normal runtime cleanup to own.
		runtime.Start(t, "8902", Fixture{File: "cleanup.txt"})
		runtime.Running(t, "8902")
		runtime.AwaitRequests(t, "8902", 1)
		cleanupPIDs = []int{
			adapter,
			readFixturePID(t, runtime.Root, "engine.pid"),
			readFixturePID(t, runtime.Root, "descendant.pid"),
		}
		connection, err := net.DialTimeout("tcp", "127.0.0.1:18081", time.Second)
		require.NoError(t, err)
		connection.Close()
		cleanupStarted = time.Now()
	})
	require.Len(t, cleanupPIDs, 3)
	require.Less(t, time.Since(cleanupStarted), proofWait+processCleanupWait+10*time.Second, "normal cleanup must remain bounded")
	for _, pid := range cleanupPIDs {
		require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "scenario process %d survived cleanup", pid)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18081")
	require.NoError(t, err, "normal cleanup must release the descendant listener")
	listener.Close()
}
