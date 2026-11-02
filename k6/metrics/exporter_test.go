package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const csvHeader = "metric_name,metric_value,status\n"

func fixture(t *testing.T, budget int) (*Exporter, agent.RunContext) {
	t.Helper()
	e, err := New(budget)
	require.NoError(t, err)
	e.resources = func() (resourceSample, error) {
		return resourceSample{cpuNanos: 1_000_000_000, memoryBytes: 4096}, nil
	}
	clock := time.Unix(100, 0)
	e.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	run := agent.RunContext{RunID: "run-one", CollectionID: "collection", PlanID: "plan", AgentID: "agent-2", Zone: "zone-a", ResultFile: filepath.Join(t.TempDir(), "k6.csv")}
	e.Reset(run)
	return e, run
}

func putCSV(t *testing.T, run agent.RunContext, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(run.ResultFile, []byte(text), 0600))
}

func appendCSV(t *testing.T, run agent.RunContext, text string) {
	t.Helper()
	file, err := os.OpenFile(run.ResultFile, os.O_WRONLY|os.O_APPEND, 0600)
	require.NoError(t, err)
	_, err = file.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func scrape(e *Exporter) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder
}

func families(t *testing.T, e *Exporter) map[string]*dto.MetricFamily {
	t.Helper()
	recorder := scrape(e)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	parser := expfmt.NewTextParser(model.LegacyValidation)
	result, err := parser.TextToMetricFamilies(strings.NewReader(recorder.Body.String()))
	require.NoError(t, err)
	return result
}

func single(t *testing.T, all map[string]*dto.MetricFamily, name string) *dto.Metric {
	t.Helper()
	require.Contains(t, all, name)
	require.Len(t, all[name].Metric, 1)
	return all[name].Metric[0]
}

func metricLabels(metric *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, pair := range metric.Label {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
}

func TestExactIndependentSampleMapping(t *testing.T) {
	e, run := fixture(t, 10)
	// Header order is deliberately unrelated to k6's default order. Quoted
	// commas, escaped quotes and multiline unrelated tags must stay opaque.
	putCSV(t, run, "extra,status,metric_value,metric_name\n"+
		"\"name, with \"\"quotes\"\"\",200,12.5,http_req_duration\n"+
		"\"multi\nline\",500,30,http_req_duration\n"+
		"x,,3,vus\nx,,120,data_received\nx,,80,data_received\n"+
		"x,,2,http_reqs\nx,,999,data_sent\nx,,not-a-number,custom_metric\n")
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationReady, state)
	all := families(t, e)
	hist := single(t, all, metricschema.RequestLatency)
	assert.Equal(t, uint64(2), hist.GetHistogram().GetSampleCount())
	assert.Equal(t, 42.5, hist.GetHistogram().GetSampleSum())
	assert.Equal(t, map[string]string{"collection_id": "collection", "plan_id": "plan", "run_id": "run-one", "engine_no": "agent-2", "zone": "zone-a", "label": "k6"}, metricLabels(hist))
	bounds := []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, math.Inf(1)}
	require.Len(t, hist.GetHistogram().Bucket, len(bounds))
	for index, bound := range bounds {
		assert.Equal(t, bound, hist.GetHistogram().Bucket[index].GetUpperBound())
	}
	assert.Equal(t, uint64(1), hist.GetHistogram().Bucket[4].GetCumulativeCount())
	assert.Equal(t, uint64(2), hist.GetHistogram().Bucket[5].GetCumulativeCount())
	statuses := all[metricschema.StatusCounter].Metric
	require.Len(t, statuses, 2)
	seen := map[string]float64{}
	for _, metric := range statuses {
		seen[metricLabels(metric)["status"]] = metric.GetCounter().GetValue()
		assert.Equal(t, "k6", metricLabels(metric)["label"])
	}
	assert.Equal(t, map[string]float64{"200": 1, "500": 1}, seen)
	assert.Equal(t, float64(3), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	assert.Equal(t, float64(200), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
	assert.NotContains(t, all, metricschema.CPUGauge, "a single CPU sample has no rate")
	assert.Equal(t, float64(4096), single(t, all, metricschema.MemoryGauge).GetGauge().GetValue())
	for range 3 {
		next := families(t, e)
		assert.Equal(t, hist, single(t, next, metricschema.RequestLatency))
		assert.Equal(t, all[metricschema.StatusCounter], next[metricschema.StatusCounter])
		assert.Equal(t, all[metricschema.EgressBytesCounter], next[metricschema.EgressBytesCounter])
		assert.Equal(t, float64(3), single(t, next, metricschema.ThreadsGauge).GetGauge().GetValue())
	}
	appendCSV(t, run, "x,,0,vus\n")
	next := families(t, e)
	assert.Equal(t, float64(0), single(t, next, metricschema.ThreadsGauge).GetGauge().GetValue())
	assert.Equal(t, hist, single(t, next, metricschema.RequestLatency))
	assert.Equal(t, float64(0), single(t, families(t, e), metricschema.ThreadsGauge).GetGauge().GetValue())
}

func TestPartialLogicalRecordsAreRetriedExactlyOnce(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("ending-%q", ending), func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, "extra,status,metric_value,metric_na")
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationPending, state)
			assert.Zero(t, e.offset)
			appendCSV(t, run, "me"+ending+"\"line one"+ending)
			state, err = e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationPending, state)
			headerOffset := e.offset
			appendCSV(t, run, "line two, with \"\"quote\"\"")
			_, err = e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, headerOffset, e.offset)
			appendCSV(t, run, "\",200,12.5,http_req_duration")
			_, err = e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, headerOffset, e.offset, "unterminated last line must not be committed")
			appendCSV(t, run, ending)
			all := families(t, e)
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
			appendCSV(t, run, "x,,1")
			all = families(t, e)
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
			appendCSV(t, run, "20,data_received"+ending)
			all = families(t, e)
			assert.Equal(t, float64(120), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
		})
	}
}

