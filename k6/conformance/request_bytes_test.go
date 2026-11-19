//go:build integration && linux

package conformance_test

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reqfleet/engine-adapters/k6/metrics"
	"github.com/reqfleet/engine-sdk/conformance"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

var requestByteBinary = []byte{0, 1, 127, 128, 193, 255, 0, 42}

const requestByteText = "界🙂é\x00response\n"

type requestByteCase struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	Size  int    `json:"-"`
}

func requestByteCases() []requestByteCase {
	base := conformance.TargetURL + "/request-bytes"
	boundary := "boundary/" + strings.Repeat("a", metricschema.MaxRequestLabelBytes-len("boundary/"))
	multibyte := strings.Repeat("界", metricschema.MaxRequestLabelBytes/len("界"))
	return []requestByteCase{
		{base + "/binary", "others", len(requestByteBinary)},
		{base + "/binary", base + "/binary", len(requestByteBinary)},
		{base + "/binary", base + "/binary", len(requestByteBinary)},
		{base + "/text?variant=a", base + "/text?variant=a", len(requestByteText)},
		{base + "/text?variant=b", base + "/text?variant=b", len(requestByteText)},
		{base + "/gzip", "body/group", len(requestByteText)},
		{base + "/binary", boundary, len(requestByteBinary)},
		{base + "/binary", boundary + "b", len(requestByteBinary)},
		{base + "/text", multibyte, len(requestByteText)},
		{base + "/text", multibyte + "界", len(requestByteText)},
		{base + "/overflow", base + "/overflow", len(requestByteBinary)},
	}
}

func serveRequestBytes(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/request-bytes/") {
		return false
	}
	switch r.URL.Path {
	case "/request-bytes/text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, requestByteText)
	case "/request-bytes/gzip":
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		compressed := gzip.NewWriter(w)
		_, _ = io.WriteString(compressed, requestByteText)
		_ = compressed.Close()
	case "/request-bytes/done":
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Path == "/request-bytes/overflow" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write(requestByteBinary)
	}
	return true
}

