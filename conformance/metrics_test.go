package conformance

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/stretchr/testify/require"
)

const metricTestIdentity = `collection_id="collection",plan_id="plan",run_id="current",engine_no="7",zone="local"`

func metricFixture() (string, MetricsExpectation) {
	concurrency := float64(3)
	expected := MetricsExpectation{
		Identity: MetricIdentity{CollectionID: "collection", PlanID: "plan", RunID: "current", EngineNo: "7", Zone: "local"},
		Requests: map[string]RequestExpectation{
			"/a?x=1": {Statuses: map[string]uint64{"200": 3, "response timeout": 2}, LatenciesMS: []float64{1, 2.5, 12, 20000}},
			"/a?x=2": {Statuses: map[string]uint64{"500": 1}},
		},
		Bytes:       map[string]float64{"aggregate": 123.5},
		Concurrency: &concurrency,
		Resources:   ResourceExpectation{CPU: MeasurementRequired, Memory: MeasurementRequired},
	}
	var text strings.Builder
	text.WriteString("# TYPE shibuya_latency_label_milliseconds histogram\n")
	bounds := []string{"1", "2.5", "5", "10", "25", "50", "100", "250", "500", "1000", "2500", "5000", "10000", "+Inf"}
	counts := []uint64{1, 2, 2, 2, 3, 3, 3, 3, 3, 3, 3, 3, 3, 4}
	for i, bound := range bounds {
		fmt.Fprintf(&text, "shibuya_latency_label_milliseconds_bucket{%s,label=\"/a?x=1\",le=\"%s\"} %d\n", metricTestIdentity, bound, counts[i])
	}
	fmt.Fprintf(&text, "shibuya_latency_label_milliseconds_count{%s,label=\"/a?x=1\"} 4\n", metricTestIdentity)
	fmt.Fprintf(&text, "shibuya_latency_label_milliseconds_sum{%s,label=\"/a?x=1\"} 20015.5\n", metricTestIdentity)
	text.WriteString("# TYPE shibuya_status_counter counter\n")
	fmt.Fprintf(&text, "shibuya_status_counter{%s,label=\"/a?x=1\",status=\"200\"} 3\n", metricTestIdentity)
	fmt.Fprintf(&text, "shibuya_status_counter{%s,label=\"/a?x=1\",status=\"response timeout\"} 2\n", metricTestIdentity)
	fmt.Fprintf(&text, "shibuya_status_counter{%s,label=\"/a?x=2\",status=\"500\"} 1\n", metricTestIdentity)
	text.WriteString("# TYPE shibuya_egress_bytes_counter counter\n")
	fmt.Fprintf(&text, "shibuya_egress_bytes_counter{%s,label=\"aggregate\"} 123.5\n", metricTestIdentity)
	text.WriteString("# TYPE shibuya_threads_gauge gauge\n")
	fmt.Fprintf(&text, "shibuya_threads_gauge{%s} 3\n", metricTestIdentity)
	text.WriteString("# TYPE shibuya_cpu_gauge gauge\n")
	fmt.Fprintf(&text, "shibuya_cpu_gauge{%s} 0.25\n", metricTestIdentity)
	text.WriteString("# TYPE shibuya_mem_gauge gauge\n")
	fmt.Fprintf(&text, "shibuya_mem_gauge{%s} 4096\n", metricTestIdentity)
	return text.String(), expected
}

func TestMetricValidationIndependentMeasurements(t *testing.T) {
	text, expected := metricFixture()
	snapshot, err := validateMetrics(text, expected)
	require.NoError(t, err)
	require.Equal(t, expected.Identity, snapshot.Identity)
	require.Equal(t, expected.Bytes, snapshot.Bytes)
	require.Equal(t, expected.Concurrency, snapshot.Concurrency)
	require.Len(t, snapshot.Requests, 2)
	require.Equal(t, expected.Requests["/a?x=1"].Statuses, snapshot.Requests["/a?x=1"].Statuses)
	require.Equal(t, uint64(4), snapshot.Requests["/a?x=1"].Latency.Count)
	require.Equal(t, 20015.5, snapshot.Requests["/a?x=1"].Latency.Sum)
	require.Equal(t, uint64(4), snapshot.Requests["/a?x=1"].Latency.Buckets[math.Inf(1)])
	require.Nil(t, snapshot.Requests["/a?x=2"].Latency, "status-only failure must not require fabricated latency")

	changedResources := strings.ReplaceAll(strings.ReplaceAll(text, "} 0.25\n", "} 0.75\n"), "} 4096\n", "} 8192\n")
	next, err := validateMetrics(changedResources, expected)
	require.NoError(t, err)
	require.Equal(t, snapshot, next, "retained snapshots exclude changing resources")
}

func TestMetricValidationPerRequestBytesAndFoldedLabels(t *testing.T) {
	t.Run("result-event attribution", func(t *testing.T) {
		text, expected := metricFixture()
		text = strings.ReplaceAll(text, `label="aggregate"`, `label="/a?x=1"`)
		expected.Bytes = map[string]float64{"/a?x=1": 123.5, "/a?x=2": 0}
		text += fmt.Sprintf("shibuya_egress_bytes_counter{%s,label=\"/a?x=2\"} 0\n", metricTestIdentity)
		_, err := validateMetrics(text, expected)
		require.NoError(t, err)
	})
	t.Run("zero label budget", func(t *testing.T) {
		text, expected := metricFixture()
		text = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(text, "/a?x=1", "others"), "/a?x=2", "others"), "aggregate", "others")
		expected.Requests = map[string]RequestExpectation{"others": {
			Statuses: map[string]uint64{"200": 3, "response timeout": 2, "500": 1}, LatenciesMS: []float64{1, 2.5, 12, 20000},
		}}
		expected.Bytes = map[string]float64{"others": 123.5}
		_, err := validateMetrics(text, expected)
		require.NoError(t, err)
	})
}

