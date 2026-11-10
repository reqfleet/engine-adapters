//go:build integration && linux

// Protocol harness adapted from engine-sdk v0.3.0 preparing-adapter example.
// Copyright reqfleet contributors. Apache-2.0; modified for real adapter processes.
package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

const (
	CollectionID = proofCollection
	PlanID       = proofPlan
	APIKey       = proofKey
	Wait         = proofWait
	TargetURL    = "http://127.0.0.1:18080"
)

// Fixture names an artifact and its adapter-specific SDK run settings.
type Fixture struct {
	File     string
	Settings json.RawMessage
}

// BasicScenarios requires a naturally completing fixture and a long-running one.
// AssertMetrics checks the adapter's real output against its native result data.
type BasicScenarios struct {
	Complete          Fixture
	Reuse             Fixture
	CompletionTimeout time.Duration
	AssertMetrics     func(*testing.T, *Runtime, string) MetricsSnapshot
}

// BudgetScenario exercises admission using real requests, then checks the final
// native-output oracle and exact labels. Fixtures must repeat and vary labels.
type BudgetScenario struct {
	Name            string
	LabelLimit      int
	Fixture         Fixture
	MinimumRequests uint64
	RequestLabels   []string
	ByteLabels      []string
}

// Contract supplies engine-specific artifacts and behavior, never peer plumbing.
// DataArtifacts keys are SDK artifact paths (use DataArtifactPath to build them).
// Run owns fixed ports and /test-data and MUST only run in a disposable container.
type Contract struct {
	Executable    string
	Artifacts     map[string]string
	DataArtifacts map[string]string
	Target        http.Handler
	LabelLimit    int
	Basic         BasicScenarios
	Budgets       []BudgetScenario
	Scenarios     func(*testing.T, *Runtime)
}

// Runtime is one real adapter process, launched with production UID/GID 65532.
type Runtime struct {
	Root            string
	Client          *http.Client
	LabelLimit      int
	peer            *proofPeer
	logs            proofLog
	done            chan struct{}
	active          string
	downloads       atomic.Int64
	blockedDownload chan struct{}
	endedDownload   chan struct{}
	blockedFile     string
}

// Run executes the reusable lifecycle, authentication, and attribution suite,
// then any adapter-specific scenarios. Do not call it from parallel tests.
func Run(t *testing.T, contract Contract) {
	t.Helper()
	require.NotEmpty(t, contract.Executable)
	require.NotEmpty(t, contract.Basic.Complete.File)
	require.NotEmpty(t, contract.Basic.Reuse.File)
	require.NotNil(t, contract.Basic.AssertMetrics)
	require.Positive(t, contract.Basic.CompletionTimeout)
	t.Run("lifecycle", func(t *testing.T) {
		runtime := startRuntime(t, contract)
		runBasic(t, runtime, contract.Basic)
		if contract.Scenarios != nil {
			contract.Scenarios(t, runtime)
		}
	})
	for _, scenario := range contract.Budgets {
		t.Run("labels/"+scenario.Name, func(t *testing.T) {
			configured := contract
			configured.LabelLimit = scenario.LabelLimit
			runtime := startRuntime(t, configured)
			const id = "9001"
			runtime.Start(t, id, scenario.Fixture)
			runtime.Running(t, id)
			runtime.AwaitRequests(t, id, scenario.MinimumRequests)
			runtime.Stop(t, id)
			runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
			snapshot := contract.Basic.AssertMetrics(t, runtime, id)
			labels := make([]string, 0, len(snapshot.Requests))
			for label := range snapshot.Requests {
				labels = append(labels, label)
			}
			require.ElementsMatch(t, scenario.RequestLabels, labels)
			labels = labels[:0]
			for label := range snapshot.Bytes {
				labels = append(labels, label)
			}
			require.ElementsMatch(t, scenario.ByteLabels, labels)
			require.Equal(t, snapshot, contract.Basic.AssertMetrics(t, runtime, id), "retained budget metrics must not change")
		})
	}
}

