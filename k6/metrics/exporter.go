// Package metrics translates independent k6 CSV points into shibuya-v1 metrics.
//
// Collection reads the result file written by k6's --out csv option. The HTTP
// endpoint serves the normalized results; it is not the collection source.
//
// CSV rows are independent metric samples, not complete request events. The SDK's
// MetricParser returns a ShibuyaMetric whose ToPrometheus method updates latency,
// status, concurrency and bytes together. Sending a vus or data_received sample
// through that path would fabricate a request and a latency observation.
// Instead, the exporter parses CSV directly, updates only the relevant metric
// family for each sample, and serves the SDK's authenticated metrics route as an
// in-process handler. See ../README.md for the CSV format and sample mapping.
package metrics

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/containerstats"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
)

// Exporter owns one run's cumulative metrics and the persistent scrape endpoint.
// No reader or goroutine survives a drain; Reset and scrapes share the same lock.
type Exporter struct {
	mu                                           sync.Mutex
	byteLabel                                    string
	labelLimit                                   int
	urlLabels                                    map[string]string
	run                                          agent.RunContext
	active                                       bool
	offset                                       int64
	observedSize                                 int64
	fileInfo                                     os.FileInfo
	columns                                      int
	nameIndex, valueIndex, statusIndex, urlIndex int
	errorCodeIndex                               int
	ready                                        bool
	failure                                      error
	readCPUUsage                                 func() (uint64, error)
	readMemoryUsage                              func() (uint64, error)
	now                                          func() time.Time
	cpuBaseline                                  uint64 // cumulative microseconds
	cpuAt                                        time.Time
	cpuStarted                                   bool
	latency                                      *prometheus.HistogramVec
	status                                       *prometheus.CounterVec
	bytes                                        *prometheus.CounterVec
	threads                                      *prometheus.GaugeVec
	cpu                                          *prometheus.GaugeVec
	memory                                       *prometheus.GaugeVec
	handler                                      http.Handler
}

// New validates the per-run URL label budget. Additional URLs use the SDK's
// reserved overflow label; received bytes retain a separate aggregate label.
func New(labelLimit int) (*Exporter, error) {
	if labelLimit < 0 {
		return nil, errors.New("label_limit must be a nonnegative integer")
	}
	e := &Exporter{
		byteLabel: "k6", labelLimit: labelLimit, urlLabels: make(map[string]string),
		readCPUUsage: containerstats.ReadCPUUsage, readMemoryUsage: containerstats.ReadMemoryUsage,
		now: time.Now,
	}
	if labelLimit == 0 {
		e.byteLabel = "others"
	}
	// SDK histogram bounds: 1, 2.5, then prometheus.DefBuckets in ms.
	buckets := make([]float64, 2, len(prometheus.DefBuckets)+2)
	buckets[0], buckets[1] = 1, 2.5
	for _, bound := range prometheus.DefBuckets {
		buckets = append(buckets, bound*1000)
	}
	e.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: metricschema.RequestLatency, Help: "HTTP request duration in milliseconds.", Buckets: buckets}, metricschema.LatencyLabels())
	e.status = prometheus.NewCounterVec(prometheus.CounterOpts{Name: metricschema.StatusCounter, Help: "HTTP responses and transport failures by status."}, metricschema.StatusLabels())
	e.bytes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: metricschema.EgressBytesCounter, Help: "Received bytes reported by k6."}, metricschema.EgressLabels())
	e.threads = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: metricschema.ThreadsGauge, Help: "Actual per-agent active VUs."}, metricschema.ThreadsLabels())
	e.cpu = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: metricschema.CPUGauge, Help: "CPU cores consumed by the container."}, metricschema.ResourceLabels())
	e.memory = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: metricschema.MemoryGauge, Help: "Container memory usage in bytes."}, metricschema.ResourceLabels())
	registry := prometheus.NewRegistry()
	for _, collector := range []prometheus.Collector{e.latency, e.status, e.bytes, e.threads, e.cpu, e.memory} {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}
	e.handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	return e, nil
}

