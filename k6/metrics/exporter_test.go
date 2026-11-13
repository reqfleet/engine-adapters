package metrics

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/conformance"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const csvHeader = "metric_name,metric_value,status,url\n"

type exporterFixture struct {
	*Exporter
	registry *agent.MetricsRegistry
}

func fixture(t *testing.T, budget int) (*exporterFixture, agent.RunContext) {
	t.Helper()
	registry := agent.NewMetricsRegistry()
	exporter, err := New(budget, registry)
	require.NoError(t, err)
	e := &exporterFixture{Exporter: exporter, registry: registry}
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

func scrape(e *exporterFixture) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(e.registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder
}

func collectAndGather(t *testing.T, e *exporterFixture) map[string]*dto.MetricFamily {
	t.Helper()
	require.NoError(t, e.Collect(context.Background(), agent.RunContext{RunID: e.runID}))
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

type requestMetrics struct {
	count    uint64
	sum      float64
	statuses map[string]float64
}

func assertRequestMetrics(t *testing.T, all map[string]*dto.MetricFamily, want map[string]requestMetrics) {
	t.Helper()
	require.Contains(t, all, metricschema.RequestLatency)
	require.Contains(t, all, metricschema.StatusCounter)
	require.Len(t, all[metricschema.RequestLatency].Metric, len(want))
	got := make(map[string]requestMetrics)
	for _, metric := range all[metricschema.RequestLatency].Metric {
		got[metricLabels(metric)["label"]] = requestMetrics{
			count:    metric.GetHistogram().GetSampleCount(),
			sum:      metric.GetHistogram().GetSampleSum(),
			statuses: make(map[string]float64),
		}
	}
	statusCount := 0
	for _, expected := range want {
		statusCount += len(expected.statuses)
	}
	require.Len(t, all[metricschema.StatusCounter].Metric, statusCount)
	for _, metric := range all[metricschema.StatusCounter].Metric {
		labels := metricLabels(metric)
		require.Contains(t, got, labels["label"])
		got[labels["label"]].statuses[labels["status"]] = metric.GetCounter().GetValue()
	}
	assert.Equal(t, want, got)
}

func TestExactIndependentSampleMapping(t *testing.T) {
	e, run := fixture(t, 10)
	// Header order is deliberately unrelated to k6's default order. Quoted
	// commas, escaped quotes and multiline unrelated tags must stay opaque.
	putCSV(t, run, "extra,status,url,metric_value,name,metric_name\n"+
		"\"name, with \"\"quotes\"\"\",200,https://example.test/items?q=one,12.5,shared-name,http_req_duration\n"+
		"\"multi\nline\",500,https://example.test/items?q=two,30,shared-name,http_req_duration\n"+
		"x,200,https://example.test/items?q=one,7.5,different-name,http_req_duration\n"+
		"x,500,https://example.test/items?q=one,5,another-name,http_req_duration\n"+
		"x,,,3,,vus\nx,,,120,,data_received\nx,,,80,,data_received\n"+
		"x,,,2,,http_reqs\nx,,,999,,data_sent\nx,,,not-a-number,,custom_metric\n")
	require.NoError(t, e.Collect(context.Background(), run))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationReady, state)
	all := collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"https://example.test/items?q=one": {count: 3, sum: 25, statuses: map[string]float64{"200": 2, "500": 1}},
		"https://example.test/items?q=two": {count: 1, sum: 30, statuses: map[string]float64{"500": 1}},
	})
	bounds := []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, math.Inf(1)}
	for _, hist := range all[metricschema.RequestLatency].Metric {
		label := metricLabels(hist)["label"]
		assert.Equal(t, map[string]string{"collection_id": "collection", "plan_id": "plan", "run_id": "run-one", "engine_no": "agent-2", "zone": "zone-a", "label": label}, metricLabels(hist))
		require.Len(t, hist.GetHistogram().Bucket, len(bounds))
		for index, bound := range bounds {
			assert.Equal(t, bound, hist.GetHistogram().Bucket[index].GetUpperBound())
		}
		if label == "https://example.test/items?q=one" {
			assert.Equal(t, uint64(3), hist.GetHistogram().Bucket[4].GetCumulativeCount())
			assert.Equal(t, uint64(3), hist.GetHistogram().Bucket[5].GetCumulativeCount())
		}
	}
	assert.Equal(t, float64(3), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	assert.Equal(t, float64(200), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
	assert.Equal(t, "k6", metricLabels(single(t, all, metricschema.EgressBytesCounter))["label"])
	for range 3 {
		next := collectAndGather(t, e)
		assert.Equal(t, all[metricschema.RequestLatency], next[metricschema.RequestLatency])
		assert.Equal(t, all[metricschema.StatusCounter], next[metricschema.StatusCounter])
		assert.Equal(t, all[metricschema.EgressBytesCounter], next[metricschema.EgressBytesCounter])
		assert.Equal(t, float64(3), single(t, next, metricschema.ThreadsGauge).GetGauge().GetValue())
	}
	appendCSV(t, run, "x,,,0,,vus\n")
	next := collectAndGather(t, e)
	assert.Equal(t, float64(0), single(t, next, metricschema.ThreadsGauge).GetGauge().GetValue())
	assert.Equal(t, all[metricschema.RequestLatency], next[metricschema.RequestLatency])
	assert.Equal(t, float64(0), single(t, collectAndGather(t, e), metricschema.ThreadsGauge).GetGauge().GetValue())
}

func TestTransportFailuresKeepErrorCodesAndRequests(t *testing.T) {
	for _, budget := range []int{1, 0} {
		t.Run(fmt.Sprintf("budget-%d", budget), func(t *testing.T) {
			e, run := fixture(t, budget)
			// All failed attempts have zero request duration. They must still
			// make the exporter ready and count once, independently of http_reqs.
			putCSV(t, run, "error_code,url,status,metric_name,metric_value\n"+
				"1101,http://missing.invalid/,0,http_req_duration,0\n"+
				"1101,http://missing.invalid/,0,http_reqs,1\n"+
				"1101,http://missing.invalid/,0,http_req_duration,0\n"+
				"1212,http://127.0.0.1:1/,0,http_req_duration,0\n"+
				"1212,http://127.0.0.1:1/,0,http_reqs,1\n"+
				"1050,http://127.0.0.1:1/,0,http_req_duration,1000\n"+
				"1211,http://127.0.0.1:1/,0,http_req_duration,0\n"+
				"1500,http://127.0.0.1:1/,500,http_req_duration,2\n"+
				",,,data_received,0\n"+
				",,,vus,1\n")
			require.NoError(t, e.Collect(context.Background(), run))
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			require.Equal(t, protocol.ObservationReady, state)
			want := map[string]requestMetrics{
				"http://missing.invalid/": {count: 2, sum: 0, statuses: map[string]float64{"DNS host not found (k6 1101)": 2}},
				"others": {count: 4, sum: 1002, statuses: map[string]float64{
					"TCP connection refused (k6 1212)": 1, "HTTP request timeout (k6 1050)": 1, "TCP connection timeout (k6 1211)": 1, "500": 1,
				}},
			}
			if budget == 0 {
				want = map[string]requestMetrics{
					"others": {count: 6, sum: 1002, statuses: map[string]float64{
						"DNS host not found (k6 1101)": 2, "TCP connection refused (k6 1212)": 1, "HTTP request timeout (k6 1050)": 1, "TCP connection timeout (k6 1211)": 1, "500": 1,
					}},
				}
			}
			all := collectAndGather(t, e)
			assertRequestMetrics(t, all, want)
			assert.Zero(t, single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
			assertRequestMetrics(t, collectAndGather(t, e), want)
		})
	}
}

func TestTransportErrorMetadataResetsWithRun(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, "metric_name,metric_value,error_code,status,url\n"+
		"http_req_duration,0,1100,0,http://missing.invalid/\n"+
		"http_req_duration,0,,0,http://missing.invalid/\n"+
		"http_req_duration,0,1999,0,http://missing.invalid/\n")
	assertRequestMetrics(t, collectAndGather(t, e), map[string]requestMetrics{
		"http://missing.invalid/": {count: 3, sum: 0, statuses: map[string]float64{
			"DNS lookup error (k6 1100)": 1, "Transport error (cause unavailable)": 1, "Unknown k6 error (k6 1999)": 1,
		}},
	})
	// Missing error metadata cannot identify a cause, but must not suppress
	// the failed request or reuse the previous run's column position.
	putCSV(t, run, csvHeader+"http_req_duration,0,0,http://127.0.0.1:1/\n")
	e.Reset(run)
	assertRequestMetrics(t, collectAndGather(t, e), map[string]requestMetrics{
		"http://127.0.0.1:1/": {count: 1, sum: 0, statuses: map[string]float64{"Transport error (cause unavailable)": 1}},
	})
}

func TestURLBudgetKeepsAdmittedURLsAndResetsSlots(t *testing.T) {
	e, run := fixture(t, 2)
	putCSV(t, run, csvHeader+
		"data_received,10,,https://example.test/bytes\n"+
		"vus,2,,https://example.test/vus\n"+
		"http_req_duration,2,201,others\n"+
		"http_req_duration,10,200,https://example.test/a\n"+
		"http_req_duration,20,201,https://example.test/b\n"+
		"http_req_duration,30,500,https://example.test/c\n"+
		"http_req_duration,40,0,https://example.test/d\n")
	all := collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"https://example.test/a": {count: 1, sum: 10, statuses: map[string]float64{"200": 1}},
		"https://example.test/b": {count: 1, sum: 20, statuses: map[string]float64{"201": 1}},
		"others":                 {count: 3, sum: 72, statuses: map[string]float64{"201": 1, "500": 1, "Transport error (cause unavailable)": 1}},
	})
	assert.Equal(t, "k6", metricLabels(single(t, all, metricschema.EgressBytesCounter))["label"])
	assert.Equal(t, float64(10), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
	appendCSV(t, run,
		"http_req_duration,5,500,https://example.test/a\n"+
			"http_req_duration,7,201,https://example.test/b\n"+
			"http_req_duration,3,200,https://example.test/c\n"+
			"data_received,15,,\n")
	all = collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"https://example.test/a": {count: 2, sum: 15, statuses: map[string]float64{"200": 1, "500": 1}},
		"https://example.test/b": {count: 2, sum: 27, statuses: map[string]float64{"201": 2}},
		"others":                 {count: 4, sum: 75, statuses: map[string]float64{"201": 1, "500": 1, "Transport error (cause unavailable)": 1, "200": 1}},
	})
	assert.Equal(t, "k6", metricLabels(single(t, all, metricschema.EgressBytesCounter))["label"])
	assert.Equal(t, float64(25), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())

	// Reset must release slots even when the run identity is unchanged.
	putCSV(t, run, csvHeader+
		"http_req_duration,4,200,https://example.test/c\n"+
		"http_req_duration,6,201,https://example.test/d\n"+
		"http_req_duration,8,500,https://example.test/a\n")
	e.Reset(run)
	all = collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"https://example.test/c": {count: 1, sum: 4, statuses: map[string]float64{"200": 1}},
		"https://example.test/d": {count: 1, sum: 6, statuses: map[string]float64{"201": 1}},
		"others":                 {count: 1, sum: 8, statuses: map[string]float64{"500": 1}},
	})
	assert.NotContains(t, all, metricschema.EgressBytesCounter)
}

