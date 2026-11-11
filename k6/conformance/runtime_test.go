//go:build integration && linux

// Protocol harness adapted from engine-sdk v0.3.0 preparing-adapter example.
// Copyright reqfleet contributors. Apache-2.0; modified for real k6 HTTP runs.
package conformance_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-adapters/conformance"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proofCollection = conformance.CollectionID
	proofPlan       = conformance.PlanID
	proofWait       = conformance.Wait
)

func TestPublicK6Runtime(t *testing.T) {
	provisioning := newProvisioningPeer(t)
	requests := &proofRequests{}
	inflight := make(chan struct{}, 1)
	inflightEnded := make(chan struct{}, 1)
	target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.record(r.URL.Path, r.Header.Get("X-Revision"))
		if strings.HasPrefix(r.URL.Path, "/data") {
			assert.Equal(t, "row-a|row-b", r.Header.Get("X-Data"))
		}
		if r.URL.Path == "/timeout" {
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/blocked" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case inflight <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case inflightEnded <- struct{}{}:
			default:
			}
			return
		}
		if r.URL.Path == "/500" {
			w.WriteHeader(500)
		}
		_, _ = io.WriteString(w, "real k6 conformance response\n")
	})
	artifacts := map[string]string{}
	add := func(name, body string) { artifacts[name] = body }
	firstSource := "\"use k6 >= 2.0.0 < 3.0.0\";\n" + httpScript("first", true)
	add("first.js", firstSource)
	add("second.js", httpScript("second", false))
	add("threshold.js", `import http from 'k6/http'; import {sleep} from 'k6';
export const options={thresholds:{http_req_failed:[{threshold:'rate<0',abortOnFail:true,delayAbortEval:'1s'}]}};
export default function(){http.get('http://127.0.0.1:18080/500');sleep(0.1);}`)
	add("inflight.js", `import http from 'k6/http'; export default function(){http.get('http://127.0.0.1:18080/blocked',{timeout:'120s'});}`)
	add("data.js", `import http from 'k6/http'; import {sleep} from 'k6'; const data=open('/test-data/rows.csv').trim().split('\n').join('|'); export default function(){http.get('http://127.0.0.1:18080/data',{headers:{'X-Data':data}});sleep(0.1);}`)
	add("dns.js", `import http from 'k6/http'; import {sleep} from 'k6';
export default function(){http.get('http://missing.invalid/');sleep(0.1);}`)
	add("refused.js", `import http from 'k6/http'; import {sleep} from 'k6';
export const options={systemTags:['method']};
export default function(){http.get('http://127.0.0.1:1/');sleep(0.1);}`)
	add("timeout.js", `import http from 'k6/http'; import {sleep} from 'k6';
export default function(){http.get('http://127.0.0.1:18080/timeout',{timeout:'100ms'});sleep(5);}`)
	for _, key := range []string{"scenarios", "vus", "duration", "iterations", "stages", "executionSegment", "executionSegmentSequence"} {
		add(key+".js", fmt.Sprintf("export const options={%s:null}; export default function(){}", key))
	}
	add("missing.js", "export const options = {};")
	add("syntax.js", "export default function(")
	add("tags.js", "export const options={systemTags:'status'};export default function(){}")
	add("verbose.js", `console.log("x".repeat(1024*1024)); throw new Error("verbose init failure"); export default function(){}`)
	add("extension.js", extensionPlan)
	add("engine-version.js", incompatiblePlan)
	add("extension-directive.js", extensionDirectivePlan)
	add("labels.js", `import http from 'k6/http'; import {sleep} from 'k6';
export default function(){for(const path of ['/200','/200','/200?item=a','/200?item=b','/500','/overflow','/overflow?item=c'])http.get('http://127.0.0.1:18080'+path);sleep(0.2);}`)
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	fixture := conformance.Fixture{File: "labels.js", Settings: settings}
	conformance.Run(t, conformance.Contract{
		Executable:    provisioning.adapterLauncher(t),
		Artifacts:     artifacts,
		DataArtifacts: map[string]string{conformance.DataArtifactPath(t, "rows.csv"): "row-a\nrow-b\n"},
		Target:        target,
		LabelLimit:    4,
		Basic: conformance.BasicScenarios{
			Complete:          conformance.Fixture{File: "first.js", Settings: settings},
			Reuse:             conformance.Fixture{File: "second.js", Settings: settings},
			CompletionTimeout: 90 * time.Second,
			AssertMetrics: func(t *testing.T, runtime *conformance.Runtime, run string) conformance.MetricsSnapshot {
				_, snapshot := compareCSVSnapshot(t, runtime.Client, filepath.Join(runtime.Root, "test-result", "k6.csv"), run, runtime.LabelLimit)
				return snapshot
			},
		},
		Budgets: []conformance.BudgetScenario{
			{Name: "overflow", LabelLimit: 4, Fixture: fixture, MinimumRequests: 14,
				RequestLabels: []string{conformance.TargetURL + "/200", conformance.TargetURL + "/200?item=a", conformance.TargetURL + "/200?item=b", conformance.TargetURL + "/500", "others"}, ByteLabels: []string{"k6"}},
			{Name: "zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 14,
				RequestLabels: []string{"others"}, ByteLabels: []string{"others"}},
		},
		Scenarios: func(t *testing.T, runtime *conformance.Runtime) {
			runK6Scenarios(t, runtime, requests, inflight, inflightEnded, firstSource)
			provisioning.assertUnused(t, runtime.Root)
		},
	})
	require.Zero(t, provisioning.requests.Load())
}

