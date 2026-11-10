package conformance

import (
	"fmt"
	"math"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/reqfleet/engine-sdk/metricschema"
)

// MetricIdentity is the complete run and agent identity on every schema series.
type MetricIdentity struct {
	CollectionID string
	PlanID       string
	RunID        string
	EngineNo     string
	Zone         string
}

// RequestExpectation describes independently available request measurements.
// Nil LatenciesMS requires an absent histogram; a non-nil empty slice expects
// an explicitly reported zero-observation histogram. Status counts need not
// equal latency observations: a failed request may have no measured duration.
type RequestExpectation struct {
	Statuses    map[string]uint64
	LatenciesMS []float64
}

// MeasurementPresence controls whether an agent resource must be reported.
type MeasurementPresence uint8

const (
	MeasurementOptional MeasurementPresence = iota
	MeasurementRequired
	MeasurementAbsent
)

// ResourceExpectation checks resource availability, not changing resource values.
// Present CPU values must be finite and nonnegative; memory must be finite and
// positive. All resource series must carry agent identity without request labels.
type ResourceExpectation struct {
	CPU    MeasurementPresence
	Memory MeasurementPresence
}

// MetricsExpectation uses engine output as the oracle, after any documented
// label-budget folding. Bytes is an exact set of labels, independent of Requests:
// an aggregate-only producer supplies just its aggregate label, never fabricated
// per-request zeroes. Nil Concurrency requires the concurrency gauge to be absent.
type MetricsExpectation struct {
	Identity MetricIdentity
	Requests map[string]RequestExpectation
	// AllowedPriorIdentities permits SDK result-event registries to retain these
	// complete identities. Their series are checked for schema and finite values,
	// but excluded from the current run's totals and snapshot. Native normalized
	// producers should leave this nil to reject stale series.
	AllowedPriorIdentities []MetricIdentity
	Bytes                  map[string]float64
	Concurrency            *float64
	Resources              ResourceExpectation
}

// LatencySnapshot retains all histogram data, including the +Inf bucket.
type LatencySnapshot struct {
	Count   uint64
	Sum     float64
	Buckets map[float64]uint64
}

type RequestSnapshot struct {
	Statuses map[string]uint64
	Latency  *LatencySnapshot
}

// MetricsSnapshot can be compared across repeated or retained scrapes. It
// intentionally excludes CPU and memory, which change independently of a run's
// request measurements. Maps make comparison independent of exposition order.
type MetricsSnapshot struct {
	Identity    MetricIdentity
	Requests    map[string]RequestSnapshot
	Bytes       map[string]float64
	Concurrency *float64
}

// AssertMetrics checks the shibuya-v1 request metrics contract for either an SDK
// result-event producer or a native normalized producer. Non-shibuya collectors
// are ignored. Counter counts and histogram bounds are exact; sums and byte
// totals allow 1e-6 absolute rounding error when engine text output is summed.
func AssertMetrics(t *testing.T, exposition string, expected MetricsExpectation) MetricsSnapshot {
	t.Helper()
	snapshot, err := validateMetrics(exposition, expected)
	if err != nil {
		t.Fatalf("normalized metrics: %v", err)
	}
	return snapshot
}

