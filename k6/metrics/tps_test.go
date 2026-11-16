package metrics

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

func TestTPSPayloadDeltasCadenceAndRetention(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.ConfigureReceivedBytes(run, LLMStreamedBytesMetric))
	require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric, LLMInterChunkDurationMetric))
	putCSV(t, run, csvHeader+DataReceivedMetric+",999,,\n"+RequestReceivedBytesMetric+",NaN,,\n")
	require.Empty(t, collectAndGather(t, e))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationPending, state)
	appendCSV(t, run, LLMStreamedBytesMetric+",3,,\n"+
		LLMStreamedBytesMetric+",7,not-http,"+strings.Repeat("界", 1024)+"\n"+
		LLMInterChunkDurationMetric+",2.5,,\n"+
		LLMInterChunkDurationMetric+",10001,ignored,"+strings.Repeat("a", 1024)+"\n"+
		DataReceivedMetric+",NaN,,\n"+RequestReceivedBytesMetric+",999,,ignored\n")
	all := collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{"tps": 10})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {2.5, 10001}})
	require.NotContains(t, all, metricschema.StatusCounter)
	require.NotContains(t, all, metricschema.ThreadsGauge)
	require.NotContains(t, all, metricschema.LLMResponseLatency)
	appendCSV(t, run, "http_req_duration,4,500,http\nvus,3,,\n"+
		LLMResponseDurationMetric+",15000,,\n"+LLMTimeToFirstTokenMetric+",5,,\n")
	all = collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{"tps": 10})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {2.5, 10001}, "http": {4}, "ttft": {5}})
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {15000}})
	status := single(t, all, metricschema.StatusCounter)
	require.Equal(t, "http", metricLabels(status)["label"])
	require.Equal(t, "500", metricLabels(status)["status"])
	require.Equal(t, float64(1), status.GetCounter().GetValue())
	require.Equal(t, float64(3), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	for range 2 {
		require.Equal(t, all, collectAndGather(t, e))
	}
	// A trailing payload delta is drained once, without a synthetic completion.
	appendCSV(t, run, LLMStreamedBytesMetric+",11,,")
	require.Equal(t, all, collectAndGather(t, e))
	appendCSV(t, run, "\n")
	final := collectAndGather(t, e)
	assertReceivedBytes(t, final, map[string]float64{"tps": 21})
	require.Equal(t, all[metricschema.RequestLatency], final[metricschema.RequestLatency])
	require.Equal(t, all[metricschema.StatusCounter], final[metricschema.StatusCounter])
	require.Equal(t, final, collectAndGather(t, e))
}

func TestTPSSharedAdmission(t *testing.T) {
	for _, budget := range []int{0, 1, 2} {
		for _, intervalFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/interval-first=%t", budget, intervalFirst), func(t *testing.T) {
				e, run := fixture(t, budget)
				require.NoError(t, e.ConfigureReceivedBytes(run, LLMStreamedBytesMetric))
				require.NoError(t, e.ConfigureLLMTimings(run, "", "", LLMInterChunkDurationMetric))
				bytes := LLMStreamedBytesMetric + ",8,,\n"
				interval := LLMInterChunkDurationMetric + ",5,,\n"
				rows := bytes + interval
				if intervalFirst {
					rows = interval + bytes
				}
				putCSV(t, run, csvHeader+"http_req_duration,4,200,http\n"+rows+"http_req_duration,6,500,later\n")
				httpLabel, tpsLabel := "others", "others"
				if budget > 0 {
					httpLabel = "http"
				}
				if budget > 1 {
					tpsLabel = "tps"
				}
				want := map[string][]float64{httpLabel: {4}}
				want[tpsLabel] = append(want[tpsLabel], 5)
				want["others"] = append(want["others"], 6)
				all := collectAndGather(t, e)
				assertTimingSamples(t, all, metricschema.RequestLatency, want)
				assertReceivedBytes(t, all, map[string]float64{tpsLabel: 8})
				require.Len(t, all[metricschema.StatusCounter].Metric, 2)
				for _, metric := range all[metricschema.StatusCounter].Metric {
					require.Equal(t, float64(1), metric.GetCounter().GetValue())
				}
				require.NotContains(t, all, metricschema.ThreadsGauge)
			})
		}
	}
}