func runK6Scenarios(t *testing.T, runtime *conformance.Runtime, requests *proofRequests, inflight, inflightEnded <-chan struct{}, firstSource string) {
	client, root := runtime.Client, runtime.Root
	start := func(id, file, vus string, mode protocol.PartitionMode) protocol.Payload {
		t.Helper()
		settings, err := json.Marshal(map[string]string{"plan_name": "conformance", "concurrency": vus, "duration": "1", "rampup": "0"})
		require.NoError(t, err)
		payload := conformance.Payload(protocol.VerbStart, id, file, settings)
		if mode != "" {
			message := payload.PlanMessage[proofPlan]
			message.DataFiles = map[string]struct{}{"rows.csv": {}}
			message.RunConfigs["7"] = protocol.RunSpec{Settings: settings, Artifacts: map[string]protocol.ArtifactAssignment{"rows.csv": {Mode: mode, ShardIndex: 0, ShardCount: 2}}}
		}
		runtime.Send(t, payload)
		return payload
	}
	terminal := func(id string, want protocol.RunState, timeout time.Duration) protocol.RunStatus {
		t.Helper()
		return runtime.Terminal(t, id, want, timeout)
	}
	running := func(id string) { t.Helper(); runtime.Running(t, id) }
	stop := func(id string) { t.Helper(); runtime.Stop(t, id) }
	csvPath := filepath.Join(root, "test-result", "k6.csv")
	first := start("1001", "first.js", "2", "")
	running("1001")
	firstMetrics := scrape(t, client)
	require.Contains(t, firstMetrics, `run_id="1001"`)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	resources, err := parser.TextToMetricFamilies(strings.NewReader(firstMetrics))
	require.NoError(t, err)
	require.NotContains(t, resources, metricschema.CPUGauge, "the first scrape has no CPU interval")
	require.Contains(t, resources, metricschema.MemoryGauge)
	require.Len(t, resources[metricschema.MemoryGauge].Metric, 1)
	require.Positive(t, resources[metricschema.MemoryGauge].Metric[0].GetGauge().GetValue())
	terminal("1001", protocol.RunCompleted, 90*time.Second)
	resources, err = parser.TextToMetricFamilies(strings.NewReader(scrape(t, client)))
	require.NoError(t, err)
	require.Contains(t, resources, metricschema.CPUGauge)
	require.Len(t, resources[metricschema.CPUGauge].Metric, 1)
	require.Positive(t, resources[metricschema.CPUGauge].Metric[0].GetGauge().GetValue(), "the real k6 run must consume CPU")
	original, err := os.ReadFile("/test-data/script.js")
	require.NoError(t, err)
	require.Equal(t, firstSource, string(original))
	firstTotals := compareCSV(t, client, csvPath, "1001")
	require.Positive(t, firstTotals.status["200"])
	require.Positive(t, firstTotals.status["500"])
	require.Positive(t, firstTotals.bytes)
	require.Len(t, firstTotals.requests, 4)
	for _, endpoint := range []string{"/200", "/500", "/setup", "/teardown"} {
		url := "http://127.0.0.1:18080" + endpoint
		require.Contains(t, firstTotals.requests, url)
		require.Positive(t, firstTotals.requests[url].count)
		status := "200"
		if endpoint == "/500" {
			status = "500"
		}
		require.Equal(t, map[string]float64{status: float64(firstTotals.requests[url].count)}, firstTotals.requests[url].status)
	}
	require.Equal(t, firstTotals, compareCSV(t, client, csvPath, "1001"), "final scrapes must not double-ingest")
	seen := requests.snapshot()
	require.Contains(t, seen, proofRequest{Path: "/setup", Plan: "first"})
	require.Contains(t, seen, proofRequest{Path: "/teardown", Plan: "first"})
	before := runtime.Downloads()
	requestCount := len(seen)
	runtime.Send(t, first)
	stop("1999")
	terminal("1999", protocol.RunCanceled, proofWait)
	require.Equal(t, before, runtime.Downloads())
	require.Len(t, requests.snapshot(), requestCount, "duplicate start must not execute")

	start("1002", "second.js", "1", "")
	running("1002")
	stop("1001") // stale stop must not cancel this run
	require.Never(t, func() bool { status, e := runtime.Progress("1002"); return e == nil && status.State.Terminal() }, 400*time.Millisecond, 25*time.Millisecond)
	stop("1002")
	terminal("1002", protocol.RunCanceled, proofWait)
	secondTotals := compareCSV(t, client, csvPath, "1002")
	require.Less(t, secondTotals.count, firstTotals.count)
	require.Len(t, secondTotals.requests, 2)
	require.Contains(t, secondTotals.requests, "http://127.0.0.1:18080/200")
	require.Contains(t, secondTotals.requests, "http://127.0.0.1:18080/500")
	body := scrape(t, client)
	require.NotContains(t, body, `run_id="1001"`)
	for _, request := range requests.snapshot()[requestCount:] {
		require.Equal(t, "second", request.Plan)
	}

	start("1003", "first.js", "invalid", "")
	status := terminal("1003", protocol.RunFailed, proofWait)
	require.Equal(t, protocol.FailureStartup, status.FailurePhase)
	require.Contains(t, status.ErrorMessage, "concurrency")
	require.NoFileExists(t, filepath.Join(root, "run-tmp", "entry.js"))
	start("1004", runtime.BlockedFile(), "1", "")
	runtime.AwaitDownload(t)
	stop("1004")
	terminal("1004", protocol.RunCanceled, proofWait)
	runtime.AwaitDownloadCancellation(t)
	start("1005", "inflight.js", "1", "")
	awaitProofEvent(t, inflight, "in-flight request")
	stop("1005")
	terminal("1005", protocol.RunCanceled, proofWait)
	awaitProofEvent(t, inflightEnded, "in-flight cancellation")
	start("1006", "threshold.js", "1", "")
	status = terminal("1006", protocol.RunFailed, 90*time.Second)
	require.NotZero(t, status.ExitCode)
	for index, key := range []string{"scenarios", "vus", "duration", "iterations", "stages", "executionSegment", "executionSegmentSequence", "missing", "syntax", "tags", "verbose"} {
		id := strconv.Itoa(1100 + index)
		start(id, key+".js", "1", "")
		status = terminal(id, protocol.RunFailed, proofWait)
		require.Equal(t, protocol.FailureStartup, status.FailurePhase)
		require.Contains(t, status.ErrorMessage, "k6 inspect failed")
		switch key {
		case "missing":
			require.Contains(t, status.ErrorMessage, "k6 plan must export a default function")
		case "syntax":
			require.Contains(t, status.ErrorMessage, "script.js")
		case "tags":
			require.Contains(t, status.ErrorMessage, "k6 options.systemTags must be an array")
		case "verbose":
			require.Contains(t, status.ErrorMessage, "[stderr truncated;")
			require.Contains(t, status.ErrorMessage, "verbose init failure")
			require.LessOrEqual(t, len(status.ErrorMessage), 16*1024+256)
		default:
			require.Contains(t, status.ErrorMessage, "k6 scheduling options are managed by the collection")
		}
	}
	for index, failure := range []struct{ file, diagnostic string }{
		{"extension.js", "k6/x/faker"},
		{"engine-version.js", "k6:>999.0.0"},
		{"extension-directive.js", "k6/x/faker:>=1.0.0"},
	} {
		t.Run("pinned-engine/"+failure.file, func(t *testing.T) {
			id := strconv.Itoa(1150 + index)
			start(id, failure.file, "1", "")
			status := terminal(id, protocol.RunFailed, proofWait)
			require.Equal(t, protocol.FailureStartup, status.FailurePhase)
			require.Contains(t, status.ErrorMessage, failure.diagnostic)
			t.Logf("%s: %s", failure.file, status.ErrorMessage)
		})
	}
	for index, mode := range []protocol.PartitionMode{protocol.PartitionNative, protocol.PartitionReplicated, protocol.PartitionMaterialized} {
		id := strconv.Itoa(1200 + index)
		start(id, "data.js", "1", mode)
		if mode == protocol.PartitionNative {
			status = terminal(id, protocol.RunFailed, proofWait)
			require.Contains(t, status.ErrorMessage, "partition mode")
			continue
		}
		running(id)
		stop(id)
		terminal(id, protocol.RunCanceled, proofWait)
		compareCSV(t, client, csvPath, id)
		data, e := os.ReadFile("/test-data/rows.csv")
		require.NoError(t, e)
		require.Equal(t, "row-a\nrow-b\n", string(data))
	}
	for index, failure := range []struct {
		file, url, status string
		connected         bool
	}{
		{"dns.js", "http://missing.invalid/", "DNS host not found (k6 1101)", false},
		{"refused.js", "http://127.0.0.1:1/", "TCP connection refused (k6 1212)", false},
		{"timeout.js", "http://127.0.0.1:18080/timeout", "HTTP request timeout (k6 1050)", true},
	} {
		id := strconv.Itoa(1400 + index)
		start(id, failure.file, "1", "")
		running(id)
		require.Eventually(t, func() bool {
			parser := expfmt.NewTextParser(model.UTF8Validation)
			families, err := parser.TextToMetricFamilies(strings.NewReader(scrape(t, client)))
			if err != nil || families[metricschema.StatusCounter] == nil {
				return false
			}
			for _, metric := range families[metricschema.StatusCounter].Metric {
				for _, label := range metric.Label {
					if label.GetName() == metricschema.LabelStatus && label.GetValue() == failure.status && metric.GetCounter().GetValue() > 0 {
						return true
					}
				}
			}
			return false
		}, proofWait, 100*time.Millisecond, "failed requests must be visible while running")
		stop(id)
		terminal(id, protocol.RunCanceled, proofWait)
		failedTotals := compareCSV(t, client, csvPath, id)
		require.Len(t, failedTotals.requests, 1)
		require.Contains(t, failedTotals.requests, failure.url)
		require.Equal(t, map[string]float64{failure.status: float64(failedTotals.count)}, failedTotals.status)
		if failure.connected {
			require.Positive(t, failedTotals.sum, "waiting for a response contributes request duration")
		} else {
			require.Zero(t, failedTotals.sum, "failed connections still count despite zero http_req_duration")
		}
		require.Zero(t, failedTotals.bytes, "no response bytes may be fabricated")
		require.Equal(t, failedTotals, compareCSV(t, client, csvPath, id), "final error metrics must persist without double counting")
		t.Logf("%s: %d failed requests, status=%s, latency sum=%g ms", failure.file, failedTotals.count, failure.status, failedTotals.sum)
	}
	// Exercise positive ramp-up through the actual engine, not generated source.
	rampSettings, err := json.Marshal(map[string]string{"concurrency": "3", "duration": "1", "rampup": "3"})
	require.NoError(t, err)
	active := "1300"
	runtime.Send(t, conformance.Payload(protocol.VerbStart, active, "second.js", rampSettings))
	running(active)
	require.Eventually(t, func() bool {
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, e := parser.TextToMetricFamilies(strings.NewReader(scrape(t, client)))
		if e != nil {
			return false
		}
		if family := families[metricschema.ThreadsGauge]; family != nil {
			for _, metric := range family.Metric {
				if metric.GetGauge().GetValue() == 3 {
					return true
				}
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "ramp must reach all three assigned VUs")
	stop(active)
	terminal(active, protocol.RunCanceled, proofWait)
	compareCSV(t, client, csvPath, "1300")
	runtime.AssertAlive(t)
}

func httpScript(revision string, hooks bool) string {
	script := fmt.Sprintf(`import http from 'k6/http'; import {sleep} from 'k6';
const headers={'X-Revision':%q};
export default function(){http.get('http://127.0.0.1:18080/200',{headers});http.get('http://127.0.0.1:18080/500',{headers});sleep(0.2);}
`, revision)
	if hooks {
		script += `export const options={systemTags:['method'],thresholds:{'http_req_duration{status:200}':['max<10000']}};
export function setup(){http.get('http://127.0.0.1:18080/setup',{headers});return {revision:'first'};}
export function teardown(data){if(data.revision!=='first')throw new Error('lost setup data');http.get('http://127.0.0.1:18080/teardown',{headers});}
export function handleSummary(){throw new Error('adapter must not forward handleSummary');}
`
	}
	return script
}

type totals struct {
	count      uint64
	sum, bytes float64
	status     map[string]float64
	vus        float64
	requests   map[string]*requestTotals
}

type requestTotals struct {
	count  uint64
	sum    float64
	status map[string]float64
}

func scrape(t *testing.T, client *http.Client) string {
	t.Helper()
	return conformance.Scrape(t, client)
}

func compareCSV(t *testing.T, client *http.Client, filename, run string) totals {
	t.Helper()
	observed, _ := compareCSVSnapshot(t, client, filename, run, 4)
	return observed
}

func compareCSVSnapshot(t *testing.T, client *http.Client, filename, run string, labelLimit int) (totals, conformance.MetricsSnapshot) {
	t.Helper()
	file, err := os.Open(filename)
	require.NoError(t, err)
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	require.NoError(t, err)
	columns := map[string]int{}
	for i, key := range header {
		columns[key] = i
	}
	for _, key := range []string{"metric_name", "metric_value", "status", "url", "method", "error_code"} {
		require.Contains(t, columns, key)
	}
	expected := totals{status: map[string]float64{}, requests: map[string]*requestTotals{}}
	durations := map[string][]float64{}
	admitted := map[string]bool{}
	for {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		require.NoError(t, e)
		value, e := strconv.ParseFloat(row[columns["metric_value"]], 64)
		require.NoError(t, e)
		switch row[columns["metric_name"]] {
		case "http_req_duration":
			url := row[columns["url"]]
			require.NotEmpty(t, url, "duration rows must retain their request URL")
			require.Equal(t, "GET", row[columns["method"]], "existing system tags must be preserved")
			if !admitted[url] {
				if len(admitted) < labelLimit {
					admitted[url] = true
				} else {
					url = "others"
				}
			}
			if expected.requests[url] == nil {
				expected.requests[url] = &requestTotals{status: map[string]float64{}}
			}
			request := expected.requests[url]
			status := row[columns["status"]]
			if status == "0" {
				switch row[columns["error_code"]] {
				case "1101":
					status = "DNS host not found (k6 1101)"
				case "1212":
					status = "TCP connection refused (k6 1212)"
				case "1050":
					status = "HTTP request timeout (k6 1050)"
				default:
					t.Fatalf("unexpected transport failure in fixture: %s", row[columns["error_code"]])
				}
			}
			request.count++
			request.sum += value
			request.status[status]++
			durations[url] = append(durations[url], value)
			expected.count++
			expected.sum += value
			expected.status[status]++
		case "data_received":
			expected.bytes += value
		case "vus":
			expected.vus = value
		}
	}
	require.Positive(t, expected.count)
	requests := make(map[string]conformance.RequestExpectation, len(expected.requests))
	for label, request := range expected.requests {
		statuses := make(map[string]uint64, len(request.status))
		for status, count := range request.status {
			statuses[status] = uint64(count)
		}
		requests[label] = conformance.RequestExpectation{Statuses: statuses, LatenciesMS: durations[label]}
	}
	byteLabel := "k6"
	if labelLimit == 0 {
		byteLabel = "others"
	}
	snapshot := conformance.AssertMetrics(t, scrape(t, client), conformance.MetricsExpectation{
		Identity:    conformance.MetricIdentity{CollectionID: proofCollection, PlanID: proofPlan, RunID: run, EngineNo: "7", Zone: "local"},
		Requests:    requests,
		Bytes:       map[string]float64{byteLabel: expected.bytes},
		Concurrency: &expected.vus,
		Resources:   conformance.ResourceExpectation{Memory: conformance.MeasurementRequired},
	})
	observed := totals{status: map[string]float64{}, requests: map[string]*requestTotals{}, vus: *snapshot.Concurrency}
	// Stable addition order keeps retained-scrape comparisons deterministic.
	for _, label := range slices.Sorted(maps.Keys(snapshot.Requests)) {
		request := snapshot.Requests[label]
		value := &requestTotals{count: request.Latency.Count, sum: request.Latency.Sum, status: map[string]float64{}}
		observed.count += value.count
		observed.sum += value.sum
		for status, count := range request.Statuses {
			value.status[status] = float64(count)
			observed.status[status] += float64(count)
		}
		observed.requests[label] = value
	}
	for _, bytes := range snapshot.Bytes {
		observed.bytes += bytes
	}
	return observed, snapshot
}

type proofRequest struct {
	Path string
	Plan string
}

type proofRequests struct {
	mu       sync.Mutex
	requests []proofRequest
}

func (p *proofRequests) record(path, plan string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, proofRequest{Path: path, Plan: plan})
}

func (p *proofRequests) snapshot() []proofRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proofRequest(nil), p.requests...)
}

func awaitProofEvent(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(proofWait):
		t.Fatalf("timed out waiting for %s", description)
	}
}
