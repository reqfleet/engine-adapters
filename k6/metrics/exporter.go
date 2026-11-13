// Package metrics translates independent k6 CSV points into shibuya-v1 metrics.
//
// Collection reads the result file written by k6's --out csv option. CSV rows
// are independent samples, not complete request events. The exporter updates
// the SDK MetricsRecorder through separate request, byte, and concurrency methods,
// without fabricating requests for VU or byte samples. The SDK invokes Collect
// during the run and once after process exit to drain final output.
// The SDK owns metric definitions, resource sampling, exposition and authentication.
// See ../README.md for the CSV format and sample mapping.
package metrics

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
)

// Exporter owns CSV ingestion state and a run-scoped SDK metrics recorder.
// No reader or goroutine survives Collect; callbacks share the same state lock.
type Exporter struct {
	mu                                           sync.Mutex
	byteLabel                                    string
	recorder                                     *agent.MetricsRecorder
	runID, resultFile                            string
	active                                       bool
	offset                                       int64
	observedSize                                 int64
	fileInfo                                     os.FileInfo
	columns                                      int
	nameIndex, valueIndex, statusIndex, urlIndex int
	errorCodeIndex                               int
	ready                                        bool
	failure                                      error
}

// New validates the per-run URL label budget and uses the SDK's collectors.
// Additional URLs use the reserved overflow label; bytes keep aggregate attribution.
func New(labelLimit int, registry *agent.MetricsRegistry) (*Exporter, error) {
	recorder, err := agent.NewMetricsRecorder(registry, labelLimit)
	if err != nil {
		return nil, err
	}
	e := &Exporter{
		byteLabel: "k6",
		recorder:  recorder,
	}
	if labelLimit == 0 {
		e.byteLabel = metricschema.OverflowRequestName
	}
	return e, nil
}

// Reset binds the assigned run identity and clears SDK reporting and CSV state.
// The SDK must finish prior-run collection before calling it in BeforeStart.
// Call even when later adapter settings validation fails, to retire old metrics.
func (e *Exporter) Reset(run agent.RunContext) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runID, e.resultFile = run.RunID, run.ResultFile
	e.active = true
	e.offset, e.observedSize, e.columns = 0, 0, 0
	e.fileInfo = nil
	e.nameIndex, e.valueIndex, e.statusIndex, e.urlIndex = 0, 0, 0, 0
	e.errorCodeIndex = -1
	e.ready, e.failure = false, nil
	e.recorder.Reset(run)
}

// Observe reads cached readiness without collecting or changing the active run.
func (e *Exporter) Observe(ctx context.Context, run agent.RunContext) (protocol.Observation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.ObservationPending, err
	}
	if !e.matchesRun(run) {
		return protocol.ObservationPending, errors.New("metrics observation does not match the active run")
	}
	if e.failure != nil {
		return protocol.ObservationPending, e.failure
	}
	if !e.ready {
		return protocol.ObservationPending, nil
	}
	return protocol.ObservationReady, nil
}

// Collect drains complete CSV records into the SDK registry. The SDK joins live
// calls before the final post-exit call and retains the metrics until Reset.
func (e *Exporter) Collect(ctx context.Context, run agent.RunContext) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.matchesRun(run) {
		return errors.New("metrics collection does not match the active run")
	}
	return e.drain(ctx)
}

// matchesRun guards the CSV cursor against callbacks for another run.
// Reporting identity is bound and applied exclusively by the SDK recorder.
func (e *Exporter) matchesRun(run agent.RunContext) bool {
	return e.active && run.RunID == e.runID
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
	file, err := os.Open(e.resultFile)
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
	var metric agent.ShibuyaMetric
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
		metric.Label, metric.Status, metric.Latency = url, status, value
		if err := e.recorder.ObserveRequest(metric); err != nil {
			// Recorder label rejection drops the whole sample without
			// admitting its name, marking readiness, or failing the run.
			return nil
		}
	case "vus":
		if math.Trunc(value) != value {
			return fmt.Errorf("invalid fractional vus sample %q", record[e.valueIndex])
		}
		metric.Threads = value
		e.recorder.SetConcurrency(metric)
	case "data_received":
		metric.Label, metric.EgressBytes = e.byteLabel, value
		if err := e.recorder.AddAggregateReceivedBytes(metric); err != nil {
			return err
		}
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