func TestMetricValidationOptionalMeasurements(t *testing.T) {
	_, expected := metricFixture()
	expected.Requests, expected.Bytes, expected.Concurrency = nil, nil, nil
	expected.Resources = ResourceExpectation{CPU: MeasurementAbsent, Memory: MeasurementOptional}
	_, err := validateMetrics("", expected)
	require.NoError(t, err, "unavailable measurements are absent, not fabricated zeroes")

	expected.Resources.Memory = MeasurementRequired
	_, err = validateMetrics("", expected)
	require.ErrorContains(t, err, "required resource absent")

	text, full := metricFixture()
	full.Resources.CPU = MeasurementAbsent
	_, err = validateMetrics(text, full)
	require.ErrorContains(t, err, "resource must be absent")
}

func TestMetricValidationRejectsContractViolations(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"collection identity", `collection_id="collection"`, `collection_id="other"`},
		{"plan identity", `plan_id="plan"`, `plan_id="other"`},
		{"stale run", `run_id="current"`, `run_id="old"`},
		{"engine identity", `engine_no="7"`, `engine_no="8"`},
		{"zone identity", `zone="local"`, `zone="other"`},
		{"missing label", `,zone="local"`, ""},
		{"extra label", `zone="local"`, `zone="local",extra="value"`},
		{"duplicate label", `zone="local"`, `zone="local",zone="local"`},
		{"wrong family type", "shibuya_status_counter counter", "shibuya_status_counter gauge"},
		{"unexpected request attribution", `/a?x=1`, "engine-wide"},
		{"collapsed query attribution", `/a?x=2`, "/a?x=1"},
		{"unexpected status", `status="500"`, `status="unknown"`},
		{"wrong status count", `status="500"} 1`, `status="500"} 2`},
		{"fractional status count", `status="500"} 1`, `status="500"} 1.5`},
		{"nonfinite status count", `status="500"} 1`, `status="500"} NaN`},
		{"unexpected per-request bytes", `label="aggregate"`, `label="/a?x=1"`},
		{"wrong bytes", "} 123.5\n", "} 120\n"},
		{"negative bytes", "} 123.5\n", "} -1\n"},
		{"infinite bytes", "} 123.5\n", "} +Inf\n"},
		{"wrong bucket bound", `le="2.5"`, `le="2.6"`},
		{"wrong bucket count", `le="2.5"} 2`, `le="2.5"} 1`},
		{"wrong infinite bucket", `le="+Inf"} 4`, `le="+Inf"} 3`},
		{"wrong histogram count", `label="/a?x=1"} 4`, `label="/a?x=1"} 5`},
		{"wrong histogram sum", "} 20015.5\n", "} 20016\n"},
		{"nonfinite histogram sum", "} 20015.5\n", "} NaN\n"},
		{"negative CPU", "} 0.25\n", "} -1\n"},
		{"nonfinite CPU", "} 0.25\n", "} +Inf\n"},
		{"zero memory", "} 4096\n", "} 0\n"},
		{"nonfinite memory", "} 4096\n", "} NaN\n"},
		{"request-labelled CPU", "shibuya_cpu_gauge{" + metricTestIdentity + "}", "shibuya_cpu_gauge{" + metricTestIdentity + `,label="request"}`},
		{"wrong concurrency", "shibuya_threads_gauge{" + metricTestIdentity + "} 3", "shibuya_threads_gauge{" + metricTestIdentity + "} 2"},
		{"nonfinite concurrency", "shibuya_threads_gauge{" + metricTestIdentity + "} 3", "shibuya_threads_gauge{" + metricTestIdentity + "} NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, expected := metricFixture()
			require.Contains(t, text, tc.old)
			_, err := validateMetrics(strings.ReplaceAll(text, tc.old, tc.new), expected)
			require.Error(t, err)
		})
	}
}

func TestMetricValidationRejectsMissingAndDuplicateSeries(t *testing.T) {
	for _, prefix := range []string{
		"shibuya_latency_label_milliseconds_bucket{", "shibuya_latency_label_milliseconds_count{", "shibuya_latency_label_milliseconds_sum{",
		"shibuya_status_counter{", "shibuya_egress_bytes_counter{", "shibuya_threads_gauge{", "shibuya_cpu_gauge{", "shibuya_mem_gauge{",
	} {
		t.Run(prefix, func(t *testing.T) {
			text, expected := metricFixture()
			var selected string
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, prefix) {
					selected = line + "\n"
					break
				}
			}
			require.NotEmpty(t, selected)
			_, err := validateMetrics(strings.Replace(text, selected, "", 1), expected)
			require.Error(t, err, "missing measurement")
			_, err = validateMetrics(text+selected, expected)
			require.Error(t, err, "duplicate measurement")
		})
	}
	text, expected := metricFixture()
	_, err := validateMetrics(text+"# TYPE shibuya_unrecognized gauge\nshibuya_unrecognized 1\n", expected)
	require.ErrorContains(t, err, "unexpected schema family")
	_, err = validateMetrics(text+"not valid exposition\n", expected)
	require.Error(t, err)
}