// Reset accepts a validated SDK run snapshot after its workspace was reset.
// Call even when later adapter settings validation fails, to retire old metrics.
func (e *Exporter) Reset(run agent.RunContext) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.run = agent.RunContext{RunID: run.RunID, CollectionID: run.CollectionID, PlanID: run.PlanID, AgentID: run.AgentID, Zone: run.Zone, ResultFile: run.ResultFile}
	e.active = true
	e.offset, e.observedSize, e.columns = 0, 0, 0
	e.fileInfo = nil
	e.nameIndex, e.valueIndex, e.statusIndex, e.urlIndex = 0, 0, 0, 0
	e.errorCodeIndex = -1
	clear(e.urlLabels)
	e.ready, e.failure = false, nil
	e.cpuBaseline, e.cpuAt, e.cpuStarted = 0, time.Time{}, false
	e.latency.Reset()
	e.status.Reset()
	e.bytes.Reset()
	e.threads.Reset()
	e.cpu.Reset()
	e.memory.Reset()
}

// Observe rejects callbacks from a retired run without disturbing the active run.
func (e *Exporter) Observe(ctx context.Context, run agent.RunContext) (protocol.Observation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active || run.RunID != e.run.RunID || run.CollectionID != e.run.CollectionID || run.PlanID != e.run.PlanID || run.AgentID != e.run.AgentID || run.Zone != e.run.Zone || run.ResultFile != e.run.ResultFile {
		return protocol.ObservationPending, errors.New("metrics observation does not match the active run")
	}
	if err := e.drain(ctx); err != nil {
		return protocol.ObservationPending, err
	}
	if !e.ready {
		return protocol.ObservationPending, nil
	}
	return protocol.ObservationReady, nil
}

// ServeHTTP drains final output even after the subprocess exits. The SDK owns
// cleanup; until the next Reset, unchanged files retain their cumulative totals.
func (e *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active {
		http.Error(w, "no prepared run", http.StatusServiceUnavailable)
		return
	}
	if err := e.drain(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !e.ready {
		http.Error(w, "waiting for a valid k6 sample", http.StatusServiceUnavailable)
		return
	}
	if err := e.reportEngineMetrics(); err != nil {
		http.Error(w, "container resources: "+err.Error(), http.StatusInternalServerError)
		return
	}
	e.handler.ServeHTTP(w, r)
}

func (e *Exporter) fail(err error) error {
	e.failure = err
	return err
}

// k6's CSV writer terminates every logical record with a newline. Replacing EOF
// with a private sentinel prevents encoding/csv from accepting a partial final
// field as a complete record. It also distinguishes an unfinished quoted field
// (including embedded newlines) from a malformed, complete CSV record. We keep
// the last committed offset, not the output contents, and retry from there.
var errIncomplete = errors.New("incomplete k6 CSV record")

type growingCSV struct{ io.Reader }

func (r growingCSV) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		err = errIncomplete
	}
	return n, err
}

