package metrics

import (
	"context"
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

func assertStatusSamples(t *testing.T, all map[string]*dto.MetricFamily, want map[string]map[string]float64) {
	t.Helper()
	if len(want) == 0 {
		require.NotContains(t, all, metricschema.StatusCounter)
		return
	}
	require.Contains(t, all, metricschema.StatusCounter)
	got := make(map[string]map[string]float64)
	for _, metric := range all[metricschema.StatusCounter].Metric {
		labels := metricLabels(metric)
		if got[labels["label"]] == nil {
			got[labels["label"]] = make(map[string]float64)
		}
		got[labels["label"]][labels["status"]] = metric.GetCounter().GetValue()
	}
	require.Equal(t, want, got)
}

func TestTPSTokenPointsAndRetention(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
	require.NoError(t, e.ConfigureLLMTimings(run, metricschema.LLMResponseDurationMetric, metricschema.LLMTimeToFirstTokenMetric, ""))
	putCSV(t, run, "name,url,status,metric_value,metric_name\n")
	require.Empty(t, collectAndGather(t, e))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationPending, state)

	// The first received token counts, without waiting for a second or completion.
	appendCSV(t, run, "unrelated,,,1,"+metricschema.LLMTokenEventsMetric+"\n")
	all := collectAndGather(t, e)
	assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 1}})
	require.Len(t, all, 1, "tokens create only status counts")
	state, err = e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationReady, state)

	// CSV name, URL and status tags cannot replace the reserved classification.
	appendCSV(t, run, "unrelated,"+strings.Repeat("界", 1024)+",not-http,1,"+metricschema.LLMTokenEventsMetric+"\n"+
		"unrelated,other,500,1,"+metricschema.LLMTokenEventsMetric+"\n")
	all = collectAndGather(t, e)
	assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 3}})
	require.Len(t, all, 1)

	appendCSV(t, run, "http,http,200,4,http_req_duration\nhttp,http,500,6,http_req_duration\n"+
		"vus,,,3,vus\nbytes,,,999,data_received\n"+
		"response,,,15000,"+metricschema.LLMResponseDurationMetric+"\nfirst,,,5,"+metricschema.LLMTimeToFirstTokenMetric+"\n")
	all = collectAndGather(t, e)
	assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 3}, "http": {"200": 1, "500": 1}})
	assertReceivedBytes(t, all, map[string]float64{"k6": 999})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"http": {4, 6}, "ttft": {5}})
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {15000}})
	require.Equal(t, float64(3), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	for range 2 {
		require.Equal(t, all, collectAndGather(t, e))
	}

	// Final draining commits a partial token row once, with no completion count.
	appendCSV(t, run, "final,,,1,"+metricschema.LLMTokenEventsMetric)
	require.Equal(t, all, collectAndGather(t, e))
	appendCSV(t, run, "\n")
	require.NoError(t, e.Collect(context.Background(), run))
	final := collectAndGather(t, e)
	assertStatusSamples(t, final, map[string]map[string]float64{"tps": {"200": 4}, "http": {"200": 1, "500": 1}})
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency, metricschema.EgressBytesCounter, metricschema.ThreadsGauge} {
		require.Equal(t, all[family], final[family])
	}
	require.Equal(t, final, collectAndGather(t, e))
}

func TestTPSSharedAdmission(t *testing.T) {
	for _, budget := range []int{0, 1, 2} {
		for _, tokensFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/tokens-first=%t", budget, tokensFirst), func(t *testing.T) {
				e, run := fixture(t, budget)
				require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
				token := metricschema.LLMTokenEventsMetric + ",1,,\n"
				http := "http_req_duration,4,200,http\n"
				rows := http + token
				if tokensFirst {
					rows = token + http
				}
				putCSV(t, run, csvHeader+rows+"http_req_duration,6,500,later\n"+token+http)
				httpLabel, tpsLabel := "others", "others"
				if budget > 1 || budget > 0 && !tokensFirst {
					httpLabel = "http"
				}
				if budget > 1 || budget > 0 && tokensFirst {
					tpsLabel = "tps"
				}
				latencies := map[string][]float64{httpLabel: {4, 4}}
				latencies["others"] = append(latencies["others"], 6)
				statuses := map[string]map[string]float64{httpLabel: {"200": 2}}
				if statuses[tpsLabel] == nil {
					statuses[tpsLabel] = make(map[string]float64)
				}
				statuses[tpsLabel]["200"] += 2
				if statuses["others"] == nil {
					statuses["others"] = make(map[string]float64)
				}
				statuses["others"]["500"] = 1
				all := collectAndGather(t, e)
				assertTimingSamples(t, all, metricschema.RequestLatency, latencies)
				assertStatusSamples(t, all, statuses)
				require.Len(t, all, 2, "overflow tokens still create only status counts")
			})
		}
	}
}