func TestMetricValidationExplicitPriorIdentity(t *testing.T) {
	text, expected := metricFixture()
	current, err := validateMetrics(text, expected)
	require.NoError(t, err)
	var retained strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			retained.WriteString(strings.ReplaceAll(line, `run_id="current"`, `run_id="prior"`) + "\n")
		}
	}
	_, err = validateMetrics(text+retained.String(), expected)
	require.Error(t, err, "native producers reject retained identities by default")
	prior := expected.Identity
	prior.RunID = "prior"
	expected.AllowedPriorIdentities = []MetricIdentity{prior}
	withPrior, err := validateMetrics(text+retained.String(), expected)
	require.NoError(t, err)
	require.Equal(t, current, withPrior, "prior-run totals and resources must not enter the current snapshot")

	_, err = validateMetrics(text+strings.ReplaceAll(retained.String(), `engine_no="7"`, `engine_no="8"`), expected)
	require.Error(t, err, "allowing a prior run does not permit arbitrary identities")
	_, err = validateMetrics(text+strings.ReplaceAll(retained.String(), "} 0.25\n", "} NaN\n"), expected)
	require.Error(t, err, "retained measurements must still be finite")
	withoutCurrentCPU := strings.ReplaceAll(text, "shibuya_cpu_gauge{"+metricTestIdentity+"} 0.25\n", "")
	_, err = validateMetrics(withoutCurrentCPU+retained.String(), expected)
	require.ErrorContains(t, err, "required resource absent", "prior resources do not satisfy current-run requirements")
}

func TestMetricValidationAbsentMeasurementsRejectFabrication(t *testing.T) {
	for _, measurement := range []string{"latency", "bytes", "concurrency"} {
		t.Run(measurement, func(t *testing.T) {
			text, expected := metricFixture()
			switch measurement {
			case "latency":
				request := expected.Requests["/a?x=1"]
				request.LatenciesMS = nil
				expected.Requests["/a?x=1"] = request
			case "bytes":
				expected.Bytes = nil
			case "concurrency":
				expected.Concurrency = nil
			}
			_, err := validateMetrics(text, expected)
			require.Error(t, err)
		})
	}
}

func TestMetricValidationIgnoresExpositionOrderAndUnrelatedCollectors(t *testing.T) {
	text, expected := metricFixture()
	first, err := validateMetrics(text, expected)
	require.NoError(t, err)
	var reordered strings.Builder
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			reordered.WriteString(line + "\n")
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], "#") {
			reordered.WriteString(lines[i] + "\n")
		}
	}
	reordered.WriteString("# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds 12345\n")
	second, err := validateMetrics(reordered.String(), expected)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// These fixtures deliberately state both SDK profiles independently of the
// validator, so changing its accepted bounds cannot silently change the oracle.
func latencyMetricFixture(name, identity, label string, observations []float64) string {
	bounds := []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, math.Inf(1)}
	if name == metricschema.LLMResponseLatency {
		bounds = []float64{100, 250, 500, 1000, 2500, 5000, 7500, 10000, 15000, 20000, 30000, 45000, 60000, 90000, 120000, 180000, 300000, math.Inf(1)}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "# TYPE %s histogram\n", name)
	for _, bound := range bounds {
		var count int
		for _, observation := range observations {
			if observation <= bound {
				count++
			}
		}
		fmt.Fprintf(&text, "%s_bucket{%s,label=%q,le=%q} %d\n", name, identity, label, fmt.Sprint(bound), count)
	}
	var sum float64
	for _, observation := range observations {
		sum += observation
	}
	fmt.Fprintf(&text, "%s_count{%s,label=%q} %d\n", name, identity, label, len(observations))
	fmt.Fprintf(&text, "%s_sum{%s,label=%q} %g\n", name, identity, label, sum)
	return text.String()
}

