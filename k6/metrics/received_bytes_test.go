package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertReceivedBytes(t *testing.T, all map[string]*dto.MetricFamily, want map[string]float64) {
	t.Helper()
	if len(want) == 0 {
		assert.NotContains(t, all, metricschema.EgressBytesCounter)
		return
	}
	require.Contains(t, all, metricschema.EgressBytesCounter)
	require.Len(t, all[metricschema.EgressBytesCounter].Metric, len(want))
	got := make(map[string]float64)
	for _, metric := range all[metricschema.EgressBytesCounter].Metric {
		got[metricLabels(metric)["label"]] = metric.GetCounter().GetValue()
	}
	assert.Equal(t, want, got)
}

func TestCustomReceivedBytesPreserveExactURLAndIndependentSamples(t *testing.T) {
	e, run := fixture(t, 10)
	require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
	// Built-in bytes arrive first and must never become a fallback or consume a slot.
	putCSV(t, run, "name,url,metric_name,status,metric_value\n"+
		"aggregate,,"+DataReceivedMetric+",,999\n"+
		"aggregate,,"+DataReceivedMetric+",,not-a-number\n")
	all := collectAndGather(t, e)
	assert.Empty(t, all)
	state, err := e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationPending, state)
	appendCSV(t, run,
		"shared,https://example.test/items?q=one,"+RequestReceivedBytesMetric+",,12.5\n"+
			"shared,https://example.test/items?q=two,"+RequestReceivedBytesMetric+",,20\n"+
			"different,https://example.test/items?q=one,"+RequestReceivedBytesMetric+",not-http,7.5\n"+
			"ignored-name,grouped/{id},"+RequestReceivedBytesMetric+",,3\n"+
			"ignored-name,https://example.test/zero,"+RequestReceivedBytesMetric+",,0\n")
	all = collectAndGather(t, e)
	assertReceivedBytes(t, all, map[string]float64{
		"https://example.test/items?q=one": 20,
		"https://example.test/items?q=two": 20,
		"grouped/{id}":                     3,
		"https://example.test/zero":        0,
	})
	assert.NotContains(t, all, metricschema.RequestLatency)
	assert.NotContains(t, all, metricschema.StatusCounter)
	assert.NotContains(t, all, metricschema.ThreadsGauge)
	state, err = e.Observe(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, protocol.ObservationReady, state)
	appendCSV(t, run,
		"shared,https://example.test/items?q=one,http_req_duration,200,4\n"+
			"shared,https://example.test/items?q=two,http_req_duration,500,6\n"+
			"unrelated,,vus,,2\n"+
			"aggregate,,"+DataReceivedMetric+",,1000\n"+
			"shared,https://example.test/items?q=one,http_reqs,,1\n")
	all = collectAndGather(t, e)
	assertRequestMetrics(t, all, map[string]requestMetrics{
		"https://example.test/items?q=one": {count: 1, sum: 4, statuses: map[string]float64{"200": 1}},
		"https://example.test/items?q=two": {count: 1, sum: 6, statuses: map[string]float64{"500": 1}},
	})
	assertReceivedBytes(t, all, map[string]float64{
		"https://example.test/items?q=one": 20,
		"https://example.test/items?q=two": 20,
		"grouped/{id}":                     3,
		"https://example.test/zero":        0,
	})
	assert.Equal(t, float64(2), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
	for range 2 {
		assert.Equal(t, all, collectAndGather(t, e))
	}
	// A final incomplete append becomes visible only once its record is complete.
	appendCSV(t, run, "last,https://example.test/items?q=one,"+RequestReceivedBytesMetric+",,5")
	assert.Equal(t, all, collectAndGather(t, e))
	appendCSV(t, run, "\n")
	final := collectAndGather(t, e)
	assertReceivedBytes(t, final, map[string]float64{
		"https://example.test/items?q=one": 25,
		"https://example.test/items?q=two": 20,
		"grouped/{id}":                     3,
		"https://example.test/zero":        0,
	})
	assert.Equal(t, all[metricschema.RequestLatency], final[metricschema.RequestLatency])
	assert.Equal(t, all[metricschema.StatusCounter], final[metricschema.StatusCounter])
	assert.Equal(t, final, collectAndGather(t, e))
}