func TestZeroURLBudgetAggregatesRequestsAndBytesSeparately(t *testing.T) {
	e, run := fixture(t, 0)
	putCSV(t, run, csvHeader+
		"data_received,12,,\n"+
		"http_req_duration,10,200,https://example.test/a\n"+
		"http_req_duration,20,200,https://example.test/b\n"+
		"http_req_duration,30,500,https://example.test/c\n"+
		"http_req_duration,40,200,https://example.test/a\n"+
		"data_received,8,,\n")
	all := collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"others": {count: 4, sum: 100, statuses: map[string]float64{"200": 3, "500": 1}},
	})
	assert.Equal(t, "others", metricLabels(single(t, all, metricschema.EgressBytesCounter))["label"])
	assert.Equal(t, float64(20), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
	appendCSV(t, run, "http_req_duration,5,200,\n")
	err := e.Collect(context.Background(), run)
	require.Error(t, err, "a zero budget does not make request URLs optional")
}

func TestURLByteLimitRejectsOversizedSamplesBeforeAdmission(t *testing.T) {
	for _, encoding := range []struct {
		name, character string
	}{
		{"ASCII", "a"},
		{"UTF8", "界"},
	} {
		for _, budget := range []int{1, 0} {
			t.Run(fmt.Sprintf("%s/budget=%d", encoding.name, budget), func(t *testing.T) {
				e, run := fixture(t, budget)
				for _, revision := range []string{"first", "second"} {
					// A different boundary URL must claim the slot after reset.
					prefix := "https://example.test/" + revision + "/"
					padding := 1023 - len(prefix)
					valid := prefix + strings.Repeat(encoding.character, padding/len(encoding.character)) +
						strings.Repeat("a", padding%len(encoding.character))
					atLimit, overLimit := valid+"b", valid+"bc"
					// Oversized values must not merge into their valid prefix.
					run.RunID = revision
					e.Reset(run)
					putCSV(t, run, csvHeader+fmt.Sprintf(
						"http_req_duration,1,200,%s\n"+
							"http_req_duration,2,500,%s\n",
						atLimit, overLimit))
					require.NoError(t, e.Collect(context.Background(), run))
					state, err := e.Observe(context.Background(), run)
					require.NoError(t, err)
					require.Equal(t, protocol.ObservationPending, state)
					appendCSV(t, run, fmt.Sprintf(
						"data_received,10,,\n"+
							"vus,2,,\n"+
							"http_req_duration,3,201,%s\n"+
							"http_req_duration,4,200,https://example.test/overflow\n"+
							"http_req_duration,5,500,%s\n"+
							"http_req_duration,6,200,%s\n"+
							"http_req_duration,7,500,%s\n"+
							"http_req_duration,8,200,https://example.test/overflow\n"+
							"data_received,5,,\n",
						valid, atLimit, overLimit, valid))
					require.NoError(t, e.Collect(context.Background(), run))
					state, err = e.Observe(context.Background(), run)
					require.NoError(t, err)
					require.Equal(t, protocol.ObservationReady, state)
					requests := map[string]conformance.RequestExpectation{
						valid:    {Statuses: map[string]uint64{"201": 1, "500": 1}, LatenciesMS: []float64{3, 7}},
						"others": {Statuses: map[string]uint64{"200": 2}, LatenciesMS: []float64{4, 8}},
					}
					byteLabel := "k6"
					if budget == 0 {
						requests = map[string]conformance.RequestExpectation{
							"others": {Statuses: map[string]uint64{"200": 2, "201": 1, "500": 1}, LatenciesMS: []float64{3, 4, 7, 8}},
						}
						byteLabel = "others"
					}
					concurrency := float64(2)
					expected := conformance.MetricsExpectation{
						Identity: conformance.MetricIdentity{
							CollectionID: run.CollectionID, PlanID: run.PlanID, RunID: run.RunID,
							EngineNo: run.AgentID, Zone: run.Zone,
						},
						Requests: requests, Bytes: map[string]float64{byteLabel: 15}, Concurrency: &concurrency,
						Resources: conformance.ResourceExpectation{CPU: conformance.MeasurementAbsent, Memory: conformance.MeasurementAbsent},
					}
					for range 2 {
						response := scrape(e)
						require.Equal(t, http.StatusOK, response.Code, response.Body.String())
						conformance.AssertMetrics(t, response.Body.String(), expected)
					}
				}
			})
		}
	}
}