func TestMetricValidationRetainedHistogramStructure(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		t.Run(family, func(t *testing.T) {
			text, expected := metricFixture()
			current, err := validateMetrics(text, expected)
			require.NoError(t, err)
			prior := expected.Identity
			prior.RunID = "prior"
			expected.AllowedPriorIdentities = []MetricIdentity{prior}
			identity := strings.ReplaceAll(metricTestIdentity, `run_id="current"`, `run_id="prior"`)
			label := "old"
			if family == metricschema.LLMResponseLatency {
				label = "llm_resp"
			}
			retained := latencyMetricFixture(family, identity, label, []float64{0, 200, 400000})
			// The ordinary family already has a TYPE in the current fixture.
			retained = strings.TrimPrefix(retained, "# TYPE "+family+" histogram\n")
			header := ""
			first, second := "1", "2.5"
			if family == metricschema.LLMResponseLatency {
				header = "# TYPE " + family + " histogram\n"
				first, second = "100", "250"
			}
			got, err := validateMetrics(text+header+retained, expected)
			require.NoError(t, err, "retained observations need not match current observations")
			require.Equal(t, current, got)

			firstBucket := fmt.Sprintf("%s_bucket{%s,label=%q,le=%q} 1\n", family, identity, label, first)
			sum := fmt.Sprintf("%s_sum{%s,label=%q} 400200\n", family, identity, label)
			count := fmt.Sprintf("%s_count{%s,label=%q} 3\n", family, identity, label)
			cases := []struct {
				name string
				old  string
				new  string
			}{
				{"wrong bound", `le="` + first + `"`, `le="0.5"`},
				{"duplicate bound", `le="` + second + `"`, `le="` + first + `"`},
				{"missing bucket", firstBucket, ""},
				{"duplicate bucket", firstBucket, firstBucket + firstBucket},
				{"decreasing counts", firstBucket, strings.ReplaceAll(firstBucket, "} 1\n", "} 3\n")},
				{"infinite bucket mismatch", `le="+Inf"} 3`, `le="+Inf"} 2`},
				{"missing infinite bucket", fmt.Sprintf("%s_bucket{%s,label=%q,le=\"+Inf\"} 3\n", family, identity, label), ""},
				{"missing sum", sum, ""},
				{"negative sum", sum, strings.ReplaceAll(sum, "400200", "-1")},
				{"NaN sum", sum, strings.ReplaceAll(sum, "400200", "NaN")},
				{"infinite sum", sum, strings.ReplaceAll(sum, "400200", "+Inf")},
				{"missing count", count, ""},
				{"negative count", count, strings.ReplaceAll(count, "} 3\n", "} -1\n")},
				{"fractional count", count, strings.ReplaceAll(count, "} 3\n", "} 3.5\n")},
				{"NaN count", count, strings.ReplaceAll(count, "} 3\n", "} NaN\n")},
				{"infinite count", count, strings.ReplaceAll(count, "} 3\n", "} +Inf\n")},
				{"negative bucket", firstBucket, strings.ReplaceAll(firstBucket, "} 1\n", "} -1\n")},
				{"fractional bucket", firstBucket, strings.ReplaceAll(firstBucket, "} 1\n", "} 1.5\n")},
				{"NaN bucket", firstBucket, strings.ReplaceAll(firstBucket, "} 1\n", "} NaN\n")},
				{"infinite bucket", firstBucket, strings.ReplaceAll(firstBucket, "} 1\n", "} +Inf\n")},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					require.Contains(t, retained, tc.old)
					_, err := validateMetrics(text+header+strings.ReplaceAll(retained, tc.old, tc.new), expected)
					require.Error(t, err)
				})
			}
			zero := latencyMetricFixture(family, identity, label, nil)
			zero = strings.TrimPrefix(zero, "# TYPE "+family+" histogram\n")
			got, err = validateMetrics(text+header+zero, expected)
			require.NoError(t, err)
			require.Equal(t, current, got)
		})
	}
}

func TestMetricValidationLLMResponseLatency(t *testing.T) {
	text, expected := metricFixture()
	observations := []float64{0, 100, 7500, 300000, 400000}
	llm := latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "llm_resp", observations)
	expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: observations}
	snapshot, err := validateMetrics(text+llm, expected)
	require.NoError(t, err)
	require.Nil(t, snapshot.Requests["llm_resp"].Latency)
	require.Empty(t, snapshot.Requests["llm_resp"].Statuses)
	latency := snapshot.Requests["llm_resp"].LLMResponseLatency
	require.Equal(t, uint64(5), latency.Count)
	require.Equal(t, 707600.0, latency.Sum)
	require.Len(t, latency.Buckets, 18)
	require.Equal(t, uint64(3), latency.Buckets[7500])
	require.Equal(t, uint64(4), latency.Buckets[300000])
	require.Equal(t, uint64(5), latency.Buckets[math.Inf(1)])
	require.Nil(t, snapshot.Requests["/a?x=1"].LLMResponseLatency)

	prior := expected.Identity
	prior.RunID = "prior"
	expected.AllowedPriorIdentities = []MetricIdentity{prior}
	priorIdentity := strings.ReplaceAll(metricTestIdentity, `run_id="current"`, `run_id="prior"`)
	retained := latencyMetricFixture(metricschema.LLMResponseLatency, priorIdentity, "llm_resp", []float64{20000})
	retained = strings.TrimPrefix(retained, "# TYPE "+metricschema.LLMResponseLatency+" histogram\n")
	withPrior, err := validateMetrics(text+llm+retained, expected)
	require.NoError(t, err)
	require.Equal(t, snapshot, withPrior, "retained LLM observations must not enter current snapshot")
	_, err = validateMetrics(text+"# TYPE "+metricschema.LLMResponseLatency+" histogram\n"+retained, expected)
	require.ErrorContains(t, err, "missing LLM response latency histogram", "retained observations must not satisfy current expectations")

	// Both histograms may independently be present for a folded label.
	ordinary := strings.ReplaceAll(text, `label="/a?x=1"`, `label="others"`)
	both := strings.ReplaceAll(llm, `label="llm_resp"`, `label="others"`)
	request := expected.Requests["/a?x=1"]
	request.LLMResponseLatenciesMS = observations
	expected.Requests["others"] = request
	delete(expected.Requests, "/a?x=1")
	delete(expected.Requests, "llm_resp")
	text = ordinary
	snapshot, err = validateMetrics(text+both, expected)
	require.NoError(t, err)
	require.Equal(t, uint64(4), snapshot.Requests["others"].Latency.Count)
	require.Equal(t, uint64(5), snapshot.Requests["others"].LLMResponseLatency.Count)

	request.LLMResponseLatenciesMS = []float64{}
	expected.Requests["others"] = request
	zero := latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "others", nil)
	snapshot, err = validateMetrics(text+zero, expected)
	require.NoError(t, err)
	require.Zero(t, snapshot.Requests["others"].LLMResponseLatency.Count)
	require.Zero(t, snapshot.Requests["others"].LLMResponseLatency.Sum)
	_, err = validateMetrics(text, expected)
	require.ErrorContains(t, err, "missing LLM response latency histogram")
	request.LLMResponseLatenciesMS = nil
	expected.Requests["others"] = request
	_, err = validateMetrics(text+zero, expected)
	require.ErrorContains(t, err, "unexpected or duplicate histogram")
}