func TestTPSIndependentSelectorsAndMissingObservations(t *testing.T) {
	for _, source := range []string{"", RequestReceivedBytesMetric, LLMStreamedBytesMetric} {
		for _, interval := range []string{"", LLMInterChunkDurationMetric} {
			t.Run(source+"/"+interval, func(t *testing.T) {
				e, run := fixture(t, 10)
				require.NoError(t, e.ConfigureReceivedBytes(run, source))
				require.NoError(t, e.ConfigureLLMTimings(run, "", "", interval))
				putCSV(t, run, csvHeader)
				if source != LLMStreamedBytesMetric {
					appendCSV(t, run, LLMStreamedBytesMetric+",NaN,,\n")
				}
				if interval == "" {
					appendCSV(t, run, LLMInterChunkDurationMetric+",-1,,\n")
				}
				require.Empty(t, collectAndGather(t, e))
				appendCSV(t, run, DataReceivedMetric+",99,,\n"+RequestReceivedBytesMetric+",7,,request\n")
				all := collectAndGather(t, e)
				want := map[string]float64{}
				if source == "" {
					want["k6"] = 99
				} else if source == RequestReceivedBytesMetric {
					want["request"] = 7
				}
				assertReceivedBytes(t, all, want)
				assertTimingSamples(t, all, metricschema.RequestLatency, nil)
				if source == LLMStreamedBytesMetric {
					appendCSV(t, run, LLMStreamedBytesMetric+",3,,\n")
					want["tps"] = 3
				}
				if interval != "" {
					appendCSV(t, run, LLMInterChunkDurationMetric+",2,,\n")
				}
				all = collectAndGather(t, e)
				assertReceivedBytes(t, all, want)
				intervals := map[string][]float64{}
				if interval != "" {
					intervals["tps"] = []float64{2}
				}
				assertTimingSamples(t, all, metricschema.RequestLatency, intervals)
				require.NotContains(t, all, metricschema.StatusCounter)
				require.NotContains(t, all, metricschema.ThreadsGauge)
			})
		}
	}
}

func TestTPSZeroAndInvalidSamples(t *testing.T) {
	for _, source := range []string{LLMStreamedBytesMetric, LLMInterChunkDurationMetric} {
		for _, value := range []string{"0", "", "no", "-1", "NaN", "+Inf", "-Inf", "1e999"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				e, run := fixture(t, 0)
				require.NoError(t, e.ConfigureReceivedBytes(run, LLMStreamedBytesMetric))
				require.NoError(t, e.ConfigureLLMTimings(run, "", "", LLMInterChunkDurationMetric))
				// Required CSV columns remain present; source tags may be empty.
				putCSV(t, run, csvHeader+source+","+value+",,\n")
				if value != "0" {
					err := e.Collect(context.Background(), run)
					require.ErrorContains(t, err, "invalid "+source+" sample")
					require.Empty(t, scrape(e).Body.String())
					return
				}
				all := collectAndGather(t, e)
				bytes, intervals := map[string]float64{}, map[string][]float64{}
				if source == LLMStreamedBytesMetric {
					bytes["others"] = 0
				} else {
					intervals["others"] = []float64{0}
				}
				assertReceivedBytes(t, all, bytes)
				assertTimingSamples(t, all, metricschema.RequestLatency, intervals)
				require.NotContains(t, all, metricschema.StatusCounter)
				require.NotContains(t, all, metricschema.ThreadsGauge)
				state, err := e.Observe(context.Background(), run)
				require.NoError(t, err)
				require.Equal(t, protocol.ObservationReady, state)
			})
		}
	}
}

func TestTPSInvalidConfigurationAndCoordinatedReset(t *testing.T) {
	e, run := fixture(t, 3)
	require.NoError(t, e.ConfigureReceivedBytes(run, LLMStreamedBytesMetric))
	require.NoError(t, e.ConfigureLLMTimings(run, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric, LLMInterChunkDurationMetric))
	for _, selectors := range [][3]string{{"", "", "tps"}, {"bad", "", ""}, {"", "bad", ""}, {"", "", " LLM_INTER_CHUNK_DURATION"}} {
		require.Error(t, e.ConfigureLLMTimings(run, selectors[0], selectors[1], selectors[2]))
	}
	require.Error(t, e.ConfigureReceivedBytes(run, "tps"))
	stale := agent.RunContext{RunID: "stale"}
	require.Error(t, e.ConfigureReceivedBytes(stale, ""))
	require.Error(t, e.ConfigureLLMTimings(stale, "", "", ""))
	putCSV(t, run, csvHeader+LLMStreamedBytesMetric+",7,,\n"+LLMInterChunkDurationMetric+",2,,\n"+
		LLMResponseDurationMetric+",15000,,\n"+LLMTimeToFirstTokenMetric+",3,,\n")
	all := collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{"tps": 7})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {2}, "ttft": {3}})
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {15000}})
	previous := run
	run.RunID = "next"
	run.CollectionID = "next-collection"
	e.Reset(run)
	require.Error(t, e.ConfigureReceivedBytes(previous, LLMStreamedBytesMetric))
	require.Error(t, e.ConfigureLLMTimings(previous, LLMResponseDurationMetric, LLMTimeToFirstTokenMetric, LLMInterChunkDurationMetric))
	putCSV(t, run, csvHeader+LLMStreamedBytesMetric+",NaN,,\n"+LLMInterChunkDurationMetric+",NaN,,\n"+
		LLMResponseDurationMetric+",NaN,,\n"+LLMTimeToFirstTokenMetric+",NaN,,\n"+
		DataReceivedMetric+",11,,\nhttp_req_duration,4,200,new-http\n")
	all = collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{"k6": 11})
	assertRequestMetrics(t, all, map[string]requestMetrics{"new-http": {count: 1, sum: 4, statuses: map[string]float64{"200": 1}}})
	require.NotContains(t, all, metricschema.LLMResponseLatency)
	for _, family := range all {
		for _, metric := range family.Metric {
			require.Equal(t, run.RunID, metricLabels(metric)["run_id"])
			require.Equal(t, run.CollectionID, metricLabels(metric)["collection_id"])
		}
	}
}