func TestURLByteLimitPreservesUTF8GroupingNames(t *testing.T) {
	e, run := fixture(t, 2)
	// Reject 1024- and 1025-byte names rather than trimming their final rune.
	first := strings.Repeat("界", 340) + "a"
	second := first + "a"
	putCSV(t, run, csvHeader+fmt.Sprintf(
		"http_req_duration,1,200,%s界\n"+
			"http_req_duration,2,500,%s界\n"+
			"http_req_duration,3,201,%s\n"+
			"http_req_duration,4,200,%s\n",
		first, second, first, second))
	require.NoError(t, e.Collect(context.Background(), run))
	response := scrape(e)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	conformance.AssertMetrics(t, response.Body.String(), conformance.MetricsExpectation{
		Identity: conformance.MetricIdentity{
			CollectionID: run.CollectionID, PlanID: run.PlanID, RunID: run.RunID,
			EngineNo: run.AgentID, Zone: run.Zone,
		},
		Requests: map[string]conformance.RequestExpectation{
			first:  {Statuses: map[string]uint64{"201": 1}, LatenciesMS: []float64{3}},
			second: {Statuses: map[string]uint64{"200": 1}, LatenciesMS: []float64{4}},
		},
		Resources: conformance.ResourceExpectation{CPU: conformance.MeasurementAbsent, Memory: conformance.MeasurementAbsent},
	})
}