func TestMetricValidationLLMResponseFamilyLabels(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		for _, retained := range []bool{false, true} {
			for _, label := range []string{"llm_resp", "others", "_other_", "/url", "LLM_RESP", "Llm_Resp", "ttft", "Others", "_OTHER_"} {
				t.Run(fmt.Sprintf("%s/retained=%t/%s", family, retained, label), func(t *testing.T) {
					_, fixture := metricFixture()
					expected := MetricsExpectation{Identity: fixture.Identity}
					identity := metricTestIdentity
					if retained {
						prior := expected.Identity
						prior.RunID = "prior"
						expected.AllowedPriorIdentities = []MetricIdentity{prior}
						identity = strings.ReplaceAll(identity, `run_id="current"`, `run_id="prior"`)
					} else {
						request := RequestExpectation{LatenciesMS: []float64{100}}
						if family == metricschema.LLMResponseLatency {
							request = RequestExpectation{LLMResponseLatenciesMS: []float64{100}}
						}
						// Matching expectations must not legitimize a wrong-family label.
						expected.Requests = map[string]RequestExpectation{label: request}
					}
					text := latencyMetricFixture(family, identity, label, []float64{100})
					snapshot, err := validateMetrics(text, expected)
					if family == metricschema.LLMResponseLatency && label != "llm_resp" && label != "others" && label != "_other_" {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					if retained {
						require.Empty(t, snapshot.Requests)
					} else if family == metricschema.LLMResponseLatency {
						require.Equal(t, uint64(1), snapshot.Requests[label].LLMResponseLatency.Count)
						require.Nil(t, snapshot.Requests[label].Latency)
					} else {
						require.Equal(t, uint64(1), snapshot.Requests[label].Latency.Count)
						require.Nil(t, snapshot.Requests[label].LLMResponseLatency)
					}
				})
			}
		}
	}
}

func TestMetricValidationLLMRejectsInvalidCurrentMeasurements(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"wrong profile", `le="7500"`, `le="8000"`},
		{"wrong observations", `le="7500"} 3`, `le="7500"} 2`},
		{"decreasing counts", `le="100"} 2`, `le="100"} 4`},
		{"wrong infinite bucket", `le="+Inf"} 5`, `le="+Inf"} 4`},
		{"wrong sum", "} 707600\n", "} 707601\n"},
		{"negative sum", "} 707600\n", "} -1\n"},
		{"nonfinite sum", "} 707600\n", "} +Inf\n"},
		{"wrong count", `label="llm_resp"} 5`, `label="llm_resp"} 4`},
		{"fractional count", `label="llm_resp"} 5`, `label="llm_resp"} 5.5`},
		{"nonfinite count", `label="llm_resp"} 5`, `label="llm_resp"} NaN`},
		{"wrong label", `label="llm_resp"`, `label="unknown"`},
		{"extra label", `label="llm_resp"`, `label="llm_resp",extra="invalid"`},
		{"wrong identity", `zone="local"`, `zone="other"`},
		{"wrong type", " histogram\n", " gauge\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, expected := metricFixture()
			observations := []float64{0, 100, 7500, 300000, 400000}
			llm := latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "llm_resp", observations)
			expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: observations}
			require.Contains(t, llm, tc.old)
			_, err := validateMetrics(text+strings.ReplaceAll(llm, tc.old, tc.new), expected)
			require.Error(t, err)
		})
	}
	text, expected := metricFixture()
	llm := latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "llm_resp", []float64{100})
	_, err := validateMetrics(text+llm, expected)
	require.ErrorContains(t, err, "unexpected request label")
	expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: []float64{100}}
	ordinary := latencyMetricFixture(metricschema.RequestLatency, metricTestIdentity, "llm_resp", []float64{100})
	ordinary = strings.ReplaceAll(ordinary, metricschema.RequestLatency, metricschema.LLMResponseLatency)
	_, err = validateMetrics(text+ordinary, expected)
	require.ErrorContains(t, err, "bucket count")
	for _, observation := range []float64{-1, math.NaN(), math.Inf(1)} {
		expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: []float64{observation}}
		_, err = validateMetrics(text+llm, expected)
		require.ErrorContains(t, err, "invalid expected latency")
	}
}

