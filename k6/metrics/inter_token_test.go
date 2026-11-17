package metrics

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/stretchr/testify/require"
)

func TestInterTokenIndependentAndFinalDrain(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.ConfigureLLMTimings(run, "", "", metricschema.LLMInterTokenDurationMetric))
	putCSV(t, run, "name,url,status,metric_value,metric_name\n"+
		"ignored,"+strings.Repeat("界", 1024)+",invalid,0,"+metricschema.LLMInterTokenDurationMetric+"\n"+
		"ignored,,,0.125,"+metricschema.LLMInterTokenDurationMetric+"\n")
	all := collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {0, .125}})
	require.Len(t, all, 1, "gap samples create no status, bytes, concurrency or response timing")
	appendCSV(t, run, "ignored,,,2.5,"+metricschema.LLMInterTokenDurationMetric)
	require.Equal(t, all, collectAndGather(t, e))
	appendCSV(t, run, "\n")
	require.NoError(t, e.Collect(context.Background(), run))
	all = collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {0, .125, 2.5}})
	require.Equal(t, all, collectAndGather(t, e))
}

func TestInterTokenAndTokenCountsShareAdmission(t *testing.T) {
	for _, budget := range []int{0, 1, 2} {
		for _, gapsFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("budget=%d/gapsFirst=%t", budget, gapsFirst), func(t *testing.T) {
				e, run := fixture(t, budget)
				require.NoError(t, e.ConfigureTPS(run, metricschema.LLMTokenEventsMetric))
				require.NoError(t, e.ConfigureLLMTimings(run, metricschema.LLMResponseDurationMetric, metricschema.LLMTimeToFirstTokenMetric, metricschema.LLMInterTokenDurationMetric))
				require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
				tokens := strings.Repeat(metricschema.LLMTokenEventsMetric+",1,,\n", 3)
				gaps := metricschema.LLMInterTokenDurationMetric + ",0,,\n" + metricschema.LLMInterTokenDurationMetric + ",2.5,,\n"
				rows := tokens + gaps
				if gapsFirst {
					rows = gaps + tokens
				}
				putCSV(t, run, csvHeader+rows+"http_req_duration,7,500,http\n"+RequestReceivedBytesMetric+",9,,tps\n"+metricschema.LLMTimeToFirstTokenMetric+",1,,\n"+metricschema.LLMResponseDurationMetric+",20,,\n")
				tokenLabel, httpLabel := "others", "others"
				if budget > 0 {
					tokenLabel = "tps"
				}
				if budget > 1 {
					httpLabel = "http"
				}
				latency := map[string][]float64{tokenLabel: {0, 2.5}}
				latency[httpLabel] = append(latency[httpLabel], 7)
				latency["others"] = append(latency["others"], 1)
				statuses := map[string]map[string]float64{tokenLabel: {"200": 3}}
				if statuses[httpLabel] == nil {
					statuses[httpLabel] = map[string]float64{}
				}
				statuses[httpLabel]["500"] = 1
				all := collectAndGather(t, e)
				assertTimingSamples(t, all, metricschema.RequestLatency, latency)
				assertTimingSamples(t, all, metricschema.LLMResponseLatency, map[string][]float64{"others": {20}})
				assertStatusSamples(t, all, statuses)
				assertReceivedBytes(t, all, map[string]float64{tokenLabel: 9})
				require.Equal(t, all, collectAndGather(t, e))
			})
		}
	}
}

func TestInterTokenSelectionValidationAndReset(t *testing.T) {
	e, run := fixture(t, 1)
	putCSV(t, run, csvHeader+metricschema.LLMInterTokenDurationMetric+",NaN,,\n")
	require.Empty(t, collectAndGather(t, e), "default ignores unselected gaps")
	require.NoError(t, e.ConfigureLLMTimings(run, "", "", metricschema.LLMInterTokenDurationMetric))
	for _, selectors := range [][3]string{{"", "", "tps"}, {"bad", "", ""}, {"", "bad", ""}} {
		require.Error(t, e.ConfigureLLMTimings(run, selectors[0], selectors[1], selectors[2]))
	}
	appendCSV(t, run, metricschema.LLMInterTokenDurationMetric+",3,,\n"+metricschema.LLMTokenEventsMetric+",NaN,,\n"+metricschema.LLMTimeToFirstTokenMetric+",NaN,,\n"+metricschema.LLMResponseDurationMetric+",NaN,,\n")
	all := collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"tps": {3}})
	require.Len(t, all, 1)
	require.NoError(t, e.ConfigureLLMTimings(run, "", "", ""))
	appendCSV(t, run, metricschema.LLMInterTokenDurationMetric+",-1,,\n")
	require.Equal(t, all, collectAndGather(t, e))
	require.NoError(t, e.ConfigureLLMTimings(run, "", "", metricschema.LLMInterTokenDurationMetric))
	previous := run
	run.RunID = "next"
	e.Reset(run)
	require.Error(t, e.ConfigureLLMTimings(previous, "", "", metricschema.LLMInterTokenDurationMetric))
	require.Error(t, e.Collect(context.Background(), previous))
	putCSV(t, run, csvHeader+metricschema.LLMInterTokenDurationMetric+",NaN,,\nhttp_req_duration,4,200,new\n")
	all = collectAndGather(t, e)
	assertTimingSamples(t, all, metricschema.RequestLatency, map[string][]float64{"new": {4}})
	require.Equal(t, "next", metricLabels(single(t, all, metricschema.RequestLatency))["run_id"])
}

func TestInterTokenRejectsInvalidSelectedValues(t *testing.T) {
	for _, value := range []string{"-1", "NaN", "+Inf", "-Inf", "bad", ""} {
		t.Run(value, func(t *testing.T) {
			e, run := fixture(t, 0)
			require.NoError(t, e.ConfigureLLMTimings(run, "", "", metricschema.LLMInterTokenDurationMetric))
			putCSV(t, run, csvHeader+metricschema.LLMInterTokenDurationMetric+","+value+",,\n")
			require.ErrorContains(t, e.Collect(context.Background(), run), "invalid "+metricschema.LLMInterTokenDurationMetric+" sample")
		})
	}
}