func TestPendingUnsupportedAndNonRequestSamples(t *testing.T) {
	e, run := fixture(t, 10)
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationPending, state)
	assert.Equal(t, http.StatusServiceUnavailable, scrape(e).Code)
	putCSV(t, run, csvHeader+"http_reqs,12,\ndata_sent,999,\ncustom,nonsense,not-http\n")
	state, err = e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationPending, state)
	appendCSV(t, run, "vus,0,\n")
	all := families(t, e)
	assert.NotContains(t, all, metricschema.RequestLatency)
	assert.NotContains(t, all, metricschema.StatusCounter)
	assert.NotContains(t, all, metricschema.EgressBytesCounter)
	assert.Equal(t, float64(0), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	appendCSV(t, run, "data_received,42,\n")
	all = families(t, e)
	assert.NotContains(t, all, metricschema.RequestLatency)
	assert.NotContains(t, all, metricschema.StatusCounter)
	assert.Equal(t, float64(42), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
}

func TestInvalidCompleteCSVIsStickyUntilReset(t *testing.T) {
	cases := map[string]string{
		"missing-name":        "metric_value,status\n1,200\n",
		"missing-value":       "metric_name,status\nhttp_req_duration,200\n",
		"missing-status":      "metric_name,metric_value\nhttp_req_duration,1\n",
		"duplicate-column":    "metric_name,metric_value,status,status\n",
		"short-row":           csvHeader + "vus,3\n",
		"long-row":            csvHeader + "vus,3,,extra\n",
		"bare-quote":          csvHeader + "vu\"s,3,\n",
		"extraneous-quote":    csvHeader + "\"vus\"x,3,\n",
		"empty-value":         csvHeader + "vus,,\n",
		"invalid-number":      csvHeader + "data_received,no,\n",
		"negative-bytes":      csvHeader + "data_received,-1,\n",
		"negative-latency":    csvHeader + "http_req_duration,-1,200\n",
		"negative-vus":        csvHeader + "vus,-1,\n",
		"fractional-vus":      csvHeader + "vus,1.5,\n",
		"nan":                 csvHeader + "http_req_duration,NaN,200\n",
		"infinity":            csvHeader + "data_received,+Inf,\n",
		"overflow":            csvHeader + "vus,1e999,\n",
		"missing-http-status": csvHeader + "http_req_duration,1,\n",
		"status-text":         csvHeader + "http_req_duration,1,OK\n",
		"status-low":          csvHeader + "http_req_duration,1,99\n",
		"status-high":         csvHeader + "http_req_duration,1,600\n",
		"status-fraction":     csvHeader + "http_req_duration,1,200.0\n",
		"status-sign":         csvHeader + "http_req_duration,1,+200\n",
		"status-leading-zero": csvHeader + "http_req_duration,1,0200\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, text)
			_, err := e.Observe(context.Background(), run)
			require.Error(t, err)
			first := err.Error()
			appendCSV(t, run, "vus,3,\n")
			_, err = e.Observe(context.Background(), run)
			require.EqualError(t, err, first)
			response := scrape(e)
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			assert.Contains(t, response.Body.String(), first)
			putCSV(t, run, csvHeader+"http_req_duration,1,200\n")
			e.Reset(run)
			assert.Equal(t, uint64(1), single(t, families(t, e), metricschema.RequestLatency).GetHistogram().GetSampleCount())
		})
	}
}

func TestFileLossTruncationAndReplacementFail(t *testing.T) {
	for _, action := range []string{"truncate-committed", "truncate-partial", "remove", "replace", "directory"} {
		t.Run(action, func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, csvHeader+"vus,3,\nhttp_req_duration,12")
			_, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			switch action {
			case "truncate-committed":
				require.NoError(t, os.Truncate(run.ResultFile, 0))
			case "truncate-partial":
				require.NoError(t, os.Truncate(run.ResultFile, e.offset+1))
			case "remove":
				require.NoError(t, os.Remove(run.ResultFile))
			case "replace":
				other := filepath.Join(filepath.Dir(run.ResultFile), "replacement.csv")
				require.NoError(t, os.WriteFile(other, []byte(csvHeader+strings.Repeat("vus,3,\n", 20)), 0600))
				require.NoError(t, os.Rename(other, run.ResultFile))
			case "directory":
				require.NoError(t, os.Remove(run.ResultFile))
				require.NoError(t, os.Mkdir(run.ResultFile, 0700))
			}
			_, err = e.Observe(context.Background(), run)
			require.Error(t, err)
			assert.Equal(t, http.StatusInternalServerError, scrape(e).Code)
		})
	}
}