func TestCustomReceivedBytesShareRequestAdmission(t *testing.T) {
	for _, budget := range []int{2, 0} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			e, run := fixture(t, budget)
			require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
			putCSV(t, run, csvHeader+
				DataReceivedMetric+",100,,https://example.test/aggregate\n"+
				RequestReceivedBytesMetric+",1,,others\n"+
				RequestReceivedBytesMetric+",0,,https://example.test/a\n"+
				"http_req_duration,2,200,https://example.test/b\n"+
				RequestReceivedBytesMetric+",20,,https://example.test/b\n"+
				RequestReceivedBytesMetric+",30,,https://example.test/c\n"+
				"http_req_duration,3,201,https://example.test/a\n"+
				"http_req_duration,4,500,https://example.test/c\n"+
				RequestReceivedBytesMetric+",5,,https://example.test/a\n"+
				RequestReceivedBytesMetric+",7,,https://example.test/c\n")
			all := collectAndGather(t, e)
			if budget == 0 {
				assertReceivedBytes(t, all, map[string]float64{"others": 63})
				assertRequestMetrics(t, all, map[string]requestMetrics{
					"others": {count: 3, sum: 9, statuses: map[string]float64{"200": 1, "201": 1, "500": 1}},
				})
			} else {
				assertReceivedBytes(t, all, map[string]float64{
					"https://example.test/a": 5,
					"https://example.test/b": 20,
					"others":                 38,
				})
				assertRequestMetrics(t, all, map[string]requestMetrics{
					"https://example.test/a": {count: 1, sum: 3, statuses: map[string]float64{"201": 1}},
					"https://example.test/b": {count: 1, sum: 2, statuses: map[string]float64{"200": 1}},
					"others":                 {count: 1, sum: 4, statuses: map[string]float64{"500": 1}},
				})
			}
		})
	}
}

func TestCustomReceivedBytesRejectOversizedLabelsBeforeAdmission(t *testing.T) {
	for _, character := range []string{"a", "界"} {
		for _, budget := range []int{1, 0} {
			t.Run(fmt.Sprintf("%s/budget=%d", character, budget), func(t *testing.T) {
				e, run := fixture(t, budget)
				require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
				valid := strings.Repeat(character, 1023/len(character))
				atLimit, overLimit := valid+"b", valid+"界"
				putCSV(t, run, csvHeader+fmt.Sprintf(
					RequestReceivedBytesMetric+",100,,%s\n"+RequestReceivedBytesMetric+",200,,%s\n", atLimit, overLimit))
				assert.Empty(t, collectAndGather(t, e))
				state, err := e.Observe(context.Background(), run)
				require.NoError(t, err)
				assert.Equal(t, protocol.ObservationPending, state)
				appendCSV(t, run, fmt.Sprintf(
					RequestReceivedBytesMetric+",3,,%s\n"+
						"http_req_duration,4,201,%s\n"+
						RequestReceivedBytesMetric+",5,,overflow\n"+
						RequestReceivedBytesMetric+",300,,%s\n"+
						RequestReceivedBytesMetric+",400,,%s\n"+
						RequestReceivedBytesMetric+",7,,%s\n"+
						"vus,2,,\n", valid, valid, atLimit, overLimit, valid))
				all := collectAndGather(t, e)
				label := valid
				if budget == 0 {
					label = "others"
					assertReceivedBytes(t, all, map[string]float64{"others": 15})
				} else {
					assertReceivedBytes(t, all, map[string]float64{valid: 10, "others": 5})
				}
				assertRequestMetrics(t, all, map[string]requestMetrics{
					label: {count: 1, sum: 4, statuses: map[string]float64{"201": 1}},
				})
				assert.Equal(t, float64(2), single(t, all, metricschema.ThreadsGauge).GetGauge().GetValue())
			})
		}
	}
}