func TestTPSAdmissionIsSharedWithRequestBytesAndTimings(t *testing.T) {
	for _, source := range []string{RequestReceivedBytesMetric, metricschema.LLMTimeToFirstTokenMetric, metricschema.LLMResponseDurationMetric} {
		for _, tokensFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tokens-first=%t", source, tokensFirst), func(t *testing.T) {
				e, run := fixture(t, 1)
				require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
				require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
				require.NoError(t, e.ConfigureLLMTimings(run, metricschema.LLMResponseDurationMetric, metricschema.LLMTimeToFirstTokenMetric, ""))
				token := metricschema.LLMTokenEventsMetric + ",1,,\n"
				other := source + ",7,,request\n"
				rows := other + token
				tpsLabel := "others"
				otherLabel := map[string]string{RequestReceivedBytesMetric: "request", metricschema.LLMTimeToFirstTokenMetric: "ttft", metricschema.LLMResponseDurationMetric: "llm_resp"}[source]
				if tokensFirst {
					rows = token + other
					tpsLabel, otherLabel = "tps", "others"
				}
				putCSV(t, run, csvHeader+rows)
				all := collectAndGather(t, e)
				assertStatusSamples(t, all, map[string]map[string]float64{tpsLabel: {"200": 1}})
				switch source {
				case RequestReceivedBytesMetric:
					assertReceivedBytes(t, all, map[string]float64{otherLabel: 7})
				case metricschema.LLMTimeToFirstTokenMetric:
					assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{otherLabel: {7}})
				case metricschema.LLMResponseDurationMetric:
					assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{otherLabel: {7}})
				}
				require.Len(t, all, 2)
			})
		}
	}
}

func TestTPSIndependentByteSelectionAndMissingObservations(t *testing.T) {
	for _, source := range []string{"", RequestReceivedBytesMetric} {
		t.Run(source, func(t *testing.T) {
			e, run := fixture(t, 10)
			require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
			require.NoError(t, e.ConfigureReceivedBytes(run, source))
			// Neither missing token points nor unrelated totals imply a zero or fallback TPS.
			putCSV(t, run, csvHeader+"llm_streamed_bytes,999,,\nhttp_reqs,1,,\n"+
				"llm_completion_tokens,42,,\ncontrol,1,,\nrole,1,,\nDONE,1,,\n,1,,\n")
			require.Empty(t, collectAndGather(t, e))
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			require.Equal(t, protocol.ObservationPending, state)
			appendCSV(t, run, DataReceivedMetric+",99,,\n")
			all := collectAndGather(t, e)
			want := map[string]float64{}
			if source == "" {
				want["k6"] = 99
			}
			assertReceivedBytes(t, all, want)
			assertStatusSamples(t, all, nil)
			appendCSV(t, run, RequestReceivedBytesMetric+",7,,request\n"+metricschema.LLMTokenEventsMetric+",1,,\n")
			if source != "" {
				want["request"] = 7
			}
			all = collectAndGather(t, e)
			assertReceivedBytes(t, all, want)
			assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 1}})
			unselected := RequestReceivedBytesMetric
			if source != "" {
				unselected = DataReceivedMetric
			}
			appendCSV(t, run, unselected+",NaN,,\n"+metricschema.LLMTokenEventsMetric+",1,,\n")
			all = collectAndGather(t, e)
			assertReceivedBytes(t, all, want)
			assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 2}})
			require.Len(t, all, 2, "tokens and bytes do not create timing or concurrency")
		})
	}
}

func TestTPSRejectsInvalidAndNonUnitSelectedPoints(t *testing.T) {
	for _, value := range []string{"0", "-0", "2", "42", "0.5", "1.5", "", "no", "-1", "NaN", "+Inf", "-Inf", "1e999"} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selected=%t", value, selected), func(t *testing.T) {
				e, run := fixture(t, 0)
				if selected {
					require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
				}
				putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+","+value+",,\n")
				if selected {
					err := e.Collect(context.Background(), run)
					require.ErrorContains(t, err, "invalid "+metricschema.LLMTokenEventsMetric+" sample")
					require.Equal(t, err, e.Collect(context.Background(), run), "invalid points remain a collection failure")
					state, observeErr := e.Observe(context.Background(), run)
					require.Equal(t, err, observeErr)
					require.Equal(t, protocol.ObservationPending, state)
				} else {
					require.Empty(t, collectAndGather(t, e))
					state, err := e.Observe(context.Background(), run)
					require.NoError(t, err)
					require.Equal(t, protocol.ObservationPending, state)
				}
				require.Empty(t, scrape(e).Body.String())
			})
		}
	}
}