func startRuntime(t *testing.T, contract Contract) *Runtime {
	t.Helper()
	require.Zero(t, os.Geteuid(), "run the adapter's container-only conformance target")
	_, err := os.Stat("/.dockerenv")
	require.NoError(t, err, "conformance owns fixed ports and /test-data; use a disposable Docker container")
	startProofDNS(t)
	certificate, ca := proofCertificate(t)
	r := &Runtime{peer: newProofPeer(t, certificate), LabelLimit: contract.LabelLimit, blockedDownload: make(chan struct{}, 1), endedDownload: make(chan struct{}, 1)}
	r.blockedFile = "conformance-blocked" + filepath.Ext(contract.Basic.Complete.File)
	artifacts := make(map[string]string, len(contract.Artifacts)+len(contract.DataArtifacts))
	for name, body := range contract.Artifacts {
		artifacts[proofArtifactPath(t, name)] = body
	}
	for name, body := range contract.DataArtifacts {
		artifacts[name] = body
	}
	if contract.Target != nil {
		startProofHTTP(t, "127.0.0.1:18080", contract.Target, nil)
	}
	startProofHTTP(t, "127.0.0.1:443", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+proofKey {
			t.Errorf("artifact request did not authenticate")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.downloads.Add(1)
		if request.URL.Path == proofArtifactPath(t, r.blockedFile) {
			select {
			case r.blockedDownload <- struct{}{}:
			default:
			}
			<-request.Context().Done()
			select {
			case r.endedDownload <- struct{}{}:
			default:
			}
			return
		}
		body, ok := artifacts[request.URL.Path]
		if !ok {
			http.NotFound(w, request)
			return
		}
		_, _ = io.WriteString(w, body)
	}), &certificate)
	r.Root, err = os.MkdirTemp("/tmp", "adapter-conformance-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(r.Root) })
	require.NoError(t, os.Chown(r.Root, 65532, 65532))
	environment := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + r.Root, "AGENT_ROOT=" + r.Root, "engine_name=engine-proof-plan-local-0", "offset=7", "coordinator_ip=127.0.0.1", "coordinator_ca_cert=" + ca, "collection_id=" + proofCollection, "plan_id=" + proofPlan, "zone=local", "api_key=" + proofKey, "label_limit=+" + strconv.Itoa(contract.LabelLimit)}
	process, err := launchRuntimeProcess(contract.Executable, environment, &r.logs)
	require.NoError(t, err)
	r.done = process.exited
	r.Client = &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(r.Client.CloseIdleConnections)
	t.Cleanup(func() {
		if r.active != "" {
			_ = r.peer.send(proofPayload(protocol.VerbStop, r.active, "", nil))
			deadline := time.Now().Add(proofWait)
		gracefulStop:
			for time.Now().Before(deadline) {
				select {
				case <-r.done:
					break gracefulStop
				default:
				}
				status, e := r.Progress(r.active)
				if e == nil && status.State.Terminal() {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
		if err := process.close(); err != nil {
			t.Logf("adapter output:\n%s", r.Logs())
			abortRuntimeCleanup(err)
		}
		if t.Failed() {
			t.Logf("adapter output:\n%s", r.Logs())
		}
	})
	r.peer.awaitSubscription(t)
	require.Eventually(t, func() bool {
		response, e := r.Client.Get("http://127.0.0.1:8080/healthz")
		if e != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, proofWait, 25*time.Millisecond)
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", process.pid))
	require.NoError(t, err)
	require.Contains(t, string(status), "Uid:\t65532\t65532\t65532\t65532")
	require.Contains(t, string(status), "Gid:\t65532\t65532\t65532\t65532")
	r.assertAuthentication(t)
	return r
}

func (r *Runtime) assertAuthentication(t *testing.T) {
	t.Helper()
	for _, endpoint := range []string{"/progress?run_id=8900", "/metrics"} {
		for _, auth := range []struct {
			header string
			status int
		}{
			{"", http.StatusBadRequest},
			{"Bearer wrong-key", http.StatusForbidden},
			{"Basic " + proofKey, http.StatusBadRequest},
		} {
			request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+endpoint, nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", auth.header)
			response, err := r.Client.Do(request)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, auth.status, response.StatusCode, "%s with authorization %q", endpoint, auth.header)
		}
	}
}

func runBasic(t *testing.T, r *Runtime, basic BasicScenarios) {
	first := r.Start(t, "8901", basic.Complete)
	r.Terminal(t, "8901", protocol.RunCompleted, basic.CompletionTimeout)
	final := basic.AssertMetrics(t, r, "8901")
	require.NotEmpty(t, final.Requests, "fixture must generate real requests")
	require.Equal(t, final, basic.AssertMetrics(t, r, "8901"), "final metrics must be retained without double ingestion")
	before := r.Downloads()
	r.Send(t, first)
	r.Stop(t, "8999")
	r.Terminal(t, "8999", protocol.RunCanceled, proofWait)
	require.Equal(t, before, r.Downloads(), "duplicate start must not download or execute again")
	r.Start(t, "8902", basic.Reuse)
	r.Running(t, "8902")
	r.AwaitRequests(t, "8902", 1)
	r.Stop(t, "8901")
	require.Never(t, func() bool { status, err := r.Progress("8902"); return err == nil && status.State.Terminal() }, 400*time.Millisecond, 25*time.Millisecond)
	r.Stop(t, "8902")
	r.Terminal(t, "8902", protocol.RunCanceled, proofWait)
	reset := basic.AssertMetrics(t, r, "8902")
	require.Equal(t, "8902", reset.Identity.RunID)
	r.Start(t, "8903", Fixture{File: r.blockedFile, Settings: basic.Reuse.Settings})
	r.AwaitDownload(t)
	r.Stop(t, "8903")
	r.Terminal(t, "8903", protocol.RunCanceled, proofWait)
	r.AwaitDownloadCancellation(t)
	r.Start(t, "8904", basic.Reuse)
	r.Running(t, "8904")
	r.AwaitRequests(t, "8904", 1)
	r.Stop(t, "8904")
	r.Terminal(t, "8904", protocol.RunCanceled, proofWait)
	basic.AssertMetrics(t, r, "8904")
	r.AssertAlive(t)
}

func Payload(verb, runID, filename string, settings json.RawMessage) protocol.Payload {
	return proofPayload(verb, runID, filename, settings)
}

func DataArtifactPath(t *testing.T, filename string) string {
	t.Helper()
	relative, err := protocol.EngineDataPath(proofCollection, proofPlan, filename, "7")
	require.NoError(t, err)
	return path.Join(protocol.ArtifactPathPrefix, relative)
}

func (r *Runtime) Start(t *testing.T, id string, fixture Fixture) protocol.Payload {
	t.Helper()
	payload := Payload(protocol.VerbStart, id, fixture.File, fixture.Settings)
	r.Send(t, payload)
	return payload
}

func (r *Runtime) Send(t *testing.T, payload protocol.Payload) {
	t.Helper()
	if payload.Verb == protocol.VerbStart {
		r.active = payload.PlanMessage[proofPlan].RunID
	}
	require.NoError(t, r.peer.send(payload))
}

func (r *Runtime) Stop(t *testing.T, id string) {
	t.Helper()
	r.Send(t, Payload(protocol.VerbStop, id, "", nil))
}

func (r *Runtime) Progress(id string) (protocol.RunStatus, error) { return proofProgress(r.Client, id) }
func (r *Runtime) Logs() string                                   { return r.logs.String() }
func (r *Runtime) Downloads() int64                               { return r.downloads.Load() }
func (r *Runtime) BlockedFile() string                            { return r.blockedFile }
func (r *Runtime) AwaitDownload(t *testing.T) {
	t.Helper()
	awaitProofEvent(t, r.blockedDownload, "artifact download")
}
func (r *Runtime) AwaitDownloadCancellation(t *testing.T) {
	t.Helper()
	awaitProofEvent(t, r.endedDownload, "download cancellation")
}

func (r *Runtime) Terminal(t *testing.T, id string, want protocol.RunState, timeout time.Duration) protocol.RunStatus {
	t.Helper()
	var status protocol.RunStatus
	require.Eventually(t, func() bool {
		var err error
		status, err = r.Progress(id)
		return err == nil && status.State.Terminal()
	}, timeout, 50*time.Millisecond, "run %s failed to finish; logs=%s", id, r.Logs())
	require.Equal(t, want, status.State, "%+v; logs=%s", status, r.Logs())
	return status
}

func (r *Runtime) Running(t *testing.T, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		status, err := r.Progress(id)
		return err == nil && status.State == protocol.RunRunning
	}, proofWait, 50*time.Millisecond, "logs=%s", r.Logs())
}

func (r *Runtime) Scrape(t *testing.T) string { t.Helper(); return Scrape(t, r.Client) }

func Scrape(t *testing.T, client *http.Client) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/metrics", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+proofKey)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	return string(body)
}

func (r *Runtime) AssertAlive(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
		t.Fatalf("adapter exited instead of recovering: %s", r.Logs())
	default:
	}
}

// AwaitRequests waits for real normalized request counts for this run only.
func (r *Runtime) AwaitRequests(t *testing.T, id string, minimum uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, err := parser.TextToMetricFamilies(strings.NewReader(r.Scrape(t)))
		if err != nil || families[metricschema.StatusCounter] == nil {
			return false
		}
		var count float64
		for _, metric := range families[metricschema.StatusCounter].Metric {
			for _, label := range metric.Label {
				if label.GetName() == metricschema.LabelRunID && label.GetValue() == id {
					count += metric.GetCounter().GetValue()
				}
			}
		}
		return count >= float64(minimum)
	}, proofWait, 100*time.Millisecond, "run %s must generate at least %d real requests", id, minimum)
}