func TestPartialLogicalRecordsAreRetriedExactlyOnce(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("ending-%q", ending), func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, "url,extra,status,metric_value,metric_na")
			require.NoError(t, e.Collect(context.Background(), run))
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationPending, state)
			assert.Zero(t, e.offset)
			appendCSV(t, run, "me"+ending+"https://example.test/partial,\"line one"+ending)
			require.NoError(t, e.Collect(context.Background(), run))
			state, err = e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationPending, state)
			headerOffset := e.offset
			appendCSV(t, run, "line two, with \"\"quote\"\"")
			err = e.Collect(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, headerOffset, e.offset)
			appendCSV(t, run, "\",200,12.5,http_req_duration")
			err = e.Collect(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, headerOffset, e.offset, "unterminated last line must not be committed")
			appendCSV(t, run, ending)
			all := collectAndGather(t, e)
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
			appendCSV(t, run, ",x,,1")
			all = collectAndGather(t, e)
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
			appendCSV(t, run, "20,data_received"+ending)
			all = collectAndGather(t, e)
			assert.Equal(t, float64(120), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
			assert.Equal(t, uint64(1), single(t, all, metricschema.RequestLatency).GetHistogram().GetSampleCount())
		})
	}
}

func TestPendingUnsupportedAndNonRequestSamples(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.Collect(context.Background(), run))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationPending, state)
	putCSV(t, run, csvHeader+"http_reqs,12,,\ndata_sent,999,,\ncustom,nonsense,not-http,\n")
	require.NoError(t, e.Collect(context.Background(), run))
	state, err = e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationPending, state)
	appendCSV(t, run, "vus,0,,\n")
	all := collectAndGather(t, e)
	assert.NotContains(t, all, metricschema.RequestLatency)
	assert.NotContains(t, all, metricschema.StatusCounter)
	assert.NotContains(t, all, metricschema.EgressBytesCounter)
	assert.Equal(t, float64(0), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	appendCSV(t, run, "data_received,42,,\n")
	all = collectAndGather(t, e)
	assert.NotContains(t, all, metricschema.RequestLatency)
	assert.NotContains(t, all, metricschema.StatusCounter)
	assert.Equal(t, float64(42), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
}

func TestInvalidCompleteCSVIsStickyUntilReset(t *testing.T) {
	cases := map[string]string{
		"missing-name":         "metric_value,status,url\n1,200,https://example.test/\n",
		"missing-value":        "metric_name,status,url\nhttp_req_duration,200,https://example.test/\n",
		"missing-status":       "metric_name,metric_value,url\nhttp_req_duration,1,https://example.test/\n",
		"missing-url-column":   "metric_name,metric_value,status\n",
		"duplicate-column":     "metric_name,metric_value,status,status,url\n",
		"duplicate-url":        "metric_name,metric_value,status,url,url\n",
		"short-row":            csvHeader + "vus,3\n",
		"long-row":             csvHeader + "vus,3,,,extra\n",
		"bare-quote":           csvHeader + "vu\"s,3,,\n",
		"extraneous-quote":     csvHeader + "\"vus\"x,3,,\n",
		"empty-value":          csvHeader + "vus,,,\n",
		"invalid-number":       csvHeader + "data_received,no,,\n",
		"negative-bytes":       csvHeader + "data_received,-1,,\n",
		"negative-latency":     csvHeader + "http_req_duration,-1,200,https://example.test/\n",
		"negative-vus":         csvHeader + "vus,-1,,\n",
		"fractional-vus":       csvHeader + "vus,1.5,,\n",
		"nan":                  csvHeader + "http_req_duration,NaN,200,https://example.test/\n",
		"infinity":             csvHeader + "data_received,+Inf,,\n",
		"overflow":             csvHeader + "vus,1e999,,\n",
		"missing-http-status":  csvHeader + "http_req_duration,1,,https://example.test/\n",
		"missing-url-value":    csvHeader + "http_req_duration,1,200,\n",
		"quoted-empty-url":     csvHeader + "http_req_duration,1,200,\"\"\n",
		"missing-url-field":    csvHeader + "http_req_duration,1,200\n",
		"status-text":          csvHeader + "http_req_duration,1,OK,https://example.test/\n",
		"status-low":           csvHeader + "http_req_duration,1,99,https://example.test/\n",
		"status-high":          csvHeader + "http_req_duration,1,600,https://example.test/\n",
		"status-fraction":      csvHeader + "http_req_duration,1,200.0,https://example.test/\n",
		"status-sign":          csvHeader + "http_req_duration,1,+200,https://example.test/\n",
		"status-leading-zero":  csvHeader + "http_req_duration,1,0200,https://example.test/\n",
		"duplicate-error-code": "metric_name,metric_value,status,url,error_code,error_code\n",
		"error-code-text":      "metric_name,metric_value,status,url,error_code\nhttp_req_duration,0,0,http://example.test/,refused\n",
		"error-code-low":       "metric_name,metric_value,status,url,error_code\nhttp_req_duration,0,0,http://example.test/,999\n",
		"error-code-sign":      "metric_name,metric_value,status,url,error_code\nhttp_req_duration,0,0,http://example.test/,+1212\n",
		"error-code-zero":      "metric_name,metric_value,status,url,error_code\nhttp_req_duration,0,0,http://example.test/,01212\n",
		"error-code-overflow":  "metric_name,metric_value,status,url,error_code\nhttp_req_duration,0,0,http://example.test/,4294967296\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, text)
			err := e.Collect(context.Background(), run)
			require.Error(t, err)
			first := err
			appendCSV(t, run, "vus,3,,\n")
			err = e.Collect(context.Background(), run)
			require.ErrorIs(t, err, first)
			_, err = e.Observe(context.Background(), run)
			require.ErrorIs(t, err, first)
			putCSV(t, run, csvHeader+"http_req_duration,1,200,https://example.test/\n")
			e.Reset(run)
			assert.Equal(t, uint64(1), single(t, collectAndGather(t, e), metricschema.RequestLatency).GetHistogram().GetSampleCount())
		})
	}
}