func requestByteScript(source string, emit bool) string {
	cases, err := json.Marshal(requestByteCases())
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {Counter} from 'k6/metrics';
export const options={systemTags:['method'],ext:{reqfleet:{receivedBytes:%s}}};
const received=new Counter(%q);
const cases=%s;
export default function(){
  if(__ITER>0){sleep(1);return;}
  const measured=[];
  for(const item of cases){
    const response=http.get(item.url,{responseType:'binary',tags:{name:item.label}});
    measured.push({label:item.label,size:response.body.byteLength});
  }
  if(%t){
    // Delayed reverse-order counters must not be joined to HTTP rows or names.
    for(const item of measured.slice().reverse())received.add(item.size,{url:item.label});
  }
  // This request proves that the preceding counter batch has been emitted.
  http.get(%q,{responseType:'binary'});
  sleep(1);
}
`, source, metrics.RequestReceivedBytesMetric, cases, emit, conformance.TargetURL+"/request-bytes/done")
}

func requestByteFixtures(artifacts map[string]string) []conformance.BudgetScenario {
	artifacts["request-bytes.js"] = requestByteScript(strconv.Quote(metrics.RequestReceivedBytesMetric), true)
	artifacts["request-bytes-empty.js"] = requestByteScript(`""`, true)
	artifacts["request-bytes-absent.js"] = strings.Replace(requestByteScript(`""`, true), `,ext:{reqfleet:{receivedBytes:""}}`, "", 1)
	artifacts["request-bytes-none.js"] = requestByteScript(strconv.Quote(metrics.RequestReceivedBytesMetric), false)
	for index, source := range []string{strconv.Quote(metrics.DataReceivedMetric), `"unknown"`, `true`, `42`, `[]`, `{}`, `null`} {
		artifacts[fmt.Sprintf("request-bytes-invalid-%d.js", index)] = requestByteScript(source, true)
	}
	for name, sample := range map[string]string{
		"missing-url": `received.add(1,{name:'not-a-url'});`,
		"negative":    `received.add(-1,{url:'negative'});`,
	} {
		artifacts["request-bytes-"+name+".js"] = `import {sleep} from 'k6';
import {Counter} from 'k6/metrics';
export const options={ext:{reqfleet:{receivedBytes:'` + metrics.RequestReceivedBytesMetric + `'}}};
const received=new Counter('` + metrics.RequestReceivedBytesMetric + `');
export default function(){` + sample + `sleep(1);}`
	}
	cases := requestByteCases()
	base := conformance.TargetURL + "/request-bytes"
	fixture := conformance.Fixture{File: "request-bytes.js", Settings: json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	firstFour := []string{"others", base + "/binary", base + "/text?variant=a", base + "/text?variant=b", "body/group"}
	all := append(append([]string{}, firstFour...), cases[6].Label, cases[8].Label, base+"/overflow")
	return []conformance.BudgetScenario{
		{Name: "request-bytes-overflow", LabelLimit: 4, Fixture: fixture, MinimumRequests: 10, RequestLabels: firstFour, ByteLabels: firstFour},
		{Name: "request-bytes-zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 10, RequestLabels: []string{"others"}, ByteLabels: []string{"others"}},
		{Name: "request-bytes-length", LabelLimit: 16, Fixture: fixture, MinimumRequests: 10, RequestLabels: append(append([]string{}, all...), base+"/done"), ByteLabels: all},
	}
}

// In addition to the CSV/exposition oracle, assert actual binary body lengths.
// Gzip measures the decoded response body, not compressed or HTTP wire bytes.
func assertRequestByteBodies(t *testing.T, runtime *conformance.Runtime, run string, snapshot conformance.MetricsSnapshot) {
	t.Helper()
	file, err := os.Open(filepath.Join(runtime.Root, "test-result", "k6.csv"))
	require.NoError(t, err)
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	columns := map[string]int{}
	for index, name := range rows[0] {
		columns[name] = index
	}
	wantSamples := map[string][]float64{}
	var expectedTotal float64
	var expectedRequests uint64 = 1 // The uninstrumented completion request.
	for _, item := range requestByteCases() {
		wantSamples[item.Label] = append(wantSamples[item.Label], float64(item.Size))
		if len(item.Label) <= metricschema.MaxRequestLabelBytes {
			expectedTotal += float64(item.Size)
			expectedRequests++
		}
	}
	actualSamples := map[string][]float64{}
	var builtIn float64
	for _, row := range rows[1:] {
		value, err := strconv.ParseFloat(row[columns["metric_value"]], 64)
		require.NoError(t, err)
		switch row[columns["metric_name"]] {
		case metrics.DataReceivedMetric:
			builtIn += value
		case metrics.RequestReceivedBytesMetric:
			actualSamples[row[columns["url"]]] = append(actualSamples[row[columns["url"]]], value)
		}
	}
	require.Equal(t, wantSamples, actualSamples, "real k6 binary ArrayBuffer measurements, including UTF-8 and decoded gzip")
	require.Positive(t, builtIn, "the ignored aggregate must genuinely exist in the same CSV")
	require.NotContains(t, snapshot.Bytes, "k6", "opt-in must not expose the aggregate series")
	var bytes float64
	for _, value := range snapshot.Bytes {
		bytes += value
	}
	require.Equal(t, expectedTotal, bytes, "oversized samples are discarded, not truncated; aggregate bytes are not added")
	var count uint64
	for _, request := range snapshot.Requests {
		count += request.Latency.Count
	}
	require.Equal(t, expectedRequests, count, "counters never fabricate latency or double request counts")
	require.Equal(t, run, snapshot.Identity.RunID)
}

func runRequestByteScenarios(t *testing.T, runtime *conformance.Runtime) {
	t.Helper()
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	csvPath := filepath.Join(runtime.Root, "test-result", "k6.csv")
	for index, scenario := range []struct {
		file, source string
		emit         bool
	}{
		{"request-bytes.js", metrics.RequestReceivedBytesMetric, true},
		{"request-bytes-empty.js", metrics.DataReceivedMetric, true},
		{"request-bytes.js", metrics.RequestReceivedBytesMetric, true},
		{"request-bytes-none.js", metrics.RequestReceivedBytesMetric, false},
		{"request-bytes-absent.js", metrics.DataReceivedMetric, true},
		{"request-bytes-empty.js", metrics.DataReceivedMetric, true},
	} {
		t.Run("request-bytes-reuse/"+strconv.Itoa(index), func(t *testing.T) {
			id := strconv.Itoa(1600 + index)
			runtime.Start(t, id, conformance.Fixture{File: scenario.file, Settings: settings})
			runtime.Running(t, id)
			runtime.AwaitRequests(t, id, 10)
			runtime.Stop(t, id)
			runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
			observed, snapshot := compareCSVSnapshotSource(t, runtime.Client, csvPath, id, runtime.LabelLimit, scenario.source)
			if scenario.source == metrics.RequestReceivedBytesMetric {
				original, err := os.ReadFile("/test-data/script.js")
				require.NoError(t, err)
				require.Equal(t, requestByteScript(strconv.Quote(metrics.RequestReceivedBytesMetric), scenario.emit), string(original), "selection must not rewrite the uploaded plan")
				if scenario.emit {
					assertRequestByteBodies(t, runtime, id, snapshot)
				} else {
					require.Empty(t, snapshot.Bytes, "selected mode without samples cannot fall back to data_received or fabricate zero series")
					raw, err := os.ReadFile(csvPath)
					require.NoError(t, err)
					require.Contains(t, string(raw), metrics.DataReceivedMetric+",", "built-in measurements exist even when no custom counter was emitted")
					require.NotContains(t, string(raw), metrics.RequestReceivedBytesMetric+",")
				}
			} else {
				require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes, "custom counters are ignored without opt-in")
				require.Positive(t, observed.bytes)
			}
			_, retained := compareCSVSnapshotSource(t, runtime.Client, csvPath, id, runtime.LabelLimit, scenario.source)
			require.Equal(t, snapshot, retained, "final custom-byte snapshots must persist without duplicate ingestion")
		})
	}
	for index := range 7 {
		id := strconv.Itoa(1650 + index)
		runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("request-bytes-invalid-%d.js", index), Settings: settings})
		status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
		require.Equal(t, protocol.FailureStartup, status.FailurePhase)
		require.Contains(t, status.ErrorMessage, "receivedBytes")
		require.NotContains(t, scrape(t, runtime.Client), `run_id="1605"`, "accepting a new run clears prior measurements even if preparation fails")
	}
	for index, failure := range []struct{ name, diagnostic string }{
		{"missing-url", "missing received bytes request URL"},
		{"negative", "invalid " + metrics.RequestReceivedBytesMetric + " sample"},
	} {
		t.Run("request-bytes-invalid-sample/"+failure.name, func(t *testing.T) {
			id := strconv.Itoa(1670 + index)
			runtime.Start(t, id, conformance.Fixture{File: "request-bytes-" + failure.name + ".js", Settings: settings})
			status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
			require.Contains(t, status.ErrorMessage, failure.diagnostic)
		})
	}
	// A failed selection must not poison the next prepared run.
	id := "1660"
	runtime.Start(t, id, conformance.Fixture{File: "request-bytes.js", Settings: settings})
	runtime.Running(t, id)
	runtime.AwaitRequests(t, id, 10)
	runtime.Stop(t, id)
	runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
	_, snapshot := compareCSVSnapshotSource(t, runtime.Client, csvPath, id, runtime.LabelLimit, metrics.RequestReceivedBytesMetric)
	assertRequestByteBodies(t, runtime, id, snapshot)
	runtime.AssertAlive(t)
}