func TestReceivedBytesSourceSelectionIgnoresUnselectedSamples(t *testing.T) {
	for _, source := range []string{"", RequestReceivedBytesMetric} {
		t.Run(source, func(t *testing.T) {
			e, run := fixture(t, 1)
			require.NoError(t, e.ConfigureReceivedBytes(run, source))
			selected, ignored, label := DataReceivedMetric, RequestReceivedBytesMetric, "k6"
			if source == RequestReceivedBytesMetric {
				selected, ignored, label = source, DataReceivedMetric, "grouped/{id}"
			}
			putCSV(t, run, csvHeader+fmt.Sprintf("%s,NaN,,\n%s,-1,,\n%s,no,,\n", ignored, ignored, ignored))
			assert.Empty(t, collectAndGather(t, e))
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationPending, state)
			// Request and concurrency remain available without a selected byte sample.
			appendCSV(t, run, "http_req_duration,1,200,grouped/{id}\nvus,0,,\n")
			all := collectAndGather(t, e)
			assertReceivedBytes(t, all, nil)
			assertRequestMetrics(t, all, map[string]requestMetrics{
				"grouped/{id}": {count: 1, sum: 1, statuses: map[string]float64{"200": 1}},
			})
			appendCSV(t, run, fmt.Sprintf("%s,0,,grouped/{id}\n", selected))
			assertReceivedBytes(t, collectAndGather(t, e), map[string]float64{label: 0})
		})
	}
}

func TestCustomReceivedBytesInvalidSamplesFailWithoutMeasurements(t *testing.T) {
	cases := map[string]string{
		"empty-url":        RequestReceivedBytesMetric + ",1,,\n",
		"quoted-empty-url": RequestReceivedBytesMetric + ",1,,\"\"\n",
		"missing-url":      RequestReceivedBytesMetric + ",1,\n",
		"empty-value":      RequestReceivedBytesMetric + ",,,valid\n",
		"invalid-value":    RequestReceivedBytesMetric + ",no,,valid\n",
		"negative":         RequestReceivedBytesMetric + ",-1,,valid\n",
		"nan":              RequestReceivedBytesMetric + ",NaN,,valid\n",
		"infinity":         RequestReceivedBytesMetric + ",+Inf,,valid\n",
		"negative-inf":     RequestReceivedBytesMetric + ",-Inf,,valid\n",
		"overflow":         RequestReceivedBytesMetric + ",1e999,,valid\n",
	}
	for name, row := range cases {
		for _, budget := range []int{1, 0} {
			t.Run(fmt.Sprintf("%s/budget=%d", name, budget), func(t *testing.T) {
				e, run := fixture(t, budget)
				require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
				putCSV(t, run, csvHeader+row)
				err := e.Collect(context.Background(), run)
				require.Error(t, err)
				first := err
				appendCSV(t, run, RequestReceivedBytesMetric+",5,,valid\n")
				require.ErrorIs(t, e.Collect(context.Background(), run), first)
				state, err := e.Observe(context.Background(), run)
				require.ErrorIs(t, err, first)
				assert.Equal(t, protocol.ObservationPending, state)
				response := scrape(e)
				require.Equal(t, http.StatusOK, response.Code)
				assert.Empty(t, response.Body.String())
			})
		}
	}
}