func TestMetricValidationDuplicateSeriesAcrossIdentities(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, reordered := range []bool{false, true} {
			for _, family := range []string{
				metricschema.RequestLatency, metricschema.LLMResponseLatency, metricschema.StatusCounter,
				metricschema.EgressBytesCounter, metricschema.ThreadsGauge, metricschema.CPUGauge, metricschema.MemoryGauge,
			} {
				t.Run(fmt.Sprintf("%s/retained=%t/reordered=%t", family, retained, reordered), func(t *testing.T) {
					text, expected := metricFixture()
					text += latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "llm_resp", []float64{100})
					expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: []float64{100}}
					var selected strings.Builder
					for _, line := range strings.Split(text, "\n") {
						if strings.HasPrefix(line, family+"{") || strings.HasPrefix(line, family+"_") {
							selected.WriteString(line + "\n")
						}
					}
					series := selected.String()
					require.NotEmpty(t, series)
					if retained {
						prior := expected.Identity
						prior.RunID = "prior"
						expected.AllowedPriorIdentities = []MetricIdentity{prior}
						series = strings.ReplaceAll(series, `run_id="current"`, `run_id="prior"`)
						text += series
					}
					_, err := validateMetrics(text, expected)
					require.NoError(t, err, "one series per complete identity is valid")
					if reordered {
						series = strings.ReplaceAll(series, `collection_id="collection",plan_id="plan"`, `plan_id="plan",collection_id="collection"`)
					}
					_, err = validateMetrics(text+series, expected)
					require.Error(t, err, "duplicate series must fail regardless of label order or retention")
				})
			}
		}
	}
}

func TestMetricValidationDuplicateHistogramComponents(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		for _, retained := range []bool{false, true} {
			for _, component := range []string{"_count", "_sum", "_bucket"} {
				for _, reordered := range []bool{false, true} {
					for _, quoted := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s%s/retained=%t/reordered=%t/quoted=%t", family, component, retained, reordered, quoted), func(t *testing.T) {
							// Delimiters and escaped quotes/newlines must remain label
							// content, not be mistaken for sample or label boundaries.
							label := "request,} # TYPE \"quoted\" \\\nnext"
							otherLabel := label + ",other"
							request := RequestExpectation{LatenciesMS: []float64{100}}
							if family == metricschema.LLMResponseLatency {
								label, otherLabel = "llm_resp", "others"
								request = RequestExpectation{LLMResponseLatenciesMS: []float64{100}}
							}
							expected := MetricsExpectation{
								Identity: MetricIdentity{CollectionID: "collection", PlanID: "plan", RunID: "current", EngineNo: "7", Zone: "local"},
								Requests: map[string]RequestExpectation{label: request, otherLabel: request},
							}
							header := fmt.Sprintf("# TYPE %s histogram\n", family)
							text := latencyMetricFixture(family, metricTestIdentity, label, []float64{100})
							text += strings.TrimPrefix(latencyMetricFixture(family, metricTestIdentity, otherLabel, []float64{100}), header)
							identity := metricTestIdentity
							if retained {
								prior := expected.Identity
								prior.RunID = "prior"
								expected.AllowedPriorIdentities = []MetricIdentity{prior}
								identity = strings.ReplaceAll(identity, `run_id="current"`, `run_id="prior"`)
								text += strings.TrimPrefix(latencyMetricFixture(family, identity, label, []float64{100}), header)
							}
							labels := fmt.Sprintf("%s,label=%q", identity, label)
							value := "1"
							if component == "_sum" {
								value = "100"
							} else if component == "_bucket" {
								labels += `,le="+Inf"`
							}
							sample := fmt.Sprintf("%s%s{%s} %s\n", family, component, labels, value)
							originalSample := sample
							// Comments resembling samples do not create identities.
							text += "# " + sample
							if reordered {
								labels = strings.ReplaceAll(labels, `collection_id="collection",plan_id="plan"`, `plan_id="plan",collection_id="collection"`)
							}
							sample = fmt.Sprintf("%s%s{%s} %s\n", family, component, labels, value)
							if quoted {
								// Prometheus also permits the quoted metric name
								// inside the braces, after label assignments.
								sample = fmt.Sprintf("{%s,%q} %s\n", labels, family+component, value)
							}
							text = strings.Replace(text, originalSample, sample, 1)
							_, err := validateMetrics(text, expected)
							require.NoError(t, err, "distinct requests, runs and histogram components are valid")
							_, err = validateMetrics(text+sample, expected)
							require.Error(t, err, "even a single repeated component must be rejected")
						})
					}
				}
			}
		}
	}
}

