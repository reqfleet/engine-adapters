package metrics

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

func assertTimingSamples(t *testing.T, all map[string]*dto.MetricFamily, family string, want map[string][]float64) {
	t.Helper()
	if len(want) == 0 {
		require.NotContains(t, all, family)
		return
	}
	require.Contains(t, all, family)
	require.Len(t, all[family].Metric, len(want))
	bounds := metricschema.RequestLatencyBuckets()
	if family == metricschema.LLMResponseLatency {
		bounds = metricschema.LLMResponseLatencyBuckets()
	}
	bounds = append(bounds, math.Inf(1))
	for _, metric := range all[family].Metric {
		label := metricLabels(metric)["label"]
		require.Contains(t, want, label)
		values := want[label]
		hist := metric.GetHistogram()
		require.Equal(t, uint64(len(values)), hist.GetSampleCount())
		var sum float64
		for _, value := range values {
			sum += value
		}
		require.Equal(t, sum, hist.GetSampleSum())
		require.Len(t, hist.Bucket, len(bounds))
		for index, bound := range bounds {
			var count uint64
			for _, value := range values {
				if value <= bound {
					count++
				}
			}
			require.Equal(t, bound, hist.Bucket[index].GetUpperBound())
			require.Equal(t, count, hist.Bucket[index].GetCumulativeCount())
		}
	}
}

func TestLLMTimingsAreIndependentAndRetained(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric))
	// Custom timing attribution is reserved, not the CSV name, status or URL.
	putCSV(t, run, "name,url,status,metric_value,metric_name\n"+
		"unrelated,,,0,"+LLMResponseDurationMetric+"\n"+
		"unrelated,"+strings.Repeat("界", 1024)+",ignored,15000,"+LLMResponseDurationMetric+"\n"+
		"unrelated,,,300001,"+LLMResponseDurationMetric+"\n"+
		"unrelated,,,2.5,"+LLMTimeToFirstTokenMetric+"\n"+
		"unrelated,,,10001,"+LLMTimeToFirstTokenMetric+"\n")
	all := collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {0, 15000, 300001}})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"ttft": {2.5, 10001}})
	for _, family := range []string{metricschema.StatusCounter, metricschema.EgressBytesCounter, metricschema.ThreadsGauge} {
		require.NotContains(t, all, family)
	}
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationReady, state)
	// An ordinary HTTP grouping name must never select the wide histogram.
	appendCSV(t, run, "http,llm_resp,500,12,http_req_duration\nbytes,,,17,data_received\nvus,,,3,vus\n")
	all = collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"ttft": {2.5, 10001}, "llm_resp": {12}})
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {0, 15000, 300001}})
	status := single(t, all, metricschema.StatusCounter)
	require.Equal(t, "500", metricLabels(status)["status"])
	require.Equal(t, float64(1), status.GetCounter().GetValue())
	assertReceivedBytes(t, all, map[string]float64{"k6": 17})
	require.Equal(t, float64(3), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	for range 2 {
		require.Equal(t, all, collectAndGather(t, e), "retained scrapes must not reingest timings")
	}
}

func TestLLMTimingsShareAdmissionWithoutChangingHistogramFamily(t *testing.T) {
	for _, budget := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			e, run := fixture(t, budget)
			require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric))
			require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
			putCSV(t, run, csvHeader+"http_req_duration,4,200,http\n"+
				LLMResponseDurationMetric+",20000,,\n"+LLMTimeToFirstTokenMetric+",5,,\n"+
				RequestReceivedBytesMetric+",8,,ttft\nhttp_req_duration,6,500,http\n"+
				LLMResponseDurationMetric+",90000,,\n")
			responseLabel, ttftLabel, httpLabel := "others", "others", "others"
			if budget > 0 {
				httpLabel = "http"
			}
			if budget > 1 {
				responseLabel = "llm_resp"
			}
			if budget > 2 {
				ttftLabel = "ttft"
			}
			ordinary := map[string][]float64{httpLabel: {4, 6}}
			ordinary[ttftLabel] = append(ordinary[ttftLabel], 5)
			all := collectAndGather(t, e)
			assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{responseLabel: {20000, 90000}})
			assertTimingSamples(t, all, metricschema.RequestLatency, ordinary)
			assertReceivedBytes(t, all, map[string]float64{ttftLabel: 8})
			require.Len(t, all[metricschema.StatusCounter].Metric, 2)
			for _, metric := range all[metricschema.StatusCounter].Metric {
				require.Equal(t, httpLabel, metricLabels(metric)["label"])
				require.Equal(t, float64(1), metric.GetCounter().GetValue())
			}
		})
	}
}