func TestReceivedBytesResetRestoresDefaultForNewRun(t *testing.T) {
	e, run := fixture(t, 1)
	for index, custom := range []bool{true, false, true} {
		previous := run
		oldRegistry := previous.Metrics.Registry()
		oldMetrics := scrapeRegistry(oldRegistry).Body.String()
		run.RunID = fmt.Sprintf("run-%d", index)
		run.CollectionID = fmt.Sprintf("collection-%d", index)
		run.PlanID = fmt.Sprintf("plan-%d", index)
		run.AgentID = fmt.Sprintf("agent-%d", index)
		run.Zone = fmt.Sprintf("zone-%d", index)
		run.Metrics = nil
		e.Reset(run)
		require.Equal(t, oldMetrics, scrapeRegistry(oldRegistry).Body.String())
		e.launch(t, &run)
		label := fmt.Sprintf("https://example.test/run-%d", index)
		byteLabel := "k6"
		if custom {
			// Only callback run ID is required; the SDK binds identity at launch.
			require.NoError(t, e.ConfigureReceivedBytes(agent.RunContext{RunID: run.RunID}, RequestReceivedBytesMetric))
			byteLabel = label
		}
		staleSource := RequestReceivedBytesMetric
		if custom {
			staleSource = ""
		}
		require.ErrorContains(t, e.ConfigureReceivedBytes(previous, staleSource), "active run")
		putCSV(t, run, csvHeader+fmt.Sprintf(
			DataReceivedMetric+",11,,\n"+
				RequestReceivedBytesMetric+",7,,%s\n"+
				"http_req_duration,2,200,%s\n", label, label))
		all := collectAndGather(t, e)
		wantBytes := float64(11)
		if custom {
			wantBytes = 7
		}
		assertReceivedBytes(t, all, map[string]float64{byteLabel: wantBytes})
		assertRequestMetrics(t, all, map[string]requestMetrics{
			label: {count: 1, sum: 2, statuses: map[string]float64{"200": 1}},
		})
		for _, family := range all {
			for _, metric := range family.Metric {
				labels := metricLabels(metric)
				assert.Equal(t, run.RunID, labels["run_id"])
				assert.Equal(t, run.CollectionID, labels["collection_id"])
				assert.Equal(t, run.PlanID, labels["plan_id"])
				assert.Equal(t, run.AgentID, labels["engine_no"])
				assert.Equal(t, run.Zone, labels["zone"])
			}
		}
		require.Equal(t, oldMetrics, scrapeRegistry(oldRegistry).Body.String())
	}
}

func TestConfigureReceivedBytesRejectsUnknownAndUnpreparedRuns(t *testing.T) {
	exporter := &Exporter{}
	require.ErrorContains(t, exporter.ConfigureReceivedBytes(agent.RunContext{}, ""), "active run")
	for _, source := range []string{DataReceivedMetric, strings.ToUpper(DataReceivedMetric), " " + RequestReceivedBytesMetric, "custom_counter", "llm_streamed_bytes", metricschema.LLMTokenEventsMetric} {
		t.Run(source, func(t *testing.T) {
			e, run := fixture(t, 1)
			require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
			require.ErrorContains(t, e.ConfigureReceivedBytes(run, source), "unsupported received bytes source")
			putCSV(t, run, csvHeader+DataReceivedMetric+",99,,\n"+RequestReceivedBytesMetric+",3,,valid\n")
			assertReceivedBytes(t, collectAndGather(t, e), map[string]float64{"valid": 3})
		})
	}
}

func TestCustomReceivedBytesMeasuredZeroMarksReadiness(t *testing.T) {
	for _, budget := range []int{1, 0} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			e, run := fixture(t, budget)
			require.NoError(t, e.ConfigureReceivedBytes(run, RequestReceivedBytesMetric))
			putCSV(t, run, csvHeader+RequestReceivedBytesMetric+",0,,zero\n")
			all := collectAndGather(t, e)
			label := "zero"
			if budget == 0 {
				label = "others"
			}
			assertReceivedBytes(t, all, map[string]float64{label: 0})
			assert.NotContains(t, all, metricschema.RequestLatency)
			assert.NotContains(t, all, metricschema.StatusCounter)
			assert.NotContains(t, all, metricschema.ThreadsGauge)
			state, err := e.Observe(context.Background(), run)
			require.NoError(t, err)
			assert.Equal(t, protocol.ObservationReady, state)
		})
	}
}