func TestMetricValidationDistinctRetainedSeries(t *testing.T) {
	text, expected := metricFixture()
	text += latencyMetricFixture(metricschema.LLMResponseLatency, metricTestIdentity, "llm_resp", []float64{100})
	expected.Requests["llm_resp"] = RequestExpectation{LLMResponseLatenciesMS: []float64{100}}
	current, err := validateMetrics(text, expected)
	require.NoError(t, err)
	var samples strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			samples.WriteString(line + "\n")
		}
	}
	prior := expected.Identity
	prior.RunID = "prior"
	identities := []MetricIdentity{prior, prior, prior, prior, prior, prior}
	identities[1].CollectionID = "other collection"
	identities[2].PlanID = "other plan"
	identities[3].RunID = "another prior run"
	identities[4].EngineNo = "8"
	identities[5].Zone = "other zone"
	expected.AllowedPriorIdentities = identities
	for _, identity := range identities {
		labels := fmt.Sprintf("collection_id=%q,plan_id=%q,run_id=%q,engine_no=%q,zone=%q",
			identity.CollectionID, identity.PlanID, identity.RunID, identity.EngineNo, identity.Zone)
		text += strings.ReplaceAll(samples.String(), metricTestIdentity, labels)
		// The same request label may have independent statuses, including a
		// status that happens to equal another request's label.
		text += fmt.Sprintf("%s{%s,label=\"/a?x=1\",status=\"/a?x=2\"} 2\n", metricschema.StatusCounter, labels)
		text += fmt.Sprintf("%s{%s,label=\"/a?x=2\",status=\"/a?x=1\"} 2\n", metricschema.StatusCounter, labels)
		text += fmt.Sprintf("%s{%s,label=\"other request\",status=\"200\"} 2\n", metricschema.StatusCounter, labels)
	}
	got, err := validateMetrics(text, expected)
	require.NoError(t, err, "family, full identity, request and status all distinguish series")
	require.Equal(t, current, got)
}

func TestMetricValidationZeroCountHistogramSum(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%t", family, retained), func(t *testing.T) {
				_, fixture := metricFixture()
				expected := MetricsExpectation{Identity: fixture.Identity}
				identity := metricTestIdentity
				label := "zero"
				if family == metricschema.LLMResponseLatency {
					label = "llm_resp"
				}
				if retained {
					prior := expected.Identity
					prior.RunID = "prior"
					expected.AllowedPriorIdentities = []MetricIdentity{prior}
					identity = strings.ReplaceAll(identity, `run_id="current"`, `run_id="prior"`)
				} else {
					request := RequestExpectation{LatenciesMS: []float64{}}
					if family == metricschema.LLMResponseLatency {
						request = RequestExpectation{LLMResponseLatenciesMS: []float64{}}
					}
					expected.Requests = map[string]RequestExpectation{label: request}
				}
				text := latencyMetricFixture(family, identity, label, nil)
				_, err := validateMetrics(text, expected)
				require.NoError(t, err, "a genuinely empty histogram is valid")
				sumLine := fmt.Sprintf("%s_sum{%s,label=%q} 0\n", family, identity, label)
				require.Contains(t, text, sumLine)
				for _, sum := range []float64{math.SmallestNonzeroFloat64, 0.0000001, 1} {
					badSum := fmt.Sprintf("%s_sum{%s,label=%q} %g\n", family, identity, label, sum)
					_, err := validateMetrics(strings.Replace(text, sumLine, badSum, 1), expected)
					require.ErrorContains(t, err, "zero-count histogram has nonzero sum", "sum %g", sum)
				}
			})
		}
	}
}

func TestMetricValidationRequestLabelByteLimit(t *testing.T) {
	labels := []struct {
		name  string
		value string
		valid bool
	}{
		{"ASCII 1023", strings.Repeat("x", 1023), true},
		{"ASCII 1024", strings.Repeat("x", 1024), false},
		{"ASCII 1025", strings.Repeat("x", 1025), false},
		{"Unicode 1023", strings.Repeat("界", 341), true},
		{"Unicode 1024", strings.Repeat("界", 341) + "x", false},
		{"Unicode 1025", strings.Repeat("界", 341) + "é", false},
	}
	// LLM labels have a closed reserved/folded vocabulary, tested separately;
	// arbitrary boundary-length labels are valid only in the other families.
	for _, family := range []string{
		metricschema.RequestLatency, metricschema.StatusCounter, metricschema.EgressBytesCounter,
	} {
		for _, retained := range []bool{false, true} {
			for _, label := range labels {
				t.Run(fmt.Sprintf("%s/retained=%t/%s", family, retained, label.name), func(t *testing.T) {
					_, fixture := metricFixture()
					expected := MetricsExpectation{Identity: fixture.Identity}
					identity := metricTestIdentity
					if retained {
						prior := expected.Identity
						prior.RunID = "prior"
						expected.AllowedPriorIdentities = []MetricIdentity{prior}
						identity = strings.ReplaceAll(identity, `run_id="current"`, `run_id="prior"`)
					}
					var text string
					request := RequestExpectation{}
					switch family {
					case metricschema.RequestLatency:
						request.LatenciesMS = []float64{1}
						text = latencyMetricFixture(family, identity, label.value, request.LatenciesMS)
					case metricschema.StatusCounter:
						request.Statuses = map[string]uint64{"200": 1}
						text = fmt.Sprintf("# TYPE %s counter\n%s{%s,label=%q,status=\"200\"} 1\n", family, family, identity, label.value)
					case metricschema.EgressBytesCounter:
						text = fmt.Sprintf("# TYPE %s counter\n%s{%s,label=%q} 1\n", family, family, identity, label.value)
					}
					if !retained {
						if family == metricschema.EgressBytesCounter {
							expected.Bytes = map[string]float64{label.value: 1}
						} else {
							expected.Requests = map[string]RequestExpectation{label.value: request}
						}
					}
					snapshot, err := validateMetrics(text, expected)
					if !label.valid {
						require.ErrorContains(t, err, "request label length")
						return
					}
					require.NoError(t, err)
					if retained {
						require.Empty(t, snapshot.Requests)
						require.Empty(t, snapshot.Bytes)
					} else if family == metricschema.EgressBytesCounter {
						require.Equal(t, map[string]float64{label.value: 1}, snapshot.Bytes)
					} else {
						require.Len(t, snapshot.Requests, 1)
						require.Contains(t, snapshot.Requests, label.value, "accepted labels retain every emitted byte")
					}
				})
			}
		}
	}
}

