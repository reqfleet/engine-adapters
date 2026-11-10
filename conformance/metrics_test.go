package conformance

import (
	"fmt"
	"math"
	"strings"
	"testing"

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
			// The Prometheus parser coalesces repeated histogram sums/counts;
			// buckets and independent series remain available for duplicate checks.
			if !strings.Contains(prefix, "_count{") && !strings.Contains(prefix, "_sum{") {
				_, err = validateMetrics(text+selected, expected)
				require.Error(t, err, "duplicate measurement")
			}
		})
	}
	text, expected := metricFixture()
	_, err := validateMetrics(text+"# TYPE shibuya_unrecognized gauge\nshibuya_unrecognized 1\n", expected)
	require.ErrorContains(t, err, "unexpected schema family")
	_, err = validateMetrics(text+"not valid exposition\n", expected)
	require.ErrorContains(t, err, "parse exposition")
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