func validateMetrics(exposition string, expected MetricsExpectation) (MetricsSnapshot, error) {
	snapshot := MetricsSnapshot{
		Identity: expected.Identity,
		Requests: make(map[string]RequestSnapshot),
		Bytes:    make(map[string]float64),
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(exposition))
	if err != nil {
		return snapshot, fmt.Errorf("parse exposition: %w", err)
	}
	identity := map[string]string{
		metricschema.LabelCollectionID: expected.Identity.CollectionID,
		metricschema.LabelPlanID:       expected.Identity.PlanID,
		metricschema.LabelRunID:        expected.Identity.RunID,
		metricschema.LabelEngineNo:     expected.Identity.EngineNo,
		metricschema.LabelZone:         expected.Identity.Zone,
	}
	seenGauges := make(map[string]bool)
	for name, family := range families {
		if !strings.HasPrefix(name, "shibuya_") {
			continue
		}
		var labelNames []string
		var kind dto.MetricType
		switch name {
		case metricschema.RequestLatency:
			labelNames, kind = metricschema.LatencyLabels(), dto.MetricType_HISTOGRAM
		case metricschema.StatusCounter:
			labelNames, kind = metricschema.StatusLabels(), dto.MetricType_COUNTER
		case metricschema.EgressBytesCounter:
			labelNames, kind = metricschema.EgressLabels(), dto.MetricType_COUNTER
		case metricschema.ThreadsGauge:
			labelNames, kind = metricschema.ThreadsLabels(), dto.MetricType_GAUGE
		case metricschema.CPUGauge, metricschema.MemoryGauge:
			labelNames, kind = metricschema.ResourceLabels(), dto.MetricType_GAUGE
		default:
			return snapshot, fmt.Errorf("unexpected schema family %q", name)
		}
		if family.GetType() != kind {
			return snapshot, fmt.Errorf("%s: type %s, want %s", name, family.GetType(), kind)
		}
		if len(family.Metric) == 0 {
			return snapshot, fmt.Errorf("%s: empty metric family", name)
		}
		for _, metric := range family.Metric {
			labels, err := validateMetricLabels(metric, labelNames, identity)
			if err != nil {
				prior := false
				for _, allowed := range expected.AllowedPriorIdentities {
					priorIdentity := map[string]string{
						metricschema.LabelCollectionID: allowed.CollectionID,
						metricschema.LabelPlanID:       allowed.PlanID,
						metricschema.LabelRunID:        allowed.RunID,
						metricschema.LabelEngineNo:     allowed.EngineNo,
						metricschema.LabelZone:         allowed.Zone,
					}
					if _, priorErr := validateMetricLabels(metric, labelNames, priorIdentity); priorErr == nil {
						prior = true
						break
					}
				}
				if !prior {
					return snapshot, fmt.Errorf("%s: %w", name, err)
				}
				if err := validateRetainedValues(metric, kind); err != nil {
					return snapshot, fmt.Errorf("%s retained series: %w", name, err)
				}
				continue
			}
			if kind == dto.MetricType_GAUGE {
				if seenGauges[name] {
					return snapshot, fmt.Errorf("%s: want exactly one current agent-level series", name)
				}
				seenGauges[name] = true
			}
			label := labels[metricschema.LabelRequest]
			switch name {
			case metricschema.RequestLatency, metricschema.StatusCounter:
				want, ok := expected.Requests[label]
				if !ok {
					return snapshot, fmt.Errorf("%s: unexpected request label %q", name, label)
				}
				request, ok := snapshot.Requests[label]
				if !ok {
					request.Statuses = make(map[string]uint64)
				}
				if name == metricschema.RequestLatency {
					if want.LatenciesMS == nil || request.Latency != nil {
						return snapshot, fmt.Errorf("%s: unexpected or duplicate histogram for %q", name, label)
					}
					request.Latency, err = validateLatency(metric.GetHistogram(), want.LatenciesMS)
					if err != nil {
						return snapshot, fmt.Errorf("%s label %q: %w", name, label, err)
					}
				} else {
					status := labels[metricschema.LabelStatus]
					count, ok := want.Statuses[status]
					if !ok {
						return snapshot, fmt.Errorf("%s label %q: unexpected status %q", name, label, status)
					}
					if _, duplicate := request.Statuses[status]; duplicate {
						return snapshot, fmt.Errorf("%s: duplicate status %q for %q", name, status, label)
					}
					value := metric.GetCounter().GetValue()
					if !finiteNonnegative(value) || value != float64(count) {
						return snapshot, fmt.Errorf("%s label %q status %q: count %g, want %d", name, label, status, value, count)
					}
					request.Statuses[status] = count
				}
				snapshot.Requests[label] = request
			case metricschema.EgressBytesCounter:
				want, ok := expected.Bytes[label]
				if !ok {
					return snapshot, fmt.Errorf("%s: unexpected byte label %q", name, label)
				}
				if _, duplicate := snapshot.Bytes[label]; duplicate {
					return snapshot, fmt.Errorf("%s: duplicate byte label %q", name, label)
				}
				value := metric.GetCounter().GetValue()
				if !sameMetricTotal(value, want) {
					return snapshot, fmt.Errorf("%s label %q: bytes %g, want %g", name, label, value, want)
				}
				snapshot.Bytes[label] = value
			case metricschema.ThreadsGauge:
				value := metric.GetGauge().GetValue()
				if expected.Concurrency == nil {
					return snapshot, fmt.Errorf("%s: unexpected concurrency measurement", name)
				}
				if !finiteNonnegative(value) || value != *expected.Concurrency {
					return snapshot, fmt.Errorf("%s: concurrency %g, want %g", name, value, *expected.Concurrency)
				}
				snapshot.Concurrency = &value
			case metricschema.CPUGauge, metricschema.MemoryGauge:
				value := metric.GetGauge().GetValue()
				if !finiteNonnegative(value) || (name == metricschema.MemoryGauge && value == 0) {
					return snapshot, fmt.Errorf("%s: invalid resource value %g", name, value)
				}
			}
		}
	}
	for label, want := range expected.Requests {
		request := snapshot.Requests[label]
		if len(request.Statuses) != len(want.Statuses) {
			return snapshot, fmt.Errorf("request %q: missing status series", label)
		}
		if want.LatenciesMS != nil && request.Latency == nil {
			return snapshot, fmt.Errorf("request %q: missing latency histogram", label)
		}
	}
	if len(snapshot.Bytes) != len(expected.Bytes) {
		return snapshot, fmt.Errorf("missing byte series: got %d, want %d", len(snapshot.Bytes), len(expected.Bytes))
	}
	if expected.Concurrency != nil && snapshot.Concurrency == nil {
		return snapshot, fmt.Errorf("missing concurrency measurement")
	}
	for name, presence := range map[string]MeasurementPresence{
		metricschema.CPUGauge: expected.Resources.CPU, metricschema.MemoryGauge: expected.Resources.Memory,
	} {
		present := seenGauges[name]
		switch presence {
		case MeasurementOptional:
		case MeasurementRequired:
			if !present {
				return snapshot, fmt.Errorf("%s: required resource absent", name)
			}
		case MeasurementAbsent:
			if present {
				return snapshot, fmt.Errorf("%s: resource must be absent", name)
			}
		default:
			return snapshot, fmt.Errorf("%s: invalid presence expectation %d", name, presence)
		}
	}
	return snapshot, nil
}