func TestMetricValidationResourceIdentityLabelsHaveNoRequestByteLimit(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained=%t", retained), func(t *testing.T) {
			_, fixture := metricFixture()
			expected := MetricsExpectation{Identity: fixture.Identity}
			identity := expected.Identity
			identity.Zone = strings.Repeat("界", 400)
			if retained {
				identity.RunID = "prior"
				expected.AllowedPriorIdentities = []MetricIdentity{identity}
			} else {
				expected.Identity = identity
				expected.Resources = ResourceExpectation{CPU: MeasurementRequired, Memory: MeasurementRequired}
			}
			labels := fmt.Sprintf("collection_id=%q,plan_id=%q,run_id=%q,engine_no=%q,zone=%q",
				identity.CollectionID, identity.PlanID, identity.RunID, identity.EngineNo, identity.Zone)
			text := fmt.Sprintf("# TYPE %s gauge\n%s{%s} 0.25\n# TYPE %s gauge\n%s{%s} 4096\n",
				metricschema.CPUGauge, metricschema.CPUGauge, labels, metricschema.MemoryGauge, metricschema.MemoryGauge, labels)
			_, err := validateMetrics(text, expected)
			require.NoError(t, err)
		})
	}
}

func TestMetricValidationRawHistogramShapes(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		for _, retained := range []bool{false, true} {
			_, fixture := metricFixture()
			expected := MetricsExpectation{Identity: fixture.Identity}
			identity := metricTestIdentity
			if retained {
				prior := expected.Identity
				prior.RunID = "prior"
				expected.AllowedPriorIdentities = []MetricIdentity{prior}
				identity = strings.ReplaceAll(identity, `run_id="current"`, `run_id="prior"`)
			} else {
				request := RequestExpectation{LatenciesMS: []float64{}}
				if family == metricschema.LLMResponseLatency {
					request = RequestExpectation{LLMResponseLatenciesMS: []float64{}}
				}
				expected.Requests = map[string]RequestExpectation{"llm_resp": request}
			}
			text := latencyMetricFixture(family, identity, "llm_resp", nil)
			labels := identity + `,label="llm_resp"`
			cases := map[string]string{
				"base sample":          text + fmt.Sprintf("%s{%s} 7\n", family, labels),
				"bucket without bound": text + fmt.Sprintf("%s_bucket{%s} 7\n", family, labels),
				"sum with bound":       strings.Replace(text, family+"_sum{"+labels+"}", family+"_sum{"+labels+`,le="1"}`, 1),
				"count with bound":     strings.Replace(text, family+"_count{"+labels+"}", family+"_count{"+labels+`,le="1"}`, 1),
			}
			for name, malformed := range cases {
				t.Run(fmt.Sprintf("%s/retained=%t/%s", family, retained, name), func(t *testing.T) {
					_, err := validateMetrics(text, expected)
					require.NoError(t, err)
					_, err = validateMetrics(malformed, expected)
					require.Error(t, err)
					// Verify valid quoted exposition too, so syntax rejection
					// cannot masquerade as component validation.
					quoteNames := strings.NewReplacer(
						family+"{", `"`+family+`"{`,
						family+"_bucket{", `"`+family+`_bucket"{`,
						family+"_sum{", `"`+family+`_sum"{`,
						family+"_count{", `"`+family+`_count"{`,
					)
					_, err = validateMetrics(quoteNames.Replace(text), expected)
					require.NoError(t, err)
					_, err = validateMetrics(quoteNames.Replace(malformed), expected)
					require.Error(t, err)
				})
			}
		}
	}
}

func TestMetricValidationRawHistogramCountRange(t *testing.T) {
	for _, family := range []string{metricschema.RequestLatency, metricschema.LLMResponseLatency} {
		for _, test := range []struct {
			name, value string
			valid       bool
		}{
			{"zero", "0", true},
			{"one", "1", true},
			{"largest representable below limit", fmt.Sprintf("%.0f", math.Nextafter(0x1p64, 0)), true},
			{"limit", "18446744073709551616", false},
			{"above limit", "36893488147419103232", false},
			{"negative", "-1", false},
			{"fractional", "1.5", false},
			{"infinite", "+Inf", false},
			{"not a number", "NaN", false},
		} {
			t.Run(family+"/"+test.name, func(t *testing.T) {
				_, fixture := metricFixture()
				prior := fixture.Identity
				prior.RunID = "prior"
				expected := MetricsExpectation{Identity: fixture.Identity, AllowedPriorIdentities: []MetricIdentity{prior}}
				identity := strings.ReplaceAll(metricTestIdentity, `run_id="current"`, `run_id="prior"`)
				text := latencyMetricFixture(family, identity, "llm_resp", nil)
				var samples strings.Builder
				for line := range strings.Lines(text) {
					if strings.HasPrefix(line, family+"_bucket{") || strings.HasPrefix(line, family+"_count{") {
						line = strings.TrimSuffix(line, " 0\n") + " " + test.value + "\n"
					}
					samples.WriteString(line)
				}
				_, err := validateMetrics(samples.String(), expected)
				if test.valid {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}
