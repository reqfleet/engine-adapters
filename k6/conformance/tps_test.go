//go:build integration && linux

package conformance_test

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reqfleet/engine-adapters/k6/metrics"
	"github.com/reqfleet/engine-sdk/conformance"
	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var tpsChunks = []string{"A", "界🙂é", "last"}

// This deliberately small RFC 6455 target uses only the standard library. All
// server frames are unmasked, final text frames with payloads shorter than 126
// bytes; the client sends no application messages or extensions to negotiate.
func serveTPSStream(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/tps/") {
		return false
	}
	if r.URL.Path != "/tps/stream" && r.URL.Path != "/tps/interrupted" {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "HTTP bytes are not generated content")
		return true
	}
	if !assert.Equal(t, "websocket", strings.ToLower(r.Header.Get("Upgrade"))) {
		http.Error(w, "websocket required", http.StatusBadRequest)
		return true
	}
	conn, rw, err := w.(http.Hijacker).Hijack()
	if !assert.NoError(t, err) {
		return true
	}
	defer conn.Close()
	if !assert.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second))) {
		return true
	}
	key := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(key[:]))
	if !assert.NoError(t, err) || !assert.NoError(t, rw.Flush()) {
		return true
	}
	send := func(message map[string]any) bool {
		payload, err := json.Marshal(message)
		if !assert.NoError(t, err) || !assert.Less(t, len(payload), 126) {
			return false
		}
		if _, err = rw.Write([]byte{0x81, byte(len(payload))}); !assert.NoError(t, err) {
			return false
		}
		if _, err = rw.Write(payload); !assert.NoError(t, err) {
			return false
		}
		return assert.NoError(t, rw.Flush())
	}
	if !send(map[string]any{"kind": "role", "role": "assistant"}) {
		return true
	}
	// Arrival of the first generated chunk is not an inter-chunk interval.
	time.Sleep(60 * time.Millisecond)
	chunks := tpsChunks
	if r.URL.Path == "/tps/interrupted" {
		chunks = chunks[:2]
	}
	total := 0
	// Send the first pair back-to-back to exercise buffered/coalesced callbacks.
	for index, chunk := range chunks {
		if index > 1 {
			time.Sleep(35 * time.Millisecond)
			if !send(map[string]any{"kind": "heartbeat"}) {
				return true
			}
			time.Sleep(35 * time.Millisecond)
		}
		if !send(map[string]any{"kind": "content", "content": chunk}) {
			return true
		}
		total += len(chunk)
	}
	kind := "complete"
	if r.URL.Path == "/tps/interrupted" {
		kind = "error"
	}
	// The completion summary is deliberately redundant: never count it again.
	if !send(map[string]any{"kind": kind, "total_bytes": total}) {
		return true
	}
	if _, err = rw.Write([]byte{0x88, 2, 3, 232}); !assert.NoError(t, err) || !assert.NoError(t, rw.Flush()) {
		return true
	}
	// Let stock k6 acknowledge the close before releasing the TCP connection.
	_, _ = io.Copy(io.Discard, rw)
	return true
}

func tpsScript(selectors string, emit, boundaries bool) string {
	values, err := json.Marshal(llmBoundaryValues(metricschema.RequestLatencyBuckets()))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`import http from 'k6/http';
import ws from 'k6/ws';
import {sleep} from 'k6';
import {Counter,Trend} from 'k6/metrics';
export const options={systemTags:['method']%s};
const streamed=new Counter('llm_streamed_bytes');
const interval=new Trend('llm_inter_chunk_duration',true);
const requestBytes=new Counter('request_received_bytes');
function stream(path){
  let previous=null;
  let chunks=0;
  let terminal=false;
  const response=ws.connect('%s'+path,{},function(socket){
    socket.on('message',function(raw){
      const message=JSON.parse(raw);
      if(message.kind==='complete'||message.kind==='error'){terminal=true;return;}
      if(message.kind!=='content')return;
      const now=Date.now();
      // Encode UTF-8, not UTF-16 code units, and exclude the JSON envelope.
      const bytes=encodeURIComponent(message.content).replace(/%%[0-9A-F]{2}/g,'x').length;
      const tags=chunks%%2===0?{}:{url:'not-tps',name:'NOT-TPS',status:'599'};
      if(%t){
        streamed.add(bytes,tags);
        if(previous!==null)interval.add(now-previous,tags);
      }
      previous=now;
      chunks++;
    });
    socket.on('error',function(error){throw new Error('stream transport failed: '+error);});
    socket.setTimeout(function(){socket.close();},4000);
  });
  if(response.status!==101||!terminal||chunks!==(path==='/tps/interrupted'?2:3))throw new Error('incomplete controlled stream');
}
export default function(){
  if(__ITER>0){sleep(0.1);return;}
  // HTTP claims the first label slot before independent measurements arrive.
  http.get('%s/tps/seed');
  requestBytes.add(9876,{url:'request-only'});
  stream('/tps/stream');
  stream('/tps/interrupted');
  if(%t)for(const value of %s)interval.add(value);
  http.get('%s/500');
  // Three outcomes prove both streams have finished before Stop.
  http.get('%s/tps/done');
  sleep(0.1);
}
export function teardown(){stream('/tps/stream');}
`, selectors, strings.Replace(conformance.TargetURL, "http://", "ws://", 1), emit,
		conformance.TargetURL, boundaries, values, conformance.TargetURL, conformance.TargetURL)
}