func TestFileLossTruncationAndReplacementFail(t *testing.T) {
	for _, action := range []string{"truncate-committed", "truncate-partial", "remove", "replace", "directory"} {
		t.Run(action, func(t *testing.T) {
			e, run := fixture(t, 10)
			putCSV(t, run, csvHeader+"vus,3,,\nhttp_req_duration,12")
			err := e.Collect(context.Background(), run)
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
				require.NoError(t, os.WriteFile(other, []byte(csvHeader+strings.Repeat("vus,3,,\n", 20)), 0600))
				require.NoError(t, os.Rename(other, run.ResultFile))
			case "directory":
				require.NoError(t, os.Remove(run.ResultFile))
				require.NoError(t, os.Mkdir(run.ResultFile, 0700))
			}
			err = e.Collect(context.Background(), run)
			require.Error(t, err)
			_, observationErr := e.Observe(context.Background(), run)
			assert.ErrorIs(t, observationErr, err)
		})
	}
}

func TestResetClearsIdentityCountersAndCursor(t *testing.T) {
	e, run := fixture(t, 0)
	putCSV(t, run, csvHeader+"http_req_duration,12.5,0,https://example.test/old\ndata_received,120,,\nvus,3,,\n")
	old := collectAndGather(t, e)
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.RequestLatency))["label"])
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.EgressBytesCounter))["label"])
	assert.Equal(t, "others", metricLabels(single(t, old, metricschema.StatusCounter))["label"])
	assert.Equal(t, "Transport error (cause unavailable)", metricLabels(single(t, old, metricschema.StatusCounter))["status"])
	next := run
	next.RunID, next.CollectionID, next.PlanID, next.AgentID, next.Zone = "new-run", "new-collection", "new-plan", "new-agent", "new-zone"
	putCSV(t, next, csvHeader+"http_req_duration,30,500,https://example.test/new\n")
	e.Reset(next)
	_, err := e.Observe(context.Background(), run)
	require.ErrorContains(t, err, "active run")
	require.ErrorContains(t, e.Collect(context.Background(), run), "active run")
	all := collectAndGather(t, e)
	// Callback routing needs only the run ID; Reset already supplied the CSV path.
	state, err := e.Observe(context.Background(), agent.RunContext{RunID: next.RunID})
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationReady, state)
	hist := single(t, all, metricschema.RequestLatency)
	assert.Equal(t, uint64(1), hist.GetHistogram().GetSampleCount())
	assert.Equal(t, float64(30), hist.GetHistogram().GetSampleSum())
	assert.Equal(t, map[string]string{"collection_id": "new-collection", "plan_id": "new-plan", "run_id": "new-run", "engine_no": "new-agent", "zone": "new-zone", "label": "others"}, metricLabels(hist))
	assert.NotContains(t, all, metricschema.EgressBytesCounter)
	assert.NotContains(t, all, metricschema.ThreadsGauge)
	assert.Equal(t, "500", metricLabels(single(t, all, metricschema.StatusCounter))["status"])

	appendCSV(t, next, "data_received,19,,\nvus,2,,\n")
	all = collectAndGather(t, e)
	assert.Equal(t, float64(19), single(t, all, metricschema.EgressBytesCounter).GetCounter().GetValue())
	assert.Equal(t, float64(2), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	for _, family := range all {
		for _, metric := range family.Metric {
			labels := metricLabels(metric)
			assert.Equal(t, "new-collection", labels["collection_id"])
			assert.Equal(t, "new-plan", labels["plan_id"])
			assert.Equal(t, "new-run", labels["run_id"])
			assert.Equal(t, "new-agent", labels["engine_no"])
			assert.Equal(t, "new-zone", labels["zone"])
		}
	}
}