func TestResetClearsIdentityCountersCursorAndCPU(t *testing.T) {
	e, run := fixture(t, 0)
	putCSV(t, run, csvHeader+"http_req_duration,12.5,0\ndata_received,120,\nvus,3,\n")
	old := families(t, e)
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.RequestLatency))["label"])
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.EgressBytesCounter))["label"])
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.StatusCounter))["label"])
	assert.Equal(t, "0", metricLabels(single(t, old, metricschema.StatusCounter))["status"])
	assert.Contains(t, families(t, e), metricschema.CPUGauge)
	next := run
	next.RunID, next.CollectionID, next.PlanID, next.AgentID, next.Zone = "new-run", "new-collection", "new-plan", "new-agent", "new-zone"
	putCSV(t, next, csvHeader+"http_req_duration,30,500\n")
	e.Reset(next)
	_, err := e.Observe(context.Background(), run)
	require.ErrorContains(t, err, "active run")
	all := families(t, e)
	hist := single(t, all, metricschema.RequestLatency)
	assert.Equal(t, uint64(1), hist.GetHistogram().GetSampleCount())
	assert.Equal(t, float64(30), hist.GetHistogram().GetSampleSum())
	assert.Equal(t, map[string]string{"collection_id": "new-collection", "plan_id": "new-plan", "run_id": "new-run", "engine_no": "new-agent", "zone": "new-zone", "label": "others"}, metricLabels(hist))
	assert.NotContains(t, all, metricschema.CPUGauge)
	assert.NotContains(t, all, metricschema.EgressBytesCounter)
	assert.NotContains(t, all, metricschema.ThreadsGauge)
	assert.Equal(t, "500", metricLabels(single(t, all, metricschema.StatusCounter))["status"])
}

func TestLabelBudgetValidationAndUnpreparedExporter(t *testing.T) {
	_, err := New(-1)
	require.ErrorContains(t, err, "label_limit")
	e, err := New(10)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, scrape(e).Code)
	_, err = e.Observe(context.Background(), agent.RunContext{})
	require.Error(t, err)
}

func TestCanceledObservationDoesNotPoisonRun(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, csvHeader+"vus,3,\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Observe(ctx, run)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, e.offset)
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationReady, state)
}

func TestResourcesUseRealCPUIntervalsAndFailClearly(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, csvHeader+"vus,0,\n")
	cpu := uint64(4_000_000_000)
	clock := time.Unix(100, 0)
	e.resources = func() (resourceSample, error) { return resourceSample{cpuNanos: cpu, memoryBytes: 8192}, nil }
	e.now = func() time.Time { return clock }
	assert.NotContains(t, families(t, e), metricschema.CPUGauge)
	cpu += 1_000_000_000
	clock = clock.Add(2 * time.Second)
	all := families(t, e)
	metric := single(t, all, metricschema.CPUGauge)
	assert.Equal(t, 0.5, metric.GetGauge().GetValue())
	assert.Equal(t, map[string]string{"collection_id": "collection", "plan_id": "plan", "run_id": "run-one", "engine_no": "agent-2", "zone": "zone-a"}, metricLabels(metric))
	assert.Equal(t, float64(8192), single(t, all, metricschema.MemoryGauge).GetGauge().GetValue())
	e.resources = func() (resourceSample, error) { return resourceSample{}, errors.New("memory unavailable") }
	response := scrape(e)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Contains(t, response.Body.String(), "container resources: memory unavailable")
	assert.NotContains(t, response.Body.String(), metricschema.RequestLatency)
	e.resources = func() (resourceSample, error) { return resourceSample{cpuNanos: 1, memoryBytes: 1}, nil }
	clock = clock.Add(time.Second)
	response = scrape(e)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Contains(t, response.Body.String(), "moved backwards")
	// A reset starts a fresh baseline even after a resource error.
	e.Reset(run)
	assert.NotContains(t, families(t, e), metricschema.CPUGauge)
	response = scrape(e)
	assert.Equal(t, http.StatusInternalServerError, response.Code, "zero elapsed time must not fabricate a rate")
}

func TestConcurrentObservationAndScrapeDoNotDoubleCount(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, csvHeader+strings.Repeat("http_req_duration,12.5,200\n", 100))
	var group sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for index := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			if index%2 == 0 {
				_, err := e.Observe(context.Background(), run)
				errorsSeen <- err
			} else {
				response := scrape(e)
				if response.Code != http.StatusOK {
					errorsSeen <- fmt.Errorf("scrape: %s", response.Body.String())
				} else {
					errorsSeen <- nil
				}
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		require.NoError(t, err)
	}
	hist := single(t, families(t, e), metricschema.RequestLatency).GetHistogram()
	assert.Equal(t, uint64(100), hist.GetSampleCount())
	assert.Equal(t, float64(1250), hist.GetSampleSum())
}