func (e *Exporter) drain(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.failure != nil {
		return e.failure
	}
	file, err := os.Open(e.run.ResultFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && e.fileInfo == nil {
			return nil
		}
		return e.fail(fmt.Errorf("open k6 CSV: %w", err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return e.fail(fmt.Errorf("stat k6 CSV: %w", err))
	}
	if !info.Mode().IsRegular() {
		return e.fail(errors.New("k6 CSV is not a regular file"))
	}
	if e.fileInfo != nil && (!os.SameFile(e.fileInfo, info) || info.Size() < e.observedSize) {
		return e.fail(errors.New("k6 CSV was replaced or truncated"))
	}
	e.fileInfo, e.observedSize = info, info.Size()
	base := e.offset
	reader := csv.NewReader(growingCSV{io.NewSectionReader(file, base, info.Size()-base)})
	reader.FieldsPerRecord = e.columns
	reader.ReuseRecord = true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := reader.Read()
		if errors.Is(err, errIncomplete) {
			// Catch concurrent truncation, including one occurring before a
			// trailing record could be committed.
			latest, statErr := file.Stat()
			if statErr != nil {
				return e.fail(fmt.Errorf("stat k6 CSV: %w", statErr))
			}
			if latest.Size() < info.Size() {
				return e.fail(errors.New("k6 CSV was truncated while reading"))
			}
			return nil
		}
		if err != nil {
			return e.fail(fmt.Errorf("read k6 CSV at offset %d: %w", e.offset, err))
		}
		if e.columns == 0 {
			if err := e.header(record); err != nil {
				return e.fail(err)
			}
		} else if err := e.point(record); err != nil {
			return e.fail(fmt.Errorf("k6 CSV at offset %d: %w", e.offset, err))
		}
		e.offset = base + reader.InputOffset()
	}
}

func (e *Exporter) header(record []string) error {
	indexes := map[string]int{}
	for index, name := range record {
		if name != "metric_name" && name != "metric_value" && name != "status" && name != "url" && name != "error_code" {
			continue
		}
		if _, exists := indexes[name]; exists {
			return fmt.Errorf("duplicate k6 CSV column %q", name)
		}
		indexes[name] = index
	}
	for _, name := range []string{"metric_name", "metric_value", "status", "url"} {
		if _, exists := indexes[name]; !exists {
			return fmt.Errorf("missing k6 CSV column %q", name)
		}
	}
	e.columns = len(record)
	e.nameIndex, e.valueIndex, e.statusIndex, e.urlIndex = indexes["metric_name"], indexes["metric_value"], indexes["status"], indexes["url"]
	e.errorCodeIndex = -1
	if index, exists := indexes["error_code"]; exists {
		e.errorCodeIndex = index
	}
	return nil
}

func (e *Exporter) point(record []string) error {
	name := record[e.nameIndex]
	if name != "http_req_duration" && name != "vus" && name != "data_received" {
		return nil
	}
	value, err := strconv.ParseFloat(record[e.valueIndex], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("invalid %s sample %q", name, record[e.valueIndex])
	}
	run := e.run
	switch name {
	case "http_req_duration":
		url := record[e.urlIndex]
		if url == "" {
			return errors.New("missing HTTP request URL")
		}
		status := record[e.statusIndex]
		// HTTP status tags are canonical decimal codes, or k6's transport
		// failure sentinel 0. Reject blank, fractional, and non-HTTP tags.
		code, err := strconv.Atoi(status)
		if err != nil || strconv.Itoa(code) != status || (code != 0 && (code < 100 || code > 599)) {
			return fmt.Errorf("invalid HTTP status %q", status)
		}
		if code == 0 {
			// Use stable descriptions with native codes, never free-form
			// messages containing changing hosts, addresses or ports.
			status = "Transport error (cause unavailable)"
			if e.errorCodeIndex >= 0 && record[e.errorCodeIndex] != "" {
				errorCode := record[e.errorCodeIndex]
				value, err := strconv.ParseUint(errorCode, 10, 32)
				if err != nil || value < 1000 || errorCode[0] < '1' || errorCode[0] > '9' {
					return fmt.Errorf("invalid k6 error code %q", errorCode)
				}
				status = transportErrorStatus(value)
			}
		}
		label := e.requestLabel(url)
		e.latency.WithLabelValues(run.CollectionID, label, run.RunID, run.AgentID, run.PlanID, run.Zone).Observe(value)
		e.status.WithLabelValues(run.CollectionID, run.PlanID, run.RunID, run.AgentID, label, run.Zone, status).Inc()
	case "vus":
		if math.Trunc(value) != value {
			return fmt.Errorf("invalid fractional vus sample %q", record[e.valueIndex])
		}
		e.threads.WithLabelValues(run.CollectionID, run.PlanID, run.RunID, run.AgentID, run.Zone).Set(value)
	case "data_received":
		e.bytes.WithLabelValues(run.CollectionID, run.PlanID, run.RunID, run.AgentID, e.byteLabel, run.Zone).Add(value)
	}
	e.ready = true
	return nil
}

// transportErrorStatus describes k6 error codes without confusing them with HTTP
// statuses. Keep common labels constant to avoid allocating on each failed request.
// https://grafana.com/docs/k6/latest/javascript-api/error-codes/
func transportErrorStatus(code uint64) string {
	switch code {
	case 1000:
		return "Request error (k6 1000)"
	case 1010:
		return "Network error (k6 1010)"
	case 1020:
		return "Invalid URL (k6 1020)"
	case 1050:
		return "HTTP request timeout (k6 1050)"
	case 1100:
		return "DNS lookup error (k6 1100)"
	case 1101:
		return "DNS host not found (k6 1101)"
	case 1110:
		return "Blocked IP address (k6 1110)"
	case 1111:
		return "Blocked hostname (k6 1111)"
	case 1200:
		return "TCP error (k6 1200)"
	case 1201:
		return "TCP broken pipe (k6 1201)"
	case 1202:
		return "TCP system error (k6 1202)"
	case 1210:
		return "TCP connection error (k6 1210)"
	case 1211:
		return "TCP connection timeout (k6 1211)"
	case 1212:
		return "TCP connection refused (k6 1212)"
	case 1213:
		return "TCP connection system error (k6 1213)"
	case 1220:
		return "TCP connection reset by peer (k6 1220)"
	case 1300:
		return "TLS error (k6 1300)"
	case 1301:
		return "TLS protocol error (k6 1301)"
	case 1310:
		return "TLS untrusted certificate (k6 1310)"
	case 1311:
		return "TLS certificate hostname mismatch (k6 1311)"
	case 1600:
		return "HTTP/2 error (k6 1600)"
	case 1701:
		return "Response decompression error (k6 1701)"
	}
	description := "Unknown k6 error"
	switch {
	case code >= 1400 && code <= 1499:
		description = "HTTP client error"
	case code >= 1500 && code <= 1599:
		description = "HTTP server error"
	case code >= 1610 && code <= 1629:
		description = "HTTP/2 GOAWAY error"
	case code >= 1630 && code <= 1649:
		description = "HTTP/2 stream error"
	case code >= 1650 && code <= 1669:
		description = "HTTP/2 connection error"
	}
	return description + " (k6 " + strconv.FormatUint(code, 10) + ")"
}

func (e *Exporter) requestLabel(url string) string {
	// k6's CSV url tag may already be a plan-defined name/grouping value,
	// rather than the requested URL. The original URL is unavailable here.
	// shibuya-v1 requires fewer than 1024 bytes. Preserve a UTF-8-safe prefix
	// before admission; distinct values with the same prefix share metrics.
	if len(url) >= 1024 {
		end := 1023
		for end > 0 && !utf8.RuneStart(url[end]) {
			end--
		}
		url = url[:end]
	}
	if label, admitted := e.urlLabels[url]; admitted {
		return label
	}
	if len(e.urlLabels) >= e.labelLimit {
		return "others"
	}
	// CSV fields share the record's storage; retain only the admitted URL.
	url = strings.Clone(url)
	e.urlLabels[url] = url
	return url
}

func (e *Exporter) reportEngineMetrics() error {
	cpuMicros, err := e.readCPUUsage()
	if err != nil {
		return fmt.Errorf("CPU usage: %w", err)
	}
	memoryBytes, err := e.readMemoryUsage()
	if err != nil {
		return fmt.Errorf("memory usage: %w", err)
	}
	now := e.now()
	run := e.run
	if e.cpuStarted {
		elapsed := now.Sub(e.cpuAt).Seconds()
		if elapsed <= 0 || cpuMicros < e.cpuBaseline {
			return errors.New("CPU usage or sampling clock moved backwards")
		}
		cores := float64(cpuMicros-e.cpuBaseline) / 1e6 / elapsed
		e.cpu.WithLabelValues(run.CollectionID, run.PlanID, run.RunID, run.Zone, run.AgentID).Set(cores)
	}
	e.cpuBaseline, e.cpuAt, e.cpuStarted = cpuMicros, now, true
	e.memory.WithLabelValues(run.CollectionID, run.PlanID, run.RunID, run.Zone, run.AgentID).Set(float64(memoryBytes))
	return nil
}
