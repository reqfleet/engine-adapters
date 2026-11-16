//go:build integration && linux

package conformance_test

import (
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
	"time"

	"github.com/reqfleet/engine-adapters/k6/metrics"
	"github.com/reqfleet/engine-sdk/conformance"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

const (
	llmResponseSource = "llm_response_duration"
	llmTTFTSource     = "llm_time_to_first_token"
	llmFinalResponse  = 23456.75
	llmFinalTTFT      = 12.25
)

type llmTimingSelection struct {
	response bool
	ttft     bool
}

func serveLLMTiming(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/llm/") {
		return false
	}
	if r.URL.Path == "/llm/response" || r.URL.Path == "/llm/token" {
		select {
		case <-time.After(30 * time.Millisecond):
		case <-r.Context().Done():
			return true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"token":"hello"}`)
	return true
}

// Deterministic observations cover either side of every finite bound, zero,
// and +Inf. They are test data, not pretend multi-minute engine operations.
func llmBoundaryValues(bounds []float64) []float64 {
	values := []float64{0}
	for _, bound := range bounds {
		values = append(values, bound-0.25, bound, bound+0.25)
	}
	return append(values, 400000)
}

func llmTimingScript(selectors string, emit bool) string {
	responseValues, err := json.Marshal(llmBoundaryValues(metricschema.LLMResponseLatencyBuckets()))
	if err != nil {
		panic(err)
	}
	ttftValues, err := json.Marshal(llmBoundaryValues(metricschema.RequestLatencyBuckets()))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {Trend} from 'k6/metrics';
export const options={systemTags:['method']%s};
const responseDuration=new Trend('%s',true);
const firstToken=new Trend('%s',true);
export default function(){
  if(__ITER>0){sleep(0.1);return;}
  // First admission is HTTP, so a one-label budget folds both timings.
  http.get('%s/llm/seed');
  if(%t){
    for(const value of %s)responseDuration.add(value);
    for(const value of %s)firstToken.add(value);
  }
  // The literal HTTP grouping label llm_resp must still use ordinary buckets.
  const response=http.get('%s/llm/response',{tags:{name:'llm_resp'}});
  if(%t)responseDuration.add(response.timings.duration);
  const started=Date.now();
  const tokenResponse=http.get('%s/llm/token');
  // This fixture delivers exactly one token in one buffered HTTP response.
  // Time its availability to the consumer, not HTTP first-byte/waiting time.
  function onToken(token){
    if(typeof token!=='string'||token.length===0)throw new Error('missing token');
    if(%t)firstToken.add(Date.now()-started);
  }
  onToken(tokenResponse.json('token'));
  http.get('%s/llm/overflow');
  // Waiting for five HTTP outcomes proves all custom observations were sent.
  http.get('%s/llm/done');
  sleep(0.1);
}
export function teardown(){
  // These observations are emitted only after Stop, testing final draining.
  if(%t){responseDuration.add(%g);firstToken.add(%g);}
}
`, selectors, llmResponseSource, llmTTFTSource, conformance.TargetURL,
		emit, responseValues, ttftValues, conformance.TargetURL, emit,
		conformance.TargetURL, emit, conformance.TargetURL, conformance.TargetURL,
		emit, llmFinalResponse, llmFinalTTFT)
}