func validateMetricLabels(metric *dto.Metric, names []string, identity map[string]string) (map[string]string, error) {
	if len(metric.Label) != len(names) {
		return nil, fmt.Errorf("label count %d, want %d", len(metric.Label), len(names))
	}
	labels := make(map[string]string, len(names))
	for _, pair := range metric.Label {
		name := pair.GetName()
		if _, duplicate := labels[name]; duplicate {
			return nil, fmt.Errorf("duplicate label %q", name)
		}
		labels[name] = pair.GetValue()
	}
	for _, name := range names {
		if _, ok := labels[name]; !ok {
			return nil, fmt.Errorf("missing schema label %q", name)
		}
	}
	for name, want := range identity {
		if labels[name] != want {
			return nil, fmt.Errorf("identity %s=%q, want %q", name, labels[name], want)
		}
	}
	return labels, nil
}

func validateLatency(histogram *dto.Histogram, observations []float64) (*LatencySnapshot, error) {
	if histogram == nil || histogram.SampleCount == nil || histogram.SampleSum == nil {
		return nil, fmt.Errorf("incomplete histogram")
	}
	// shibuya-v1: 1ms, 2.5ms, then Prometheus default bounds converted to ms.
	bounds := [...]float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, math.Inf(1)}
	if len(histogram.Bucket) != len(bounds) {
		return nil, fmt.Errorf("bucket count %d, want %d", len(histogram.Bucket), len(bounds))
	}
	var sum float64
	for _, observation := range observations {
		if !finiteNonnegative(observation) {
			return nil, fmt.Errorf("invalid expected latency %g", observation)
		}
		sum += observation
	}
	if histogram.GetSampleCount() != uint64(len(observations)) || !sameMetricTotal(histogram.GetSampleSum(), sum) {
		return nil, fmt.Errorf("count/sum %d/%g, want %d/%g", histogram.GetSampleCount(), histogram.GetSampleSum(), len(observations), sum)
	}
	snapshot := &LatencySnapshot{Count: histogram.GetSampleCount(), Sum: histogram.GetSampleSum(), Buckets: make(map[float64]uint64, len(bounds))}
	for _, bucket := range histogram.Bucket {
		bound := bucket.GetUpperBound()
		if bucket.UpperBound == nil || bucket.CumulativeCount == nil {
			return nil, fmt.Errorf("incomplete bucket")
		}
		if _, duplicate := snapshot.Buckets[bound]; duplicate {
			return nil, fmt.Errorf("duplicate bucket bound %g", bound)
		}
		snapshot.Buckets[bound] = bucket.GetCumulativeCount()
	}
	for _, bound := range bounds {
		observed, ok := snapshot.Buckets[bound]
		if !ok {
			return nil, fmt.Errorf("missing schema bucket bound %g", bound)
		}
		var count uint64
		for _, observation := range observations {
			if observation <= bound {
				count++
			}
		}
		if observed != count {
			return nil, fmt.Errorf("bucket %g: count %d, want %d", bound, observed, count)
		}
	}
	return snapshot, nil
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func sameMetricTotal(value, want float64) bool {
	return finiteNonnegative(value) && finiteNonnegative(want) && math.Abs(value-want) <= 0.000001
}

func validateRetainedValues(metric *dto.Metric, kind dto.MetricType) error {
	switch kind {
	case dto.MetricType_COUNTER:
		if !finiteNonnegative(metric.GetCounter().GetValue()) {
			return fmt.Errorf("invalid counter value")
		}
	case dto.MetricType_GAUGE:
		if !finiteNonnegative(metric.GetGauge().GetValue()) {
			return fmt.Errorf("invalid gauge value")
		}
	case dto.MetricType_HISTOGRAM:
		histogram := metric.GetHistogram()
		if histogram == nil || histogram.SampleCount == nil || histogram.SampleSum == nil || !finiteNonnegative(histogram.GetSampleSum()) {
			return fmt.Errorf("invalid histogram")
		}
	}
	return nil
}