func TestLLMSourceSelectionAndReset(t *testing.T) {
	e, run := fixture(t, 1)
	previous := run
	for index, sources := range [][2]string{{LLMResponseDurationMetric, ""}, {"", LLMTimeToFirstTokenMetric}, {"", ""}} {
		run.RunID = fmt.Sprintf("run-%d", index)
		run.CollectionID = fmt.Sprintf("collection-%d", index)
		e.Reset(run)
		require.ErrorContains(t, e.ConfigureLLMTimings(previous, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric), "active run")
		if sources != [2]string{} {
			require.NoError(t, e.ConfigureLLMTimings(agent.RunContext{RunID: run.RunID}, sources[0], sources[1]))
		}
		putCSV(t, run, csvHeader)
		if sources[0] == "" {
			appendCSV(t, run, LLMResponseDurationMetric+",NaN,,\n")
		}
		if sources[1] == "" {
			appendCSV(t, run, LLMTimeToFirstTokenMetric+",-1,,\n")
		}
		require.Empty(t, collectAndGather(t, e))
		state, err := e.Observe(context.Background(), run)
		require.NoError(t, err)
		require.Equal(t, protocol.ObservationPending, state)
		for _, source := range sources {
			if source != "" {
				appendCSV(t, run, source+",0,,\n")
			}
		}
		all := collectAndGather(t, e)
		response, ttft := map[string][]float64{}, map[string][]float64{}
		if sources[0] != "" {
			response["llm_resp"] = []float64{0}
		}
		if sources[1] != "" {
			ttft["ttft"] = []float64{0}
		}
		assertTimingSamples(t, all, metricschema.LLMResponseLatency, response)
		assertTimingSamples(t, all, metricschema.RequestLatency, ttft)
		for _, family := range all {
			for _, metric := range family.Metric {
				require.Equal(t, run.RunID, metricLabels(metric)["run_id"])
				require.Equal(t, run.CollectionID, metricLabels(metric)["collection_id"])
			}
		}
		previous = run
	}
}

func TestInvalidLLMTimingSamplesFailIngestion(t *testing.T) {
	for _, source := range []string{LLMResponseDurationMetric, LLMTimeToFirstTokenMetric} {
		for _, value := range []string{"", "no", "-1", "NaN", "+Inf", "-Inf", "1e999"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				e, run := fixture(t, 0)
				require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric))
				putCSV(t, run, csvHeader+source+","+value+",,\n")
				err := e.Collect(context.Background(), run)
				require.ErrorContains(t, err, "invalid "+source+" sample")
				appendCSV(t, run, source+",1,,\n")
				require.ErrorIs(t, e.Collect(context.Background(), run), err)
				require.Empty(t, scrape(e).Body.String())
			})
		}
	}
}

func TestInvalidLLMSourceDoesNotReplaceSelection(t *testing.T) {
	e, run := fixture(t, 2)
	require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric))
	for _, sources := range [][2]string{{"http_req_duration", ""}, {"", "ttft"}, {LLMResponseDurationMetric, "invalid"}, {" LLM_RESPONSE_DURATION", LLMTimeToFirstTokenMetric}} {
		require.Error(t, e.ConfigureLLMTimings(run, sources[0], sources[1]))
	}
	putCSV(t, run, csvHeader+LLMResponseDurationMetric+",7,,\n"+LLMTimeToFirstTokenMetric+",2,,\n")
	all := collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {7}})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"ttft": {2}})
}
