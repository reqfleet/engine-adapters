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
	chunks := tpsChunks
	if r.URL.Path == "/tps/interrupted" {
		chunks = chunks[:2]
	}
	total := 0
	for index, chunk := range chunks {
		if index > 1 {
			if !send(map[string]any{"kind": "heartbeat"}) {
				return true
			}
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

func tpsScript(selectors string, emit bool) string {
	return fmt.Sprintf(`import http from 'k6/http';
import ws from 'k6/ws';
import {sleep} from 'k6';
import {Counter} from 'k6/metrics';
export const options={systemTags:['method']%s};
const streamed=new Counter('llm_streamed_bytes');
const requestBytes=new Counter('request_received_bytes');
function stream(path){
  let chunks=0;
  let terminal=false;
  const response=ws.connect('%s'+path,{},function(socket){
    socket.on('message',function(raw){
      const message=JSON.parse(raw);
      if(message.kind==='complete'||message.kind==='error'){terminal=true;return;}
      if(message.kind!=='content')return;
      // Encode UTF-8, not UTF-16 code units, and exclude the JSON envelope.
      const bytes=encodeURIComponent(message.content).replace(/%%[0-9A-F]{2}/g,'x').length;
      const tags=chunks%%2===0?{}:{url:'not-tps',name:'NOT-TPS',status:'599'};
      if(%t){
        streamed.add(bytes,tags);
      }
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
  http.get('%s/500');
  // Three outcomes prove both streams have finished before Stop.
  http.get('%s/tps/done');
  sleep(0.1);
}
export function teardown(){stream('/tps/stream');}
`, selectors, strings.Replace(conformance.TargetURL, "http://", "ws://", 1), emit,
		conformance.TargetURL, conformance.TargetURL, conformance.TargetURL)
}

func tpsFixtures(artifacts map[string]string) []conformance.BudgetScenario {
	selection := `,ext:{reqfleet:{receivedBytes:'llm_streamed_bytes'}}`
	artifacts["tps-bytes.js"] = tpsScript(selection, true)
	artifacts["tps-none.js"] = tpsScript(selection, false)
	artifacts["tps-default.js"] = tpsScript("", true)
	artifacts["tps-disabled.js"] = tpsScript(`,ext:{reqfleet:{receivedBytes:''}}`, true)
	artifacts["tps-request-bytes.js"] = tpsScript(`,ext:{reqfleet:{receivedBytes:'request_received_bytes'}}`, true)
	for _, selected := range []bool{false, true} {
		options := ""
		if selected {
			options = selection
		}
		artifacts[fmt.Sprintf("tps-negative-%t.js", selected)] = fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {Counter} from 'k6/metrics';
export const options={systemTags:['method']%s};
const value=new Counter('llm_streamed_bytes');
export default function(){if(__ITER>0){sleep(0.1);return;}value.add(-1);http.get('%s/tps/seed');sleep(0.1);}
`, options, conformance.TargetURL)
	}
	fixture := conformance.Fixture{File: "tps-bytes.js", Settings: json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	return []conformance.BudgetScenario{
		{Name: "tps-overflow", LabelLimit: 1, Fixture: fixture, MinimumRequests: 3,
			RequestLabels: []string{conformance.TargetURL + "/tps/seed", "others"}, ByteLabels: []string{"others"}},
		{Name: "tps-zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 3,
			RequestLabels: []string{"others"}, ByteLabels: []string{"others"}},
	}
}

func assertTPSCSV(t *testing.T, runtime *conformance.Runtime, emit bool) {
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
		case metrics.LLMStreamedBytesMetric, metrics.RequestReceivedBytesMetric, metrics.DataReceivedMetric:
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
}

func runTPSScenarios(t *testing.T, runtime *conformance.Runtime) {
	t.Helper()
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	csvPath := filepath.Join(runtime.Root, "test-result", "k6.csv")
	check := func(t *testing.T, id, file, source string, emit bool) {
		t.Helper()
		runtime.Start(t, id, conformance.Fixture{File: file, Settings: settings})
		runtime.Running(t, id)
		runtime.AwaitRequests(t, id, 3)
		runtime.Stop(t, id)
		runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
		observed, snapshot := compareCSVSnapshotSource(t, runtime.Client, csvPath, id, runtime.LabelLimit, source)
		assertTPSCSV(t, runtime, emit)
		require.Equal(t, uint64(3), observed.count, "WebSocket chunks and summary markers never create HTTP outcomes")
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
		require.NotContains(t, snapshot.Requests, "tps", "byte-only TPS never fabricates latency or outcomes")
		_, retained := compareCSVSnapshotSource(t, runtime.Client, csvPath, id, runtime.LabelLimit, source)
		require.Equal(t, snapshot, retained, "final streamed bytes remain retained without duplicate ingestion")
	}
	for index, scenario := range []struct {
		file, source string
		emit         bool
	}{
		{"tps-bytes.js", metrics.LLMStreamedBytesMetric, true},
		{"tps-default.js", metrics.DataReceivedMetric, true},
		{"tps-bytes.js", metrics.LLMStreamedBytesMetric, true},
		{"tps-disabled.js", metrics.DataReceivedMetric, true},
		{"tps-request-bytes.js", metrics.RequestReceivedBytesMetric, true},
		{"tps-none.js", metrics.LLMStreamedBytesMetric, false},
	} {
		t.Run("tps-reuse/"+strconv.Itoa(index), func(t *testing.T) {
			check(t, strconv.Itoa(1800+index), scenario.file, scenario.source, scenario.emit)
		})
	}
	for selectedIndex, selected := range []bool{true, false} {
		t.Run(fmt.Sprintf("tps-negative/%t", selected), func(t *testing.T) {
			source := metrics.LLMStreamedBytesMetric
			id := strconv.Itoa(1870 + selectedIndex)
			file := fmt.Sprintf("tps-negative-%t.js", selected)
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
	t.Run("tps-recovery", func(t *testing.T) {
		check(t, "1890", "tps-bytes.js", metrics.LLMStreamedBytesMetric, true)
		check(t, "1891", "tps-default.js", metrics.DataReceivedMetric, true)
	})
	runtime.AssertAlive(t)
}