func llmTimingFixtures(artifacts map[string]string) []conformance.BudgetScenario {
	both := `,ext:{reqfleet:{llmResponse:'llm_response_duration',ttft:'llm_time_to_first_token'}}`
	artifacts["llm-both.js"] = llmTimingScript(both, true)
	artifacts["llm-none.js"] = llmTimingScript(both, false)
	artifacts["llm-default.js"] = llmTimingScript("", true)
	artifacts["llm-disabled.js"] = llmTimingScript(`,ext:{reqfleet:{llmResponse:'',ttft:''}}`, true)
	artifacts["llm-response.js"] = llmTimingScript(`,ext:{reqfleet:{llmResponse:'llm_response_duration'}}`, true)
	artifacts["llm-ttft.js"] = llmTimingScript(`,ext:{reqfleet:{ttft:'llm_time_to_first_token'}}`, true)
	for _, key := range []string{"llmResponse", "ttft"} {
		for index, selector := range []string{`"unknown"`, `"http_req_duration"`, `true`, `42`, `[]`, `{}`, `null`} {
			artifacts[fmt.Sprintf("llm-invalid-%s-%d.js", key, index)] = llmTimingScript(
				fmt.Sprintf(",ext:{reqfleet:{%s:%s}}", key, selector), true)
		}
	}
	fixture := conformance.Fixture{File: "llm-both.js", Settings: json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	return []conformance.BudgetScenario{
		{Name: "llm-overflow", LabelLimit: 1, Fixture: fixture, MinimumRequests: 5,
			RequestLabels: []string{conformance.TargetURL + "/llm/seed", "others"}, ByteLabels: []string{"k6"}},
		{Name: "llm-zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 5,
			RequestLabels: []string{"others"}, ByteLabels: []string{"others"}},
	}
}

func assertLLMTimingCSV(t *testing.T, runtime *conformance.Runtime, emit bool) {
	t.Helper()
	file, err := os.Open(filepath.Join(runtime.Root, "test-result", "k6.csv"))
	require.NoError(t, err)
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	require.NoError(t, err)
	columns := map[string]int{}
	for index, name := range header {
		columns[name] = index
	}
	for _, name := range []string{"metric_name", "metric_value", "url"} {
		require.Contains(t, columns, name)
	}
	values := map[string][]float64{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		name := row[columns["metric_name"]]
		if name != llmResponseSource && name != llmTTFTSource {
			continue
		}
		require.Empty(t, row[columns["url"]], "custom timings deliberately carry no HTTP URL")
		value, err := strconv.ParseFloat(row[columns["metric_value"]], 64)
		require.NoError(t, err)
		values[name] = append(values[name], value)
	}
	if !emit {
		require.Empty(t, values, "selected but unmeasured timings remain unavailable")
		return
	}
	for _, timing := range []struct {
		name   string
		bounds []float64
		final  float64
	}{
		{llmResponseSource, metricschema.LLMResponseLatencyBuckets(), llmFinalResponse},
		{llmTTFTSource, metricschema.RequestLatencyBuckets(), llmFinalTTFT},
	} {
		boundary := llmBoundaryValues(timing.bounds)
		samples := values[timing.name]
		require.Len(t, samples, len(boundary)+2, "one actual timing and one teardown timing in addition to boundary data")
		require.Equal(t, boundary, samples[:len(boundary)])
		require.GreaterOrEqual(t, samples[len(boundary)], 20.0, "actual timing must include the controlled 30ms target delay")
		require.Equal(t, timing.final, samples[len(samples)-1], "post-Stop teardown observations must be drained")
	}
}

func runLLMTimingScenarios(t *testing.T, runtime *conformance.Runtime) {
	t.Helper()
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	csvPath := filepath.Join(runtime.Root, "test-result", "k6.csv")
	check := func(t *testing.T, id, file string, selection llmTimingSelection, emit bool) {
		t.Helper()
		runtime.Start(t, id, conformance.Fixture{File: file, Settings: settings})
		runtime.Running(t, id)
		runtime.AwaitRequests(t, id, 5)
		runtime.Stop(t, id)
		runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
		observed, snapshot := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, metrics.DataReceivedMetric, selection)
		require.Equal(t, uint64(5), observed.count, "independent timings must not fabricate HTTP outcomes")
		require.Equal(t, map[string]float64{"200": 5}, observed.status)
		require.Positive(t, observed.bytes)
		require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes, "timings cannot create request byte series")
		assertLLMTimingCSV(t, runtime, emit)
		response := snapshot.Requests[metricschema.LLMResponseLabel]
		require.NotNil(t, response.Latency, "HTTP URL llm_resp remains an ordinary histogram")
		require.Equal(t, uint64(1), response.Latency.Count)
		require.Equal(t, map[string]uint64{"200": 1}, response.Statuses)
		if selection.response && emit {
			require.NotNil(t, response.LLMResponseLatency)
		} else {
			require.Nil(t, response.LLMResponseLatency)
		}
		if selection.ttft && emit {
			ttft := snapshot.Requests[metricschema.TTFTLabel]
			require.NotNil(t, ttft.Latency)
			require.Empty(t, ttft.Statuses, "TTFT samples are not HTTP outcomes")
			require.Nil(t, ttft.LLMResponseLatency)
		} else {
			require.NotContains(t, snapshot.Requests, metricschema.TTFTLabel)
		}
		_, retained := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, metrics.DataReceivedMetric, selection)
		require.Equal(t, snapshot, retained, "final timing histograms must persist without duplicate ingestion")
	}
	for index, scenario := range []struct {
		file      string
		selection llmTimingSelection
		emit      bool
	}{
		{"llm-both.js", llmTimingSelection{response: true, ttft: true}, true},
		{"llm-disabled.js", llmTimingSelection{}, true},
		{"llm-both.js", llmTimingSelection{response: true, ttft: true}, true},
		{"llm-default.js", llmTimingSelection{}, true},
		{"llm-response.js", llmTimingSelection{response: true}, true},
		{"llm-ttft.js", llmTimingSelection{ttft: true}, true},
		{"llm-none.js", llmTimingSelection{response: true, ttft: true}, false},
	} {
		t.Run("llm-reuse/"+strconv.Itoa(index), func(t *testing.T) {
			check(t, strconv.Itoa(1700+index), scenario.file, scenario.selection, scenario.emit)
		})
	}
	for keyIndex, key := range []string{"llmResponse", "ttft"} {
		for index := range 7 {
			t.Run(fmt.Sprintf("llm-invalid/%s/%d", key, index), func(t *testing.T) {
				id := strconv.Itoa(1750 + keyIndex*10 + index)
				runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("llm-invalid-%s-%d.js", key, index), Settings: settings})
				status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
				require.Equal(t, protocol.FailureStartup, status.FailurePhase)
				body := scrape(t, runtime.Client)
				require.NotContains(t, body, `run_id="1706"`, "failed preparation must clear prior measurements")
				require.NotContains(t, body, `run_id="`+id+`"`, "invalid selection must not launch or report samples")
			})
		}
	}
	t.Run("llm-recovery", func(t *testing.T) {
		check(t, "1780", "llm-both.js", llmTimingSelection{response: true, ttft: true}, true)
		check(t, "1781", "llm-default.js", llmTimingSelection{}, true)
	})
	runtime.AssertAlive(t)
}
