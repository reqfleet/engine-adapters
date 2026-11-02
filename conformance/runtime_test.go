//go:build integration && linux

// Protocol harness adapted from engine-sdk v0.3.0 preparing-adapter example.
// Copyright reqfleet contributors. Apache-2.0; modified for real k6 HTTP runs.
package conformance

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixed privileged ports and /test-data belong ONLY to the disposable image.
// The adapter itself runs as the production UID/GID 65532, not harness root.
func TestPublicK6Runtime(t *testing.T) {
	require.Zero(t, os.Geteuid(), "use make test-conformance")
	_, err := os.Stat("/.dockerenv")
	require.NoError(t, err)
	certificate, ca := proofCertificate(t)
	peer := newProofPeer(t, certificate)
	requests := &proofRequests{}
	inflight := make(chan struct{}, 1)
	inflightEnded := make(chan struct{}, 1)
	startProofHTTP(t, "127.0.0.1:18080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.record(r.URL.Path, r.Header.Get("X-Revision"))
		if strings.HasPrefix(r.URL.Path, "/data") {
			assert.Equal(t, "row-a|row-b", r.Header.Get("X-Data"))
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
	}), nil)
	blockedDownload := make(chan struct{}, 1)
	endedDownload := make(chan struct{}, 1)
	var downloads atomic.Int64
	artifacts := map[string]string{}
	add := func(name, body string) { artifacts[proofArtifactPath(t, name)] = body }
	firstSource := httpScript("first", true)
	add("first.js", firstSource)
	add("second.js", httpScript("second", false))
	add("threshold.js", `import http from 'k6/http'; import {sleep} from 'k6';
export const options={thresholds:{http_req_failed:[{threshold:'rate<0',abortOnFail:true,delayAbortEval:'1s'}]}};
export default function(){http.get('http://127.0.0.1:18080/500');sleep(0.1);}`)
	add("inflight.js", `import http from 'k6/http'; export default function(){http.get('http://127.0.0.1:18080/blocked',{timeout:'120s'});}`)
	add("data.js", `import http from 'k6/http'; import {sleep} from 'k6'; const data=open('/test-data/rows.csv').trim().split('\n').join('|'); export default function(){http.get('http://127.0.0.1:18080/data',{headers:{'X-Data':data}});sleep(0.1);}`)
	for _, key := range []string{"scenarios", "vus", "duration", "iterations", "stages", "executionSegment", "executionSegmentSequence"} {
		add(key+".js", fmt.Sprintf("export const options={%s:null}; export default function(){}", key))
	}
	add("missing.js", "export const options = {};")
	add("syntax.js", "export default function(")
	add("tags.js", "export const options={systemTags:'status'};export default function(){}")
	dataPath, err := protocol.EngineDataPath(proofCollection, proofPlan, "rows.csv", "7")
	require.NoError(t, err)
	artifacts[path.Join(protocol.ArtifactPathPrefix, dataPath)] = "row-a\nrow-b\n"
	startProofHTTP(t, "127.0.0.1:443", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "Bearer "+proofKey, r.Header.Get("Authorization")) {
			http.Error(w, "unauthorized", 401)
			return
		}
		downloads.Add(1)
		if r.URL.Path == proofArtifactPath(t, "blocked.js") {
			select {
			case blockedDownload <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case endedDownload <- struct{}{}:
			default:
			}
			return
		}
		body, ok := artifacts[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}), &certificate)

	root, err := os.MkdirTemp("/tmp", "k6-conformance-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.NoError(t, os.Chown(root, 65532, 65532))
	var logs proofLog
	child := exec.Command("/usr/local/bin/k6-adapter")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	child.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + root, "AGENT_ROOT=" + root, "engine_name=engine-proof-plan-local-0", "offset=7", "coordinator_ip=127.0.0.1", "coordinator_ca_cert=" + ca, "collection_id=" + proofCollection, "plan_id=" + proofPlan, "zone=local", "api_key=" + proofKey}
	child.Stdout, child.Stderr = &logs, &logs
	require.NoError(t, child.Start())
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	active := ""
	t.Cleanup(func() {
		if active != "" {
			_ = peer.send(proofPayload(protocol.VerbStop, active, "", nil))
			deadline := time.Now().Add(proofWait)
			for time.Now().Before(deadline) {
				status, e := proofProgress(client, active)
				if e == nil && status.State.Terminal() {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
		_ = child.Process.Kill()
		<-done
		if t.Failed() {
			t.Logf("adapter output:\n%s", logs.String())
		}
	})
	peer.awaitSubscription(t)
	require.Eventually(t, func() bool {
		response, e := client.Get("http://127.0.0.1:8080/healthz")
		if e != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	}, proofWait, 25*time.Millisecond)
	processStatus, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", child.Process.Pid))
	require.NoError(t, err)
	require.Contains(t, string(processStatus), "Uid:\t65532\t65532\t65532\t65532")
	start := func(id, file, vus string, mode protocol.PartitionMode) protocol.Payload {
		t.Helper()
		settings, e := json.Marshal(map[string]string{"plan_name": "conformance", "concurrency": vus, "duration": "1", "rampup": "0"})
		require.NoError(t, e)
		payload := proofPayload(protocol.VerbStart, id, file, settings)
		if mode != "" {
			message := payload.PlanMessage[proofPlan]
			message.DataFiles = map[string]struct{}{"rows.csv": {}}
			message.RunConfigs["7"] = protocol.RunSpec{Settings: settings, Artifacts: map[string]protocol.ArtifactAssignment{"rows.csv": {Mode: mode, ShardIndex: 0, ShardCount: 2}}}
		}
		active = id
		require.NoError(t, peer.send(payload))
		return payload
	}
	terminal := func(id string, want protocol.RunState, timeout time.Duration) protocol.RunStatus {
		t.Helper()
		var status protocol.RunStatus
		require.Eventually(t, func() bool {
			var e error
			status, e = proofProgress(client, id)
			return e == nil && status.State.Terminal()
		}, timeout, 50*time.Millisecond, "run %s failed to finish; logs=%s", id, logs.String())
		require.Equal(t, want, status.State, "%+v; logs=%s", status, logs.String())
		return status
	}
	running := func(id string) {
		t.Helper()
		require.Eventually(t, func() bool {
			status, e := proofProgress(client, id)
			return e == nil && status.State == protocol.RunRunning
		}, proofWait, 50*time.Millisecond, "logs=%s", logs.String())
	}
	stop := func(id string) {
		t.Helper()
		require.NoError(t, peer.send(proofPayload(protocol.VerbStop, id, "", nil)))
	}
	csvPath := filepath.Join(root, "test-result", "k6.csv")
	first := start("1001", "first.js", "2", "")
	running("1001")
	firstMetrics := scrape(t, client)
	require.Contains(t, firstMetrics, `run_id="1001"`)
	terminal("1001", protocol.RunCompleted, 90*time.Second)
	original, err := os.ReadFile("/test-data/script.js")
	require.NoError(t, err)
	require.Equal(t, firstSource, string(original))
	firstTotals := compareCSV(t, client, csvPath, "1001")
	require.Positive(t, firstTotals.status["200"])
	require.Positive(t, firstTotals.status["500"])
	require.Positive(t, firstTotals.bytes)
	require.Equal(t, firstTotals, compareCSV(t, client, csvPath, "1001"), "final scrapes must not double-ingest")
	seen := requests.snapshot()
	require.Contains(t, seen, proofRequest{Path: "/setup", Plan: "first"})
	require.Contains(t, seen, proofRequest{Path: "/teardown", Plan: "first"})
	before := downloads.Load()
	requestCount := len(seen)
	require.NoError(t, peer.send(first))
	stop("1999")
	terminal("1999", protocol.RunCanceled, proofWait)
	require.Equal(t, before, downloads.Load())
	require.Len(t, requests.snapshot(), requestCount, "duplicate start must not execute")

	start("1002", "second.js", "1", "")
	running("1002")
	stop("1001") // stale stop must not cancel this run
	require.Never(t, func() bool { status, e := proofProgress(client, "1002"); return e == nil && status.State.Terminal() }, 400*time.Millisecond, 25*time.Millisecond)
	stop("1002")
	terminal("1002", protocol.RunCanceled, proofWait)
	secondTotals := compareCSV(t, client, csvPath, "1002")
	require.Less(t, secondTotals.count, firstTotals.count)
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
	start("1004", "blocked.js", "1", "")
	awaitProofEvent(t, blockedDownload, "artifact download")
	stop("1004")
	terminal("1004", protocol.RunCanceled, proofWait)
	awaitProofEvent(t, endedDownload, "download cancellation")
	start("1005", "inflight.js", "1", "")
	awaitProofEvent(t, inflight, "in-flight request")
	stop("1005")
	terminal("1005", protocol.RunCanceled, proofWait)
	awaitProofEvent(t, inflightEnded, "in-flight cancellation")
	start("1006", "threshold.js", "1", "")
	status = terminal("1006", protocol.RunFailed, 90*time.Second)
	require.NotZero(t, status.ExitCode)
	for index, key := range []string{"scenarios", "vus", "duration", "iterations", "stages", "executionSegment", "executionSegmentSequence", "missing", "syntax", "tags"} {
		logOffset := len(logs.String())
		id := strconv.Itoa(1100 + index)
		start(id, key+".js", "1", "")
		status = terminal(id, protocol.RunFailed, proofWait)
		require.NotZero(t, status.ExitCode)
		if index < 7 {
			require.Contains(t, logs.String()[logOffset:], "k6 scheduling options are managed by the collection")
		}
	}
	require.Contains(t, logs.String(), "k6 plan must export a default function")
	require.Contains(t, logs.String(), "k6 options.systemTags must be an array")
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
	// Exercise positive ramp-up through the actual engine, not generated source.
	rampSettings, err := json.Marshal(map[string]string{"concurrency": "3", "duration": "1", "rampup": "3"})
	require.NoError(t, err)
	active = "1300"
	require.NoError(t, peer.send(proofPayload(protocol.VerbStart, active, "second.js", rampSettings)))
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
	select {
	case <-done:
		t.Fatalf("agent exited instead of recovering: %s", logs.String())
	default:
	}
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
}

func scrape(t *testing.T, client *http.Client) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/metrics", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+proofKey)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode, string(body))
	return string(body)
}

func compareCSV(t *testing.T, client *http.Client, filename, run string) totals {
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
	for _, key := range []string{"metric_name", "metric_value", "status"} {
		require.Contains(t, columns, key)
	}
	expected := totals{status: map[string]float64{}}
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
			expected.count++
			expected.sum += value
			expected.status[row[columns["status"]]]++
		case "data_received":
			expected.bytes += value
		case "vus":
			expected.vus = value
		}
	}
	require.Positive(t, expected.count)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(scrape(t, client)))
	require.NoError(t, err)
	observed := totals{status: map[string]float64{}}
	for name, family := range families {
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if !strings.HasPrefix(name, "shibuya_") {
				continue
			}
			require.Equal(t, run, labels[metricschema.LabelRunID])
			require.Equal(t, "7", labels[metricschema.LabelEngineNo])
			require.Equal(t, proofCollection, labels[metricschema.LabelCollectionID])
			require.Equal(t, proofPlan, labels[metricschema.LabelPlanID])
			require.Equal(t, "local", labels[metricschema.LabelZone])
			if label, ok := labels[metricschema.LabelRequest]; ok {
				require.Equal(t, "k6", label)
			}
			switch name {
			case metricschema.RequestLatency:
				observed.count += metric.GetHistogram().GetSampleCount()
				observed.sum += metric.GetHistogram().GetSampleSum()
			case metricschema.StatusCounter:
				observed.status[labels[metricschema.LabelStatus]] += metric.GetCounter().GetValue()
			case metricschema.EgressBytesCounter:
				observed.bytes += metric.GetCounter().GetValue()
			case metricschema.ThreadsGauge:
				observed.vus = metric.GetGauge().GetValue()
			case metricschema.MemoryGauge:
				require.Positive(t, metric.GetGauge().GetValue())
			}
		}
	}
	require.Equal(t, expected.count, observed.count)
	require.InDelta(t, expected.sum, observed.sum, 0.000001)
	require.Equal(t, expected.status, observed.status)
	require.InDelta(t, expected.bytes, observed.bytes, 0.000001)
	require.Equal(t, expected.vus, observed.vus)
	return observed
}