func tpsFixtures(artifacts map[string]string) []conformance.BudgetScenario {
	both := `,ext:{reqfleet:{receivedBytes:'llm_streamed_bytes',tpsInterval:'llm_inter_chunk_duration'}}`
	artifacts["tps-both.js"] = tpsScript(both, true, false)
	artifacts["tps-boundaries.js"] = tpsScript(both, true, true)
	artifacts["tps-none.js"] = tpsScript(both, false, false)
	artifacts["tps-default.js"] = tpsScript("", true, false)
	artifacts["tps-disabled.js"] = tpsScript(`,ext:{reqfleet:{receivedBytes:'',tpsInterval:''}}`, true, false)
	artifacts["tps-bytes.js"] = tpsScript(`,ext:{reqfleet:{receivedBytes:'llm_streamed_bytes'}}`, true, false)
	artifacts["tps-interval.js"] = tpsScript(`,ext:{reqfleet:{tpsInterval:'llm_inter_chunk_duration'}}`, true, false)
	artifacts["tps-request-interval.js"] = tpsScript(`,ext:{reqfleet:{receivedBytes:'request_received_bytes',tpsInterval:'llm_inter_chunk_duration'}}`, true, false)
	for index, selector := range []string{`"unknown"`, `"http_req_duration"`, `true`, `42`, `[]`, `{}`, `null`} {
		artifacts[fmt.Sprintf("tps-invalid-%d.js", index)] = tpsScript(
			fmt.Sprintf(",ext:{reqfleet:{receivedBytes:'llm_streamed_bytes',tpsInterval:%s}}", selector), true, false)
	}
	for _, source := range []string{metrics.LLMStreamedBytesMetric, metrics.LLMInterChunkDurationMetric} {
		selector, kind := "receivedBytes", "Counter"
		if source == metrics.LLMInterChunkDurationMetric {
			selector, kind = "tpsInterval", "Trend"
		}
		for _, selected := range []bool{false, true} {
			options := ""
			if selected {
				options = fmt.Sprintf(",ext:{reqfleet:{%s:%q}}", selector, source)
			}
			artifacts[fmt.Sprintf("tps-negative-%s-%t.js", source, selected)] = fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {%s} from 'k6/metrics';
export const options={systemTags:['method']%s};
const value=new %s(%q);
export default function(){if(__ITER>0){sleep(0.1);return;}value.add(-1);http.get('%s/tps/seed');sleep(0.1);}
`, kind, options, kind, source, conformance.TargetURL)
		}
	}
	fixture := conformance.Fixture{File: "tps-both.js", Settings: json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	return []conformance.BudgetScenario{
		{Name: "tps-overflow", LabelLimit: 1, Fixture: fixture, MinimumRequests: 3,
			RequestLabels: []string{conformance.TargetURL + "/tps/seed", "others"}, ByteLabels: []string{"others"}},
		{Name: "tps-zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 3,
			RequestLabels: []string{"others"}, ByteLabels: []string{"others"}},
	}
}

func assertTPSCSV(t *testing.T, runtime *conformance.Runtime, emit, boundaries bool) {
	t.Helper()
	file, err := os.Open(filepath.Join(runtime.Root, "test-result", "k6.csv"))
	require.NoError(t, err)
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	columns := map[string]int{}
	for index, name := range rows[0] {
		columns[name] = index
	}
	for _, name := range []string{"metric_name", "metric_value", "url"} {
		require.Contains(t, columns, name)
	}
	values := map[string][]float64{}
	urls := map[string]bool{}
	for _, row := range rows[1:] {
		name := row[columns["metric_name"]]
		switch name {
		case metrics.LLMStreamedBytesMetric, metrics.LLMInterChunkDurationMetric, metrics.RequestReceivedBytesMetric, metrics.DataReceivedMetric:
			value, err := strconv.ParseFloat(row[columns["metric_value"]], 64)
			require.NoError(t, err)
			values[name] = append(values[name], value)
			if name == metrics.LLMStreamedBytesMetric {
				urls[row[columns["url"]]] = true
			}
		}
	}
	require.NotEmpty(t, values[metrics.DataReceivedMetric], "ignored native wire traffic must actually exist")
	require.Equal(t, []float64{9876}, values[metrics.RequestReceivedBytesMetric], "the competing URL-attributed byte source exists too")
	if !emit {
		require.Empty(t, values[metrics.LLMStreamedBytesMetric])
		require.Empty(t, values[metrics.LLMInterChunkDurationMetric])
		return
	}
	want := []float64{}
	// Two complete streams (including teardown), one interrupted generation.
	for _, chunks := range [][]string{tpsChunks, tpsChunks[:2], tpsChunks} {
		for _, chunk := range chunks {
			want = append(want, float64(len(chunk)))
		}
	}
	require.Equal(t, want, values[metrics.LLMStreamedBytesMetric], "include every first chunk and UTF-8 byte, exclude envelopes, controls and completion totals")
	require.Equal(t, map[string]bool{"": true, "not-tps": true}, urls, "both absent and misleading tags must map to reserved tps")
	intervals := values[metrics.LLMInterChunkDurationMetric]
	if boundaries {
		boundary := llmBoundaryValues(metricschema.RequestLatencyBuckets())
		require.Len(t, intervals, 5+len(boundary))
		require.Equal(t, boundary, intervals[3:3+len(boundary)])
		intervals = append(append([]float64{}, intervals[:3]...), intervals[3+len(boundary):]...)
	}
	require.Len(t, intervals, 5, "three generations yield 2+1+2 intervals, never a fake first interval or cross-generation gap")
	for _, interval := range intervals {
		// Server send spacing does not bound client callback spacing.
		require.GreaterOrEqual(t, interval, 0.0, "coalesced callbacks may have a zero interval")
	}
}

func runTPSScenarios(t *testing.T, runtime *conformance.Runtime) {
	t.Helper()
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	csvPath := filepath.Join(runtime.Root, "test-result", "k6.csv")
	check := func(t *testing.T, id, file, source string, interval, emit, boundaries bool) {
		t.Helper()
		runtime.Start(t, id, conformance.Fixture{File: file, Settings: settings})
		runtime.Running(t, id)
		runtime.AwaitRequests(t, id, 3)
		runtime.Stop(t, id)
		runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
		selection := llmTimingSelection{interval: interval}
		observed, snapshot := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, source, selection)
		assertTPSCSV(t, runtime, emit, boundaries)
		require.Equal(t, uint64(3), observed.count, "WebSocket chunks, intervals and summary markers never create HTTP outcomes")
		require.Equal(t, map[string]float64{"200": 2, "500": 1}, observed.status)
		if source == metrics.LLMStreamedBytesMetric {
			if emit {
				want := float64(3*len(tpsChunks[0]) + 3*len(tpsChunks[1]) + 2*len(tpsChunks[2]))
				require.Equal(t, map[string]float64{"tps": want}, snapshot.Bytes, "exclude native and request-attributed bytes, including pre-stream traffic")
			} else {
				require.Empty(t, snapshot.Bytes, "missing selected samples cannot fall back to either competing byte source")
			}
		} else if source == metrics.RequestReceivedBytesMetric {
			require.Equal(t, map[string]float64{"request-only": 9876}, snapshot.Bytes)
		} else {
			require.Positive(t, observed.bytes)
			require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes)
		}
		if interval && emit {
			request := snapshot.Requests["tps"]
			require.NotNil(t, request.Latency)
			require.Nil(t, request.LLMResponseLatency)
			require.Empty(t, request.Statuses, "intervals never invent outcomes")
		} else {
			require.NotContains(t, snapshot.Requests, "tps", "disabled or unmeasured intervals remain unavailable")
		}
		_, retained := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, source, selection)
		require.Equal(t, snapshot, retained, "final streamed bytes and intervals remain retained without duplicate ingestion")
	}
	for index, scenario := range []struct {
		file, source               string
		interval, emit, boundaries bool
	}{
		{"tps-both.js", metrics.LLMStreamedBytesMetric, true, true, false},
		{"tps-default.js", metrics.DataReceivedMetric, false, true, false},
		{"tps-both.js", metrics.LLMStreamedBytesMetric, true, true, false},
		{"tps-disabled.js", metrics.DataReceivedMetric, false, true, false},
		{"tps-bytes.js", metrics.LLMStreamedBytesMetric, false, true, false},
		{"tps-interval.js", metrics.DataReceivedMetric, true, true, false},
		{"tps-request-interval.js", metrics.RequestReceivedBytesMetric, true, true, false},
		{"tps-none.js", metrics.LLMStreamedBytesMetric, true, false, false},
		{"tps-boundaries.js", metrics.LLMStreamedBytesMetric, true, true, true},
	} {
		t.Run("tps-reuse/"+strconv.Itoa(index), func(t *testing.T) {
			check(t, strconv.Itoa(1800+index), scenario.file, scenario.source, scenario.interval, scenario.emit, scenario.boundaries)
		})
	}
	for index := range 7 {
		t.Run(fmt.Sprintf("tps-invalid/%d", index), func(t *testing.T) {
			id := strconv.Itoa(1850 + index)
			runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("tps-invalid-%d.js", index), Settings: settings})
			status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
			require.Equal(t, protocol.FailureStartup, status.FailurePhase)
			body := scrape(t, runtime.Client)
			require.NotContains(t, body, `run_id="1808"`, "failed preparation clears prior TPS measurements")
			require.NotContains(t, body, `run_id="`+id+`"`, "invalid selections cannot launch or report measurements")
		})
	}
	for index, source := range []string{metrics.LLMStreamedBytesMetric, metrics.LLMInterChunkDurationMetric} {
		for selectedIndex, selected := range []bool{true, false} {
			t.Run(fmt.Sprintf("tps-negative/%s/%t", source, selected), func(t *testing.T) {
				id := strconv.Itoa(1870 + index*2 + selectedIndex)
				file := fmt.Sprintf("tps-negative-%s-%t.js", source, selected)
				runtime.Start(t, id, conformance.Fixture{File: file, Settings: settings})
				if selected {
					status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
					require.Contains(t, status.ErrorMessage, "invalid "+source+" sample")
					return
				}
				runtime.Running(t, id)
				runtime.AwaitRequests(t, id, 1)
				runtime.Stop(t, id)
				runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
				observed, snapshot := compareCSVSnapshot(t, runtime.Client, csvPath, id, runtime.LabelLimit)
				output, err := os.Open(csvPath)
				require.NoError(t, err)
				defer output.Close()
				rows, err := csv.NewReader(output).ReadAll()
				require.NoError(t, err)
				require.NotEmpty(t, rows)
				columns := map[string]int{}
				for index, name := range rows[0] {
					columns[name] = index
				}
				require.Contains(t, columns, "metric_name")
				require.Contains(t, columns, "metric_value")
				var values []float64
				for _, row := range rows[1:] {
					if row[columns["metric_name"]] == source {
						value, err := strconv.ParseFloat(row[columns["metric_value"]], 64)
						require.NoError(t, err)
						values = append(values, value)
					}
				}
				require.Equal(t, []float64{-1}, values, "the ignored source must emit a genuinely invalid observation")
				require.Equal(t, uint64(1), observed.count)
				require.NotContains(t, snapshot.Requests, "tps")
				require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes)
			})
		}
	}
	t.Run("tps-recovery", func(t *testing.T) {
		check(t, "1890", "tps-both.js", metrics.LLMStreamedBytesMetric, true, true, false)
		check(t, "1891", "tps-default.js", metrics.DataReceivedMetric, false, true, false)
	})
	runtime.AssertAlive(t)
}