func TestTPSConfigurationAndDisable(t *testing.T) {
	exporter := &Exporter{}
	require.ErrorContains(t, exporter.ConfigureTPS(agent.RunContext{}, metricschema.LLMTokenEventsMetric), "active run")
	e, run := fixture(t, 1)
	require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
	for _, source := range []string{"tps", "llm_streamed_bytes", DataReceivedMetric, " " + metricschema.LLMTokenEventsMetric, strings.ToUpper(metricschema.LLMTokenEventsMetric)} {
		require.ErrorContains(t, e.ConfigureTPS(run, source), "unsupported TPS source")
	}
	require.ErrorContains(t, e.ConfigureTPS(agent.RunContext{RunID: "stale"}, ""), "active run")
	putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+",1,,\n")
	assertStatusSamples(t, collectAndGather(t, e), map[string]map[string]float64{"tps": {"200": 1}})
	run.RunID = "disabled"
	run.Metrics = nil
	e.Reset(run)
	e.launch(t, &run)
	require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
	require.NoError(t, e.ConfigureTPS(run, ""))
	putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+",1,,\n"+metricschema.LLMTokenEventsMetric+",NaN,,\n")
	require.Empty(t, collectAndGather(t, e))
}

func TestTPSNewRunResetsSelectionIdentityAndAdmission(t *testing.T) {
	e, run := fixture(t, 3)
	require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
	require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
	require.NoError(t, e.ConfigureLLMTimings(run, metricschema.LLMResponseDurationMetric, metricschema.LLMTimeToFirstTokenMetric, ""))
	putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+",1,,\n"+
		metricschema.LLMResponseDurationMetric+",15000,,\n"+metricschema.LLMTimeToFirstTokenMetric+",3,,\n")
	all := collectAndGather(t, e)
	assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 1}})
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"ttft": {3}})
	assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"llm_resp": {15000}})
	previous := run
	oldRegistry := previous.Metrics.Registry()
	oldMetrics := scrapeRegistry(oldRegistry).Body.String()
	run.RunID, run.CollectionID, run.PlanID, run.AgentID, run.Zone = "next", "next-collection", "next-plan", "next-agent", "next-zone"
	run.Metrics = nil
	e.Reset(run)
	require.Equal(t, oldMetrics, scrapeRegistry(oldRegistry).Body.String())
	e.launch(t, &run)
	require.Empty(t, scrape(e).Body.String())
	require.Error(t, e.ConfigureTPS(previous, metricschema.LLMTokenEventsMetric))
	require.Error(t, e.ConfigureReceivedBytes(previous, RequestReceivedBytesMetric))
	require.Error(t, e.ConfigureLLMTimings(previous, metricschema.LLMResponseDurationMetric, metricschema.LLMTimeToFirstTokenMetric, ""))
	require.Error(t, e.Collect(context.Background(), previous))
	putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+",NaN,,\n"+
		metricschema.LLMResponseDurationMetric+",NaN,,\n"+metricschema.LLMTimeToFirstTokenMetric+",NaN,,\n"+
		RequestReceivedBytesMetric+",NaN,,\n")
	require.Empty(t, collectAndGather(t, e))
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, protocol.ObservationPending, state)
	appendCSV(t, run, DataReceivedMetric+",11,,\nhttp_req_duration,4,200,new-http\n")
	all = collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{"k6": 11})
	assertRequestMetrics(t, all, map[string]requestMetrics{"new-http": {count: 1, sum: 4, statuses: map[string]float64{"200": 1}}})
	require.NotContains(t, all, metricschema.LLMResponseLatency)
	for _, family := range all {
		for _, metric := range family.Metric {
			labels := metricLabels(metric)
			require.Equal(t, run.RunID, labels["run_id"])
			require.Equal(t, run.CollectionID, labels["collection_id"])
			require.Equal(t, run.PlanID, labels["plan_id"])
			require.Equal(t, run.AgentID, labels["engine_no"])
			require.Equal(t, run.Zone, labels["zone"])
		}
	}
	require.Equal(t, oldMetrics, scrapeRegistry(oldRegistry).Body.String())
	// Re-enabling in a fresh run starts at one, never at the previous token total.
	run.RunID = "tokens-again"
	run.Metrics = nil
	e.Reset(run)
	e.launch(t, &run)
	require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
	putCSV(t, run, csvHeader+metricschema.LLMTokenEventsMetric+",1,,\n")
	all = collectAndGather(t, e)
	assertStatusSamples(t, all, map[string]map[string]float64{"tps": {"200": 1}})
	require.Len(t, all, 1)
	require.Equal(t, run.RunID, metricLabels(single(t, all, metricschema.StatusCounter))["run_id"])
	require.Equal(t, oldMetrics, scrapeRegistry(oldRegistry).Body.String())
}