func TestLabelBudgetValidationAndUnpreparedExporter(t *testing.T) {
	_, err := New(-1, agent.NewMetricsRegistry())
	require.Error(t, err)
	_, err = New(10, nil)
	require.Error(t, err)
	e, err := New(10, agent.NewMetricsRegistry())
	require.NoError(t, err)
	require.Error(t, e.Collect(context.Background(), agent.RunContext{}))
	_, err = e.Observe(context.Background(), agent.RunContext{})
	require.Error(t, err)
}

func TestCanceledObservationDoesNotPoisonRun(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, csvHeader+"vus,3,,\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Observe(ctx, run)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, e.Collect(ctx, run), context.Canceled)
	assert.Zero(t, e.offset)
	require.NoError(t, e.Collect(context.Background(), run))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationReady, state)
}

func TestConcurrentObservationAndScrapeDoNotDoubleCount(t *testing.T) {
	e, run := fixture(t, 10)
	putCSV(t, run, csvHeader+strings.Repeat("http_req_duration,12.5,200,https://example.test/concurrent\n", 100))
	var group sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for index := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			switch index % 3 {
			case 0:
				errorsSeen <- e.Collect(context.Background(), run)
			case 1:
				_, err := e.Observe(context.Background(), run)
				errorsSeen <- err
			default:
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
	hist := single(t, collectAndGather(t, e), metricschema.RequestLatency).GetHistogram()
	assert.Equal(t, uint64(100), hist.GetSampleCount())
	assert.Equal(t, float64(1250), hist.GetSampleSum())
}
