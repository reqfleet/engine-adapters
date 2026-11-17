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

// The controlled endpoint declares each nonempty content event to be exactly
// one generated model token, regardless of its UTF-8 byte or character length.
var tpsTokens = []string{"A", "界🙂é", "last"}

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
	if !send(map[string]any{"kind": "role", "role": "assistant", "unit": "model_token"}) {
		return true
	}
	chunks := tpsTokens
	if r.URL.Path == "/tps/interrupted" {
		chunks = chunks[:2]
	}
	if !send(map[string]any{"kind": "content", "content": "", "tokens": 0}) {
		return true
	}
	time.Sleep(20 * time.Millisecond)
	for index, chunk := range chunks {
		if index > 0 {
			time.Sleep(15 * time.Millisecond)
		}
		if index > 1 {
			if !send(map[string]any{"kind": "heartbeat"}) {
				return true
			}
		}
		if !send(map[string]any{"kind": "content", "content": chunk, "tokens": 1}) {
			return true
		}
	}
	kind := "complete"
	if r.URL.Path == "/tps/interrupted" {
		kind = "error"
	}
	// The completion summary is deliberately redundant: never count it again.
	if !send(map[string]any{"kind": "done"}) || !send(map[string]any{"kind": kind, "total_tokens": len(chunks)}) {
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
import {Counter,Trend} from 'k6/metrics';
export const options={systemTags:['method']%s};
const tokens=new Counter('llm_token_events');
const gaps=new Trend('llm_inter_token_duration',true);
const ttft=new Trend('llm_time_to_first_token',true);
const requestBytes=new Counter('request_received_bytes');
function stream(path){
  // k6 teardown has no execution-state context; use a clock available in both hooks.
  const started=Date.now();
  let previous=null;
  let chunks=0;
  let terminal=false;
  let tokenUnits=false;
  const response=ws.connect('%s'+path,{},function(socket){
    socket.on('message',function(raw){
      const message=JSON.parse(raw);
      if(message.kind==='role'){tokenUnits=message.unit==='model_token';return;}
      if(message.kind==='complete'||message.kind==='error'){terminal=true;return;}
      if(message.kind!=='content'||!message.content)return;
      // Transport frames alone do not establish model-token cardinality.
      if(!tokenUnits||message.tokens!==1)throw new Error('unknown token cardinality');
      const tags=chunks%%2===0?{}:{url:'not-tps',name:'NOT-TPS',status:'599'};
      if(%t){
        const now=Date.now();
        if(previous===null)ttft.add(now-started,tags);
        else gaps.add(now-previous,tags);
        previous=now;
        tokens.add(1,tags);
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
  // Three HTTP outcomes plus five token receipts precede teardown.
  http.get('%s/tps/done');
  sleep(0.1);
}
export function teardown(){stream('/tps/stream');}
`, selectors, strings.Replace(conformance.TargetURL, "http://", "ws://", 1), emit,
		conformance.TargetURL, conformance.TargetURL, conformance.TargetURL)
}

func tpsFixtures(artifacts map[string]string) []conformance.BudgetScenario {
	selection := `,ext:{reqfleet:{tps:'llm_token_events',interToken:'llm_inter_token_duration',ttft:'llm_time_to_first_token'}}`
	artifacts["tps-tokens.js"] = tpsScript(selection, true)
	artifacts["tps-none.js"] = tpsScript(selection, false)
	artifacts["tps-default.js"] = tpsScript("", true)
	artifacts["tps-disabled.js"] = tpsScript(`,ext:{reqfleet:{tps:'',interToken:'',ttft:''}}`, true)
	artifacts["tps-request-bytes.js"] = tpsScript(`,ext:{reqfleet:{tps:'llm_token_events',interToken:'llm_inter_token_duration',ttft:'llm_time_to_first_token',receivedBytes:'request_received_bytes'}}`, true)
	artifacts["tps-count-only.js"] = tpsScript(`,ext:{reqfleet:{tps:'llm_token_events'}}`, true)
	artifacts["tps-gap-only.js"] = tpsScript(`,ext:{reqfleet:{interToken:'llm_inter_token_duration',ttft:'llm_time_to_first_token'}}`, true)
	for _, selected := range []bool{false, true} {
		options := ""
		if selected {
			options = `,ext:{reqfleet:{interToken:'llm_inter_token_duration'}}`
		}
		artifacts[fmt.Sprintf("tps-gap-invalid-%t.js", selected)] = fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {Trend} from 'k6/metrics';
export const options={systemTags:['method']%s};
const gaps=new Trend('llm_inter_token_duration',true);
export default function(){if(__ITER>0){sleep(0.1);return;}gaps.add(-1);http.get('%s/tps/seed');sleep(0.1);}
`, options, conformance.TargetURL)
	}
	for index, selector := range []string{`true`, `'llm_token_events'`} {
		artifacts[fmt.Sprintf("tps-gap-selector-%d.js", index)] = tpsScript(`,ext:{reqfleet:{interToken:`+selector+`}}`, true)
	}
	for _, selected := range []bool{false, true} {
		options := ""
		if selected {
			options = selection
		}
		for index, value := range []float64{-1, 0, 2, 0.5} {
			artifacts[fmt.Sprintf("tps-invalid-%t-%d.js", selected, index)] = fmt.Sprintf(`import http from 'k6/http';
import {sleep} from 'k6';
import {Counter} from 'k6/metrics';
export const options={systemTags:['method']%s};
const value=new Counter('llm_token_events');
export default function(){if(__ITER>0){sleep(0.1);return;}value.add(%g);http.get('%s/tps/seed');sleep(0.1);}
`, options, value, conformance.TargetURL)
		}
	}
	for index, selector := range []string{`true`, `12`, `[]`, `{}`, `'unknown'`, `'llm_streamed_bytes'`} {
		artifacts[fmt.Sprintf("tps-selector-%d.js", index)] = tpsScript(`,ext:{reqfleet:{tps:`+selector+`}}`, true)
	}
	fixture := conformance.Fixture{File: "tps-tokens.js", Settings: json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	return []conformance.BudgetScenario{
		{Name: "tps-overflow", LabelLimit: 1, Fixture: fixture, MinimumRequests: 8,
			RequestLabels: []string{conformance.TargetURL + "/tps/seed", "others"}, ByteLabels: []string{"k6"}},
		{Name: "tps-zero", LabelLimit: 0, Fixture: fixture, MinimumRequests: 8,
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
		case metrics.LLMTokenEventsMetric, metrics.LLMInterTokenDurationMetric, llmTTFTSource, metrics.RequestReceivedBytesMetric, metrics.DataReceivedMetric:
			value, err := strconv.ParseFloat(row[columns["metric_value"]], 64)
			require.NoError(t, err)
			values[name] = append(values[name], value)
			if name == metrics.LLMTokenEventsMetric {
				urls[row[columns["url"]]] = true
			}
		}
	}
	require.NotEmpty(t, values[metrics.DataReceivedMetric], "independent native wire traffic must actually exist")
	require.Equal(t, []float64{9876}, values[metrics.RequestReceivedBytesMetric], "the competing URL-attributed byte source exists too")
	if !emit {
		require.Empty(t, values[metrics.LLMTokenEventsMetric])
		require.Empty(t, values[metrics.LLMInterTokenDurationMetric])
		require.Empty(t, values[llmTTFTSource])
		return
	}
	want := []float64{}
	// Two complete streams (including teardown), one interrupted generation.
	for _, tokens := range [][]string{tpsTokens, tpsTokens[:2], tpsTokens} {
		for range tokens {
			want = append(want, 1)
		}
	}
	require.Equal(t, want, values[metrics.LLMTokenEventsMetric], "include every first token, exclude envelopes, empty content, controls and completion totals")
	require.Len(t, values[metrics.LLMInterTokenDurationMetric], 5, "three streams contribute 2+1+2 gaps, never first-token or control intervals")
	require.Len(t, values[llmTTFTSource], 3, "each stream resets its previous-token timestamp, including interrupted generation and teardown")
	for _, gap := range values[metrics.LLMInterTokenDurationMetric] {
		require.GreaterOrEqual(t, gap, float64(0), "source milliseconds may legitimately be zero")
	}
	require.Equal(t, map[string]bool{"": true, "not-tps": true}, urls, "both absent and misleading tags must map to reserved tps")
}

func runTPSScenarios(t *testing.T, runtime *conformance.Runtime) {
	t.Helper()
	settings := json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)
	csvPath := filepath.Join(runtime.Root, "test-result", "k6.csv")
	check := func(t *testing.T, id, file, source string, selected, interToken, emit bool) {
		t.Helper()
		runtime.Start(t, id, conformance.Fixture{File: file, Settings: settings})
		runtime.Running(t, id)
		if selected && emit {
			runtime.AwaitRequests(t, id, 8)
		} else {
			runtime.AwaitRequests(t, id, 3)
		}
		runtime.Stop(t, id)
		runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
		selection := llmTimingSelection{tps: selected, interToken: interToken, ttft: interToken}
		observed, snapshot := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, source, selection)
		assertTPSCSV(t, runtime, emit)
		require.Equal(t, uint64(3), observed.count, "token receipts and summary markers never create HTTP outcomes")
		require.Equal(t, map[string]float64{"200": 2, "500": 1}, observed.status)
		if source == metrics.RequestReceivedBytesMetric {
			require.Equal(t, map[string]float64{"request-only": 9876}, snapshot.Bytes)
		} else {
			require.Positive(t, observed.bytes)
			require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes)
		}
		if selected && emit {
			require.Equal(t, map[string]uint64{"200": 8}, snapshot.Requests["tps"].Statuses)
			if !interToken {
				require.Nil(t, snapshot.Requests["tps"].Latency)
			}
			require.Nil(t, snapshot.Requests["tps"].LLMResponseLatency)
		} else if interToken && emit {
			require.Empty(t, snapshot.Requests["tps"].Statuses, "gap-only selection must not count token or HTTP outcomes")
		} else {
			require.NotContains(t, snapshot.Requests, "tps")
		}
		if interToken && emit {
			require.NotNil(t, snapshot.Requests["tps"].Latency)
			require.Equal(t, uint64(5), snapshot.Requests["tps"].Latency.Count)
			require.Nil(t, snapshot.Requests["tps"].LLMResponseLatency)
			require.NotNil(t, snapshot.Requests["ttft"].Latency)
			require.Equal(t, uint64(3), snapshot.Requests["ttft"].Latency.Count)
			require.Empty(t, snapshot.Requests["ttft"].Statuses)
		}
		require.NotContains(t, snapshot.Bytes, "tps", "tokens never create bandwidth")
		_, retained := compareCSVSnapshotSelection(t, runtime.Client, csvPath, id, runtime.LabelLimit, source, selection)
		require.Equal(t, snapshot, retained, "final token counts remain retained without duplicate ingestion")
	}
	for index, scenario := range []struct {
		file, source               string
		selected, interToken, emit bool
	}{
		{"tps-tokens.js", metrics.DataReceivedMetric, true, true, true},
		{"tps-default.js", metrics.DataReceivedMetric, false, false, true},
		{"tps-tokens.js", metrics.DataReceivedMetric, true, true, true},
		{"tps-disabled.js", metrics.DataReceivedMetric, false, false, true},
		{"tps-request-bytes.js", metrics.RequestReceivedBytesMetric, true, true, true},
		{"tps-none.js", metrics.DataReceivedMetric, true, true, false},
		{"tps-gap-only.js", metrics.DataReceivedMetric, false, true, true},
		{"tps-count-only.js", metrics.DataReceivedMetric, true, false, true},
	} {
		t.Run("tps-reuse/"+strconv.Itoa(index), func(t *testing.T) {
			check(t, strconv.Itoa(1800+index), scenario.file, scenario.source, scenario.selected, scenario.interToken, scenario.emit)
		})
	}
	for selectedIndex, selected := range []bool{true, false} {
		for index, invalid := range []float64{-1, 0, 2, 0.5} {
			t.Run(fmt.Sprintf("tps-invalid/%t/%d", selected, index), func(t *testing.T) {
				source := metrics.LLMTokenEventsMetric
				id := strconv.Itoa(1850 + selectedIndex*10 + index)
				file := fmt.Sprintf("tps-invalid-%t-%d.js", selected, index)
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
				require.Equal(t, []float64{invalid}, values, "the ignored source must emit a genuinely invalid observation")
				require.Equal(t, uint64(1), observed.count)
				require.NotContains(t, snapshot.Requests, "tps")
				require.Equal(t, map[string]float64{"k6": observed.bytes}, snapshot.Bytes)
			})
		}
	}
	for index := range 6 {
		t.Run(fmt.Sprintf("tps-selector/%d", index), func(t *testing.T) {
			id := strconv.Itoa(1880 + index)
			runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("tps-selector-%d.js", index), Settings: settings})
			status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
			require.Contains(t, status.ErrorMessage, "tps")
			body := scrape(t, runtime.Client)
			require.NotContains(t, body, `run_id="1863"`, "failed preparation must clear preceding run measurements")
			require.NotContains(t, body, `run_id="`+id+`"`, "invalid selection must not launch or report samples")
		})
	}
	for index, selected := range []bool{true, false} {
		t.Run(fmt.Sprintf("tps-gap-invalid/%t", selected), func(t *testing.T) {
			id := strconv.Itoa(1892 + index)
			runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("tps-gap-invalid-%t.js", selected), Settings: settings})
			if selected {
				status := runtime.Terminal(t, id, protocol.RunFailed, proofWait)
				require.Contains(t, status.ErrorMessage, "invalid "+metrics.LLMInterTokenDurationMetric+" sample")
				return
			}
			runtime.Running(t, id)
			runtime.AwaitRequests(t, id, 1)
			runtime.Stop(t, id)
			runtime.Terminal(t, id, protocol.RunCanceled, proofWait)
			observed, snapshot := compareCSVSnapshot(t, runtime.Client, csvPath, id, runtime.LabelLimit)
			require.Equal(t, uint64(1), observed.count)
			require.NotContains(t, snapshot.Requests, "tps")
			output, err := os.ReadFile(csvPath)
			require.NoError(t, err)
			require.Contains(t, string(output), metrics.LLMInterTokenDurationMetric, "unselected invalid source must actually be emitted")
		})
	}
	for index := range 2 {
		t.Run(fmt.Sprintf("tps-gap-selector/%d", index), func(t *testing.T) {
			id := strconv.Itoa(1894 + index)
			runtime.Start(t, id, conformance.Fixture{File: fmt.Sprintf("tps-gap-selector-%d.js", index), Settings: settings})
			runtime.Terminal(t, id, protocol.RunFailed, proofWait)
			body := scrape(t, runtime.Client)
			require.NotContains(t, body, `run_id="1893"`, "failed preparation must clear previous measurements")
			require.NotContains(t, body, `run_id="`+id+`"`)
		})
	}
	t.Run("tps-recovery", func(t *testing.T) {
		check(t, "1890", "tps-tokens.js", metrics.DataReceivedMetric, true, true, true)
		check(t, "1891", "tps-default.js", metrics.DataReceivedMetric, false, false, true)
	})
	runtime.AssertAlive(t)
}
