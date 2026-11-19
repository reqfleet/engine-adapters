# HTTP k6 reference adapter

Default engine: [k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0), configurable at image build time. Default public SDK: [merged-main snapshot 6f58d26](https://github.com/reqfleet/engine-sdk/commit/6f58d2664c2651723a1afe05c5d6f867de6919aa), pinned in `go.mod` as `v0.5.1-0.20261118014957-6f58d2664c26` for PR #47's run-scoped `RunContext.Metrics` recorder and accepted-ID clearing, shared LLM instrumentation source names and SDK-owned LLM response, TTFT, inter-token timing and token-receipt recording, assigned identity, request admission, SDK-owned collectors, custom ingestion and the optional public conformance toolkit. This pin is a snapshot, not a published SDK release. Metrics: `shibuya-v1`. See [AGENTS.md](../AGENTS.md) for detailed development, onboarding and release instructions.

## Supported plan

Upload a single bundled JavaScript HTTP test with a default function. Optional named `setup` and `teardown` functions are forwarded, including setup data. Native imports such as `k6/http`, `k6` and `k6/data` remain native imports. Bundle third-party JavaScript helpers into the primary `.js` before uploading; extra modules, arbitrary directory archives, browser/gRPC reporting and uploaded dependency manifests are not supported. Custom `handleSummary` is deliberately not forwarded: results use the adapter's CSV-normalization path.

Plans run only on the checksum-pinned k6 binary packaged in the image. Automatic extension resolution is disabled for inspection, dependency checking and workload execution, even if the deployment sets `K6_AUTO_EXTENSION_RESOLUTION=true`. Unsupported `k6/x/...` imports and `"use k6 ..."` requirements that need a different engine or extension fail preparation with readable diagnostics; the adapter never provisions a replacement binary. Compatible engine constraints remain supported. Select and verify a different engine at image build time, not through an uploaded plan; custom-binary support requires a separate provenance, licensing and conformance contract.

[Minimal HTTP example](testdata/http.js):

```js
import http from 'k6/http';
import { sleep } from 'k6';

export const options = { thresholds: { http_req_failed: ['rate<0.01'] } };
export default function () {
  http.get('https://test.k6.io/');
  sleep(1);
}
```

Only load-test systems you are authorized to test. Replace the example URL with your target. Upload the script in a deployment whose operator installed the adapter and whose Shibuya release admits k6. There is no API-side JavaScript execution/preflight: upload admission is not proof of valid syntax or startup success.

## Settings and scheduling

The coordinator delivers an explicit per-agent string-valued settings object:

```json
{"plan_name":"http-check","concurrency":"2","duration":"1","rampup":"10"}
```

- `concurrency`: positive integer **VUs per agent**, never redivided. Two agents with `2` run four VUs total. Values must fit JavaScript's exact integer range (up to 9007199254740991); practical capacity remains limited by k6/resources.
- `duration`: positive integer **minutes**, converted to seconds. Values must fit k6's nanosecond duration representation (up to 153722867 minutes).
- `rampup`: nonnegative integer **seconds within the total duration**, at most duration × 60.
- `plan_name` is informational; it is not interpreted as a command or scheduling option.

The three numeric settings are required. Missing, null, nonstring, negative, signed, whitespace, fractional, exponential and overflowing inputs fail preparation. With rampup zero, the wrapper uses one `constant-vus` scenario. Positive rampup uses `ramping-vus`, starts at zero, ramps to assigned VUs and holds for the remaining duration; a zero-length hold is omitted. Both have `gracefulStop: '0s'`; ramping also has `gracefulRampDown: '0s'`.

Collection scheduling owns these options: `scenarios`, `vus`, `duration`, `iterations`, `stages`, `executionSegment`, `executionSegmentSequence`. Presence of any on uploaded `options`, even null, fails inside k6 with `k6 scheduling options are managed by the collection`. A missing default function fails with `k6 plan must export a default function`. Other options, including native threshold selectors, are retained. If `options.systemTags` is explicit, it must be an array; the wrapper adds missing `status`, `url` and `error_code` tags without discarding other tags. Default k6 tags remain unchanged when it is absent.

The adapter creates `Workspace.Temp/entry.js` atomically and leaves `/test-data/script.js` byte-for-byte original. Preparation respects cancellation and invalidates previously prepared run IDs. Only that successfully prepared run ID may build a command. Command construction does not launch a process; the SDK checks cancellation immediately before launch.

Before marking the run prepared, `BeforeStart` executes `/usr/local/bin/k6 inspect <generated-entry-path>` in `Workspace.Inputs`. Inspection errors, including k6's syntax diagnostics and source location, are returned through the SDK startup failure to the control plane rather than only logged. Cancellation terminates inspection. Inspection evaluates init code (again when the actual run starts), but does not execute setup, VU iterations or teardown; plans must allow repeated init evaluation.

After inspection, `/usr/local/bin/k6 deps --json <generated-entry-path>` checks whether the packaged binary satisfies the plan's dependencies. This separate check is necessary because k6 skips version-constraint enforcement during inspection when automatic resolution is disabled. A custom-build requirement, dependency-check error or missing compatibility result fails preparation. Image overrides must support this dependency-check command and JSON contract; unsupported versions fail closed rather than silently ignoring requirements.

Inspection stdout is parsed as k6 options JSON to select the received-byte, LLM timing and token-event sources before launch. Stderr capture is limited to its final 16 KiB while excess output is drained; oversized diagnostics include a truncation marker.

Command execution is direct, not shell interpolation:

```text
/usr/local/bin/k6 run --no-usage-report --out csv=<run.ResultFile> <generated-entry-path>
```

Working directory is `Workspace.Inputs`; `K6_CSV_SAVE_INTERVAL=1s` and `K6_AUTO_EXTENSION_RESOLUTION=false` are explicit for all three invocations. The SDK owns process groups, wait/reap, cancellation, SIGTERM and the default ten-second escalation timeout. Nonzero k6 exits, including threshold failures, remain failures rather than being reported as successful load tests. No second process supervisor runs here.

## CSV inputs

Use absolute paths, e.g. `open('/test-data/targets.csv')`, because the generated wrapper is in a different directory. See [csv.js](testdata/csv.js) and [targets.csv](testdata/targets.csv). That small example uses headerless, unquoted, one-URL-per-line data; bundle a real CSV parser if your input needs quoted fields or headers.

`replicated` and `materialized` assignments are consumed whole. The coordinator already chooses whether each agent receives all CSV rows or its materialized subset. The adapter must not shard those bytes again using agent ID, VU ID or shard metadata. `native` and unknown assignment modes fail explicitly. Local per-VU iteration through the supplied data is allowed; it is not a second cross-agent partition. The SDK provides run-local files and resets them before the next run.

## URL-grouped request metrics

The exporter **collects metrics by parsing the CSV file** written by `k6 run --out csv=<run.ResultFile>`. It reads new records with Go's `encoding/csv` and updates the corresponding SDK-owned `shibuya-v1` collectors. The SDK's HTTP endpoint exposes those values; it is not the collection source.

The SDK `MetricParser` callback is not suitable for these rows: it represents complete requests, whereas k6 CSV contains independent samples. Passing a `vus`, `data_received`, custom byte-counter or custom timing row through that callback would fabricate request measurements. The exporter instead calls `MetricsRecorder.ObserveRequest`, `ObserveLLMResponse`, `ObserveTTFT`, `ObserveInterTokenLatency`, `RecordReceivedToken`, `AddAggregateReceivedBytes`, `AddReceivedBytes`, or `SetConcurrency` independently. It defines no Prometheus collectors and does not call the complete-event `RecordEvent` method.

The entry point resolves deployment options once, creates a zero-value exporter with `&metrics.Exporter{}`, and sets `CustomIngestMetrics` to `exporter.Collect`. The exporter needs no label limit or constructor validation. It does not inject a registry, and the exporter neither constructs nor retains a recorder. The SDK supplies `run.Metrics` after successful launch: a fresh recorder, admission state and registry for that run, shared by its live and final callbacks. This callback handles k6's independent CSV samples rather than complete-request events. The SDK invokes it after launch, every 250ms and once after process exit. HTTP requests only gather stored values: there is no wrapper, extra listener or proxy. The SDK owns `/metrics` on port 8080, bearer authentication, resource sampling and exposition. `MetricParser`, `MetricsHandler`, `MetricsURL`, `NativeMetricsSchema`, and folding options are unset. `ResultFile` names k6's CSV output only; the adapter retains parsing, normalization, source attribution and ingestion-state ownership, not a request-name admission map.

### Metric attribution

"Per request" means cumulative series grouped by the emitted request URL/grouping name, not a separate series for every call. All series also carry the assigned run and agent identity.

| Metric | Attribution |
|---|---|
| `shibuya_latency_label_milliseconds` | Request URL/grouping name, `ttft`, or `tps`; latency histogram in milliseconds |
| `shibuya_latency_llm_resp_milliseconds` | Optional completed LLM response duration under `llm_resp` (or `others` after admission overflow); wide histogram in milliseconds |
| `shibuya_status_counter` | Request URL/grouping name and outcome; successful received-token counts under `tps` when selected |
| `shibuya_egress_bytes_counter` | Agent aggregate by default; request URL/grouping name with the explicit custom-counter mode below |
| `shibuya_threads_gauge` | Latest k6 `vus` sample for the agent; no request label |
| `shibuya_cpu_gauge` | Container-wide CPU cores; no request label |
| `shibuya_mem_gauge` | Container-wide memory bytes; no request label |

The historical "egress" counter measures **received bytes**, not outgoing traffic or sent-plus-received traffic. It is cumulative; bandwidth is its rate over time. Default byte totals are emitted once under `label="k6"`, not copied onto each request. VUs are not in-flight HTTP requests: a VU may sleep, execute sequential requests, or issue a parallel batch. CPU and memory cannot be attributed to individual requests.

### CSV output format

This excerpt was captured from k6 v2.3.0 with its default system tags, one VU and one HTTP 200 request against a local server. Other metric rows are omitted; the header and shown rows are unchanged.

```csv
metric_name,timestamp,metric_value,check,error,error_code,expected_response,group,method,name,proto,scenario,service,status,subproto,tls_version,url,extra_tags,metadata
http_reqs,1793861283,1.000000,,,,true,,GET,http://127.0.0.1:45775/,HTTP/1.1,default,,200,,,http://127.0.0.1:45775/,,
http_req_duration,1793861283,4.696574,,,,true,,GET,http://127.0.0.1:45775/,HTTP/1.1,default,,200,,,http://127.0.0.1:45775/,,
vus,1793861284,1.000000,,,,,,,,,,,,,,,,
data_received,1793861284,151.000000,,,,,,,,,default,,,,,,,
```

- `metric_name` identifies the measurement; one row is one sample, not one complete request.
- `timestamp` is Unix time in seconds in the default CSV format. It is not a unique request ID; the exporter does not use it to join rows.
- `metric_value` is the numeric sample in that metric's units, described in the mapping below.
- `status` is the HTTP status tag, populated on the HTTP rows but absent from the VU and byte rows in this example. k6 uses `0` when no HTTP response was received.
- `error_code` identifies a k6 request failure, including DNS and TCP errors. It is empty on successful requests; HTTP 4xx/5xx responses also carry an error code, but retain their actual HTTP status in normalized metrics.
- `url` supplies the request metric label. Samples with the same URL share latency and status series; different URLs remain separate within the label budget and byte limit below.
- The remaining columns carry tags or metadata; an empty cell means that field has no value for that sample.

The header depends on the enabled system tags. The exporter locates `metric_name`, `metric_value`, `status`, `url` and optional `error_code` by name rather than fixed positions and ignores unrelated columns. Missing required columns or an empty URL on an HTTP duration or selected custom request-byte sample fail ingestion; aggregate byte and VU samples may leave `url` empty. Values containing commas, quotes or newlines use standard CSV quoting; splitting lines on commas is not sufficient.

### Normalization and run lifecycle

HTTP latency histograms and status counters use the exact CSV `url` value as `label` when it fits the schema and is admitted within the label budget, not the `name` column or a fixed `k6` label. Repeated requests to the same admitted label accumulate in the same histogram and status counters. The adapter does not truncate labels, normalize paths, or remove query strings. Avoid secrets in URL tags and account for query-string cardinality when choosing a label budget.

The entrypoint resolves configuration once with `agent.LoadAgentServerOptions` and starts `agent.NewAgentServer` with the resolved options. The SDK applies `AgentServerOptions.LabelsUsageLimit` when creating each run's recorder; the exporter receives `run.Metrics`, not a label budget. The adapter does not parse environment variables itself. The SDK defaults an unset or empty `label_limit` to 10, accepts decimal integers with an optional sign (for example, `+4`), and rejects negative values, malformed input, whitespace and integer overflow at startup.

With the default pinned SDK, request labels must be **at most 1023 bytes**, measured in UTF-8 bytes rather than characters; see its [request-label contract](https://github.com/reqfleet/engine-sdk/blob/6f58d2664c2651723a1afe05c5d6f867de6919aa/agent/metrics.md#request-label-validation). The SDK recorder validates the original label before admission or recording. Values exceeding the selected SDK's `metricschema.MaxRequestLabelBytes` cause the entire `http_req_duration` sample to be dropped: neither its latency observation nor its status count is recorded. This shares the SDK result-event policy and applies equally to URLs and plan-defined grouping names. Oversized values are not truncated and do not merge into valid prefixes. The exporter treats recorder label rejection as a dropped sample, not a CSV/exporter failure; readiness still requires a valid selected sample. Independent received-byte and VU samples remain unaffected; a selected custom byte sample also undergoes its own label validation. Newer SDK development uses a 255-byte maximum; see [SDK upgrade guidance](../AGENTS.md#configuring-the-engine-sdk-version) before selecting a different version.

The SDK recorder owns the request-name budget and synchronized admission state, using the shared `metricschema.AdmitRequestName` policy and `others` overflow name. The exporter passes original request names without pre-admitting them. A request name identifies a request or request group and is emitted under the unchanged Prometheus `label` key. The first N distinct valid request names observed in each run receive their own series; additional valid names fold into `label="others"`. A source name already equal to `others` does not consume a slot. `ObserveRequest` applies one decision to latency and status. Admitted names remain stable after the budget fills. Rejected names consume no admission slots and are not retained. A zero limit folds every valid request name into `others`, but still drops oversized samples. Each successful launch receives a new SDK recorder with fresh admission and collectors; exporter reset changes only adapter-owned ingestion state, never resets or reuses a recorder.

The source value is always the CSV `url` tag. k6's [request grouping](https://grafana.com/docs/k6/latest/using-k6/http-requests/#group-urls-under-one-tag), such as a custom `tags.name` or `http.url` template, can replace the emitted `url` with a grouped value before CSV export. The same byte limit applies to that grouping value; the adapter cannot reconstruct URLs omitted by k6. To retain distinct labels, keep emitted values within the selected SDK's byte limit, avoid unintended source grouping, and provide enough label slots.

Built-in `data_received` samples lack request URL attribution. By default they remain aggregate under `label="k6"` at every request-name budget, including zero, without consuming request-name slots. Per-URL bandwidth is unavailable, not zero, unless the plan explicitly supplies the custom byte counter below. VUs, CPU and memory remain agent-level metrics.

| CSV point | Normalized effect |
|---|---|
| `http_req_duration` | One latency observation in milliseconds and one status count, grouped by URL within the label budget and byte limit; transport failures use the error statuses below, including when duration is zero |
| `vus` | Set latest actual per-agent concurrency, including genuine zero; no request count |
| `data_received` | In default mode, add received-byte delta to the aggregate historical `shibuya_egress_bytes_counter`; ignore in custom-counter mode |
| `request_received_bytes` | Only in custom-counter mode, add byte delta to `shibuya_egress_bytes_counter` using the exact CSV `url` and shared SDK label admission; no latency, status or concurrency update |
| `llm_response_duration` | Only when selected by `llmResponse`, observe milliseconds in `shibuya_latency_llm_resp_milliseconds` under `llm_resp` (or `others` after admission overflow) |
| `llm_time_to_first_token` | Only when selected by `ttft`, observe milliseconds in the ordinary histogram under `ttft` (or `others` after admission overflow) |
| `llm_token_events` | Only when selected by `tps`, count each unit-valued token observation in `shibuya_status_counter` under `tps`, status `200` (or `others` after admission overflow); no latency, bytes or concurrency update |
| `llm_inter_token_duration` | Only when selected by `interToken`, observe measured milliseconds between successive tokens in the ordinary histogram under `tps` (or `others` after admission overflow); no status, bytes or concurrency update |
| `http_reqs`, `data_sent`, other names | Ignore; no double counting and no synthetic request observations |

#### Opt-in request-byte counter

Stock k6 supports [custom counters with tags](https://grafana.com/docs/k6/latest/javascript-api/k6-metrics/counter/). To supply request-attributed bytes, declare `Counter('request_received_bytes')` and select it in `options.ext.reqfleet.receivedBytes`:

```js
import http from 'k6/http';
import { sleep } from 'k6';
import { Counter } from 'k6/metrics';

const received = new Counter('request_received_bytes');
export const options = {
  ext: { reqfleet: { receivedBytes: 'request_received_bytes' } },
};

export default function () {
  const url = 'https://example.com/products';
  const label = url; // Or a stable grouping name, e.g. 'products'.
  const res = http.get(url, {
    tags: { name: label },
    responseType: 'binary',
  });
  if (res.body instanceof ArrayBuffer) {
    received.add(res.body.byteLength, { url: label });
  }
  sleep(1);
}
```

Omit the selector or leave it empty to use built-in `data_received`. Select `request_received_bytes` for the URL-attributed mode above. Explicitly setting `data_received`, other values or a non-string selector fails preparation. The selector is read from `k6 inspect` before any samples are collected and remains fixed for that run. Existing plans need no changes. Merely declaring a custom counter does not opt in. TPS selection is independent of this byte-source choice.

In custom-counter mode, built-in `data_received` samples are ignored from the start, including samples preceding the first custom point. They are never added to, subtracted from, or used to fill gaps in request-byte totals. If the plan emits no custom samples, the byte family is absent, not fabricated as zero or replaced by aggregate traffic. Instrument every request whose bytes you want reported, including calls in setup/teardown and each batch/async response where applicable. Partial instrumentation produces partial byte totals.

Each `Counter.add` must provide a **measured byte delta**, not a running total, and a nonempty `url` tag. Custom counters do not inherit the preceding HTTP request's tags: supply `url` explicitly, matching the HTTP metric's emitted URL/grouping value. The adapter does not fall back to `name`, infer attribution from row order/timestamps, or check that a corresponding HTTP request exists. CSV does not encode custom metric types; declaring this reserved metric as a Gauge, Rate or Trend is outside the contract.

The SDK's existing byte-length check and shared request-name budget apply to custom points. Repeated labels accumulate; query variants remain distinct within the budget; excess valid names fold into `others`. A custom point may admit a name before its HTTP duration point. Labels exceeding the selected SDK's byte limit drop only that byte sample, without consuming a slot or marking readiness; independent HTTP/VU samples are unaffected. Empty URLs and negative, nonnumeric or nonfinite selected byte values fail ingestion. A genuine measured zero is recorded. Custom samples never create latency observations or status counts. Collection, final drain, repeated-scrape retention and reset follow the same lifecycle as the other metrics; reset also restores default source selection before preparing the next plan.

**The example measures response-body size, not full network bandwidth:**

- `responseType: 'binary'` makes `byteLength` a byte count; JavaScript string `.length` is not a byte count for Unicode.
- k6 returns the body after supported decompression. The count excludes response headers, transfer framing and TLS/connection overhead and need not equal built-in `data_received`.
- The example retains response bodies, overriding body-discard behavior for that call and increasing memory use. Without a body or another trustworthy byte measurement, omit the sample; do not substitute `Content-Length` or an invented zero.
- With automatic redirects, k6's returned response contains only the final response. This example does not count intermediate response bodies, even though native latency/status samples include those HTTP attempts.
- This is explicit plan instrumentation, not automatic interception of every request. The adapter reports the supplied measurements and cannot recover omitted bytes.

#### Opt-in LLM timing histograms

The SDK's [engine-neutral LLM streaming contract](https://github.com/reqfleet/engine-sdk/blob/main/agent/metrics.md#llm-streaming-metrics) defines measurement boundaries, normalized families, units, labels and failure semantics for all engines. The following is k6's source mapping and opt-in instrumentation, not a separate LLM specification. The link follows the SDK development contract; it does not change the pinned runtime version listed above.

Select any of these fixed custom [Trend](https://grafana.com/docs/k6/latest/javascript-api/k6-metrics/trend/) sources independently in `options.ext.reqfleet`:

```js
import { Trend } from 'k6/metrics';

const responseDuration = new Trend('llm_response_duration', true);
const timeToFirstToken = new Trend('llm_time_to_first_token', true);
const interTokenDuration = new Trend('llm_inter_token_duration', true);
export const options = {
  ext: {
    reqfleet: {
      llmResponse: 'llm_response_duration',
      ttft: 'llm_time_to_first_token',
      interToken: 'llm_inter_token_duration',
    },
  },
};
```

Add one **measured elapsed time in milliseconds** per completed LLM response to `responseDuration`, one per observed first generated token to `timeToFirstToken`, and one gap between successive individually observed tokens to `interTokenDuration`. These are individual observations, not cumulative values, averages or percentiles. The `true` argument identifies timing Trends; it does not convert seconds to milliseconds. CSV does not encode custom metric types, so declaring these reserved sources as Counters, Gauges or Rates is outside the contract.

Keep the previous-token timestamp local to each generation. The first token has no inter-token gap; each later token records the elapsed time since the previous token. Ignore greetings, empty/control events and completion markers without moving that timestamp. A stream with N individually observed tokens supplies N-1 gaps, independently of its N TPS observations. Retain measured gaps on interruption, but never invent timings within a batched payload or carry the timestamp into another stream. Measured zero and fractional milliseconds are valid; missing instrumentation leaves the histogram absent.

Instrument the request-start, generated-token arrival and protocol-completion boundaries defined by the SDK contract. Stock k6's buffered HTTP response does not expose streaming token arrival: `response.timings.waiting` is not TTFT, and a multi-token buffered response cannot recover TTFT or inter-token gaps. Use a client with actual token callbacks for these measurements. Automatic extension resolution remains disabled; selecting these Trends does not add an LLM or streaming client.

For a non-streaming endpoint, a complete runnable response-duration example is:

```js
import http from 'k6/http';
import { Trend } from 'k6/metrics';

const responseDuration = new Trend('llm_response_duration', true);
export const options = {
  ext: { reqfleet: { llmResponse: 'llm_response_duration' } },
};

export default function () {
  const started = Date.now();
  const response = http.post('https://example.com/v1/chat/completions',
    JSON.stringify({ model: 'example-model', messages: [{ role: 'user', content: 'Hello' }], stream: false }),
    { headers: { 'Content-Type': 'application/json' } });
  const elapsed = Date.now() - started;
  if (response.status === 200 && response.json('choices.0.message.content') != null) {
    responseDuration.add(elapsed);
  }
}
```

This non-streaming example intentionally reports only completed-response duration. Adapt its completion/error detection to the endpoint protocol and follow the SDK contract for unavailable measurements and interrupted generations.

Omitting a selector or setting it to `""` disables that measurement. The only accepted nonempty values are the exact names shown above; other strings and non-string values (including `null`) fail preparation. Merely declaring a Trend does not opt in. Inspection fixes all timing selections before launch; reset disables them before inspecting the next plan. Unselected points are ignored, including invalid numeric values. Selected negative, nonnumeric or nonfinite observations fail ingestion.

Timing points need no `url`, `name` or `status` values. Their CSV tags do not change the reserved labels. The exporter calls SDK `ObserveLLMResponse`, `ObserveTTFT`, and `ObserveInterTokenLatency` for response duration, first-token timing, and inter-token timing respectively. The SDK owns all four LLM instrumentation source-name constants in `metricschema`, as well as reserved labels, histogram routing, numeric validation and shared admission with HTTP, token counts and custom bytes. k6 imports those constants directly and retains its selectors, CSV parsing and source validation, without local definitions or aliases. SDK overflow and histogram-family rules apply unchanged. HTTP groups named `ttft` or `tps` share those ordinary labels; HTTP groups named `llm_resp` still use the ordinary histogram. Avoid those grouping names when distinct reporting is needed.

Timing observations never increment status counters, add received bytes, or change VUs. HTTP latency/outcomes and the selected byte source remain independent. Identity binding, final drain, retained scrapes and reset use the same SDK-owned lifecycle as other samples.

#### Opt-in token-stream TPS

The SDK's [shared LLM contract](https://github.com/reqfleet/engine-sdk/blob/main/agent/metrics.md#llm-streaming-metrics) defines TPS as received-token throughput, not payload bandwidth. k6 passes explicit token observations to SDK `RecordReceivedToken`, which owns their classification in the existing status counter used by the TPS chart; no dashboard change is needed.

Declare the fixed token-event Counter and select it independently of byte and timing sources:

```js
import { Counter } from 'k6/metrics';

const tokens = new Counter('llm_token_events');
export const options = {
  ext: { reqfleet: { tps: 'llm_token_events' } },
};
```

- Call `tokens.add(1)` in the streaming callback **once per received generated token**, including the first token. Recording TTFT must not skip that token's TPS observation. Payload byte length, character length and elapsed milliseconds are not token counts.
- For an SSE endpoint guaranteeing one token per token-bearing `data:` event, each such event produces one observation. SSE can split one event across multiple `data:` lines, and network reads can split or combine events: parse the protocol before counting. Ignore empty/control events, role-only deltas, heartbeats and `[DONE]`. If a server batches tokens, instrument each known token; do not assume one message is one model token when cardinality is unknown.
- Each CSV point must have value **exactly `1`**. Batched `add(n)`, cumulative totals, rates, zero, negative, fractional and nonfinite values fail ingestion when selected. Emit no sample when no token arrives. CSV does not encode the custom metric type; Gauge, Rate and Trend declarations are outside this Counter contract.
- The exporter calls SDK `RecordReceivedToken()` once per point. The SDK supplies `label="tps"` and `status="200"`: a successful token receipt, not another HTTP response. CSV `url`, `name` and `status` tags cannot override that attribution. Stream/request failures belong to their own outcome reporting, not extra `tps` observations.
- Omit `tps` or set it to an empty string to disable ingestion. Any other selector value or non-string selector fails preparation. Selection is fixed by inspection before launch; declaring the Counter alone does not opt in. Unselected points are ignored, and reset disables TPS before the next plan's inspection.
- Token observations create no latency, byte or concurrency samples. The `llmResponse`, `ttft`, `interToken`, native HTTP metrics and `receivedBytes` selections remain independent. Missing token observations stay unavailable, without byte-derived estimates or fabricated zero series.
- Counts use SDK-owned identity, shared admission, retention and final drain. Repeated scrapes do not recount tokens; cancellation retains tokens already received. Overflow folds `tps` into `others`, which may also contain HTTP outcomes and must not be presented as isolated token throughput. Avoid HTTP grouping names that collide with `tps`.

The existing TPS chart derives the rate from `shibuya_status_counter{label="tps"}`. The separate Inter-token Latency chart reads `shibuya_latency_label_milliseconds{label="tps"}` when `interToken` is selected and measured. Gaps are not the TPS denominator, and timing observations never increment the token counter. Either measurement can be selected alone; bandwidth remains a separate byte-counter measurement.

Use a client with actual streaming callbacks. The stock k6 image supports WebSocket callbacks, used by conformance, but this selector does not add an SSE client or extension. Buffered `k6/http` responses cannot recover live token arrivals; unsupported extension imports remain rejected by the pinned-engine policy.

##### Complete WebSocket plan

[`testdata/llm-websocket.js`](testdata/llm-websocket.js) is an uploadable plan for the Reqfleet mock `/websocket` endpoint using stock `k6/websockets`. It excludes the `websocket connected` greeting from token metrics, records TTFT on the first generated token, counts every token including the first, records inter-token gaps from the second token onward, and records `llm_resp` only after a normal WebSocket close (code `1000`). Transport errors, invalid payloads and the three-minute client deadline fail the stream check without inventing completion latency; already received tokens, TTFT and gaps remain recorded. Each iteration starts with no previous-token timestamp.

For live payload bandwidth, the plan selects `receivedBytes: 'request_received_bytes'` and counts every received text message before token parsing, including greetings, control text and invalid payloads. `TextEncoder` measures UTF-8 bytes, tagged with the exact WebSocket URL. These deltas reach `shibuya_egress_bytes_counter` while the stream remains open, subject to CSV ingestion and scrape intervals. Native `data_received`, normally emitted at VU iteration completion, is ignored for the entire run to avoid double-counting.

This measures decoded text payloads re-encoded as UTF-8, not full wire bandwidth: HTTP upgrade headers, WebSocket framing and TLS overhead are excluded. Binary messages fail this sample's token parser and are not assigned invented byte counts. Stream failures or interruptions retain payload bytes already measured. Allow four distinct labels to keep the URL, `ttft`, `tps` and `llm_resp` separate under the shared SDK label budget.

The plan measures callback timings with `exec.instance.currentTestRunDuration` from [`k6/execution`](https://grafana.com/docs/k6/latest/javascript-api/k6-execution/#instance), in fractional milliseconds. In k6 v2.3.0 this clock is wall-time-based rather than monotonic, excludes pauses, and is unavailable in `teardown()`. Local clock adjustments and callback scheduling can affect observed arrival gaps.

The deployed mock server must include the normal-close behavior in `testserver/testhttp/websocket.go`. Older servers that simply drop TCP will fail this plan's completion check. Updating a checkout does not update the public mock service.

Upload the file with a TPS-capable adapter and configure VUs/duration through the collection settings. The mock emits 100–10,000 tokens per stream; at 60 tokens/second, allow at least three minutes for an initial stream to finish. A scheduled stop may interrupt a later stream. To run one iteration directly from the repository root:

```sh
k6 run --vus 1 --iterations 1 \
  -e LLM_WS_URL='ws://127.0.0.1:8080/websocket?tokens_per_second=60&ttft_ms=200' \
  k6/testdata/llm-websocket.js
```

The file defaults to `wss://sgra.reqfleet.io/websocket` with the same rate and TTFT query parameters. For another endpoint, change the URL, the `isToken` parser and the completion condition to match its protocol; keep the metric recording independent of the transport.

Failed-stream logs include the native k6 error, connection phase, close code/reason, received-token count and elapsed milliseconds. The first parser or deadline failure is preserved rather than overwritten by a secondary transport error. `phase=handshake` means no WebSocket connection opened; `close=1006` identifies an abnormal disconnect, not successful completion. These details are diagnostic logs, not metric labels.

TPS status counts (`shibuya_status_counter{label="tps",status="200"}`), first-token latency (`shibuya_latency_label_milliseconds` with `label="ttft"`) and inter-token latency (the same family with `label="tps"`, from the second token) should appear while a stream is still open. Only `llm_resp` waits for successful completion. If k6's CSV or summary contains these custom measurements but the normalized series are absent, check the **deployed adapter runtime image**, not just the plan or k6 version. Older adapters ignore selectors they do not support. `make -C k6 test-conformance` builds a separate test image; it does not update the runtime image. Build the runtime with `make -C k6 image`, load it into the local cluster when applicable, then Purge/Launch the collection to replace its agents. Trigger alone reuses the existing adapter binary.

#### Transport failures

When k6 emits `status=0`, the normalized `shibuya_status_counter` uses a stable, readable description with the native error code instead of an apparent HTTP status of `0`. The code comes from the same duration sample as the URL; no cross-row matching is used. Common [k6 error codes](https://grafana.com/docs/k6/latest/javascript-api/error-codes/):

| k6 `error_code` | Normalized `status` |
|---|---|
| `1100` | `DNS lookup error (k6 1100)` |
| `1101` | `DNS host not found (k6 1101)` |
| `1211` | `TCP connection timeout (k6 1211)` |
| `1212` | `TCP connection refused (k6 1212)` |
| `1050` | `HTTP request timeout (k6 1050)` |

Known network, TCP, TLS and request errors receive fixed descriptions; HTTP/2 codes include their error family and native code. Unrecognized numeric codes remain visible as `Unknown k6 error (k6 <code>)`. If error metadata is absent or empty, the status is `Transport error (cause unavailable)`: a failure is known, but its cause is unavailable. Free-form error messages are not metric labels because they can contain changing hosts, addresses and ports. Real HTTP responses keep their numeric status, e.g. HTTP 500 remains `status="500"`.

DNS failures and refused connections can have `http_req_duration=0`: k6's request duration excludes DNS lookup and connection establishment. Those samples still produce a histogram observation and an error count. An all-failing run remains observable, with cumulative URL-grouped error metrics retained after it stops; zero latency or zero received bytes does not mean there were no attempts.

#### Collection lifecycle

The exporter resolves required CSV header names, uses `encoding/csv` including quoted/multiline values, retains its committed offset, retries incomplete tails and processes records once in `Collect`. The SDK serializes collection. A short mutex publishes readiness/error transitions to concurrent `Observe` calls; no lock is held during file I/O, parsing or SDK recording. The collector is the only status writer after launch and can read its own status without locking. Invalid selected values, complete malformed records, required-column errors and post-start truncation/read errors fail collection instead of producing fabricated data. The SDK fails the run and registry scrapes on those errors. Readiness stays pending until collection sees a valid selected sample. Check `/progress`, not the metrics response status: an empty registry is still a successful scrape. The SDK separately handles a process that exits before readiness is polled.

Collection, plan, run, agent and zone identity come from the current `RunContext`, bound by the SDK to that run's recorder at launch. Preparation, `BeforeStart` and `BuildCommand` receive nil `Metrics`; no samples are recorded during preparation. CSV samples supply only measurements and source attribution; the SDK applies the bound identity to request, received-byte, concurrency and resource metrics. The exporter retains the run ID to reject callbacks for another run and the result-file path solely for reading CSV. After matching the run ID, `Collect` requires non-nil `run.Metrics` and returns an explicit error before reading or advancing CSV if it is absent; every sample uses that supplied recorder. The SDK reuses the result-file path across runs, so it is not part of callback matching. The exporter neither stores nor compares collection, plan, agent or zone. The SDK registry defines ordinary and LLM histogram bounds through `metricschema.RequestLatencyBuckets()` and `metricschema.LLMResponseLatencyBuckets()`. Shared conformance validation uses the canonical boundary helpers, with independent fixed-profile fixtures guarding against schema drift. `ObserveRequest` does not reinterpret a grouping value such as `llm_resp` as an LLM measurement; only explicitly selected custom response timings populate the wide histogram.

The SDK samples container resources in a background loop after launch and every five seconds, with a final attempt after joining live monitors. The k6 exporter owns no resource sampler or gauges. Each successfully launched run starts with fresh resource state, assigned identity and a fresh CPU baseline. Completed values remain available without further reads after exit until the next accepted run ID clears published metrics. First use, a new run or a decreasing counter omits both CPU and memory until a complete sample is available. A resource read error warns and skips the update, preserving last-good values without failing HTTP or the workload. See the SDK's [resource metrics contract](https://github.com/reqfleet/engine-sdk/blob/6f58d2664c2651723a1afe05c5d6f867de6919aa/agent/metrics.md#runtime-resource-metrics) for sampling and recovery semantics.

Memory is cgroup-accounted bytes, including cache. Both resources measure all processes in the container's cgroup, not just the k6 PID. The SDK expects that cgroup mounted at `/sys/fs/cgroup`: v2 uses `cpu.stat` and `memory.current`; v1 requires the combined `cpu,cpuacct/cpuacct.usage` and `memory/memory.usage_in_bytes` paths. V1 CPU nanoseconds are truncated to microseconds by the SDK. No host or process fallback is used.

The SDK joins live callbacks and processes, then gives `Collect` the same run's recorder and a fresh two-second context to drain final CSV rows before releasing the workspace. Cumulative values remain available until a new valid run ID is accepted; post-exit scrapes do not read CSV again. Acceptance clears previously published metrics before staging, preparation or startup completes, so failed startup leaves metrics empty. Rejected, duplicate and stale triggers do not clear output. Captured old registry references remain bound to their original run and retain its values; the SDK does not reset or rebind them, and they are not combined into the current endpoint.

After the SDK joins prior callbacks, exporter `Reset(run)` in `BeforeStart` clears only its CSV cursor, path/run ID, source selectors, readiness and parse errors. It accepts nil `run.Metrics` and does not change SDK metrics. Reset and source configuration must finish before live callbacks start; reset does not cancel callbacks. Default aggregate received bytes remain under `k6` at every request-name budget without consuming request-name slots. Quiet request periods do not insert fake zero concurrency. Raw CSV is disposable run-local SDK output, not permanent result storage.

Accepted collection errors remain run failures across concurrent process exit. Final-drain errors or an expired deadline also fail the run, including after coordinator-requested scheduled completion; a successful process exit or stop cannot hide incomplete metrics.

## Container and operational failures

[Makefile](Makefile) builds [Dockerfile](Dockerfile)'s `runtime` target with the repository root as its build context. The default engine is v2.3.0; `K6_VERSION` and its matching `K6_CHECKSUMS_SHA256` build argument select another release. Official archives are verified against the selected release's checksum file, itself checked against the pinned digest. The image includes that release's source and license. See [build configuration](../AGENTS.md#configuring-the-k6-version). Both Linux amd64 and arm64 are supported. The CA-equipped runtime executes as UID/GID **65532**, with writable `/test-data` and `/shibuya-agent/{test-conf,test-result,run-tmp}` and `AGENT_ROOT=/shibuya-agent`.

The same `/usr/share/licenses/engine-adapters/` directory also contains `xk6-dashboard-assets-<version>-source.tar.gz` and `XK6_DASHBOARD_ASSETS_LICENSE`. k6 vendors generated dashboard assets; this additional upstream archive supplies the original UI/report sources and their workspace manifests, lockfile and build configuration. The Docker build derives the dashboard version from the selected k6 source archive's `vendor/modules.txt`, so `K6_VERSION` overrides select matching dashboard source rather than retaining a separate default. Missing dependency metadata or an unavailable source archive fails the build.

`ENGINE_SDK_VERSION` selects the SDK compiled into the image, its bundled notices and image metadata; the default matches the SDK pseudo-version above. Image-only overrides do not change the checkout's dependency files. See [SDK build configuration](../AGENTS.md#configuring-the-engine-sdk-version).

The SDK owns port 8080, `/healthz`, authenticated `/metrics`, coordinator TLS/CA/authentication and lifecycle. Supply existing coordinator settings/credentials at deployment; none are baked into the image. Do not loosen scheduler security settings to run the adapter. The harness root process is a test-only privileged-port requirement; it explicitly starts the adapter non-root.

- Missing or blank deployment image: install an approved immutable image digest; uploading JavaScript cannot register an engine.
- Image pull error: check digest, package visibility, architecture and `pull_secret`.
- Startup error: inspect lifecycle/error logs for strict settings, invalid script/default export, managed scheduling options, missing bundled helpers or wrong CSV path.
- Failed threshold: k6 exits nonzero; inspect thresholds and actual target behavior, not just metric availability.
- Scrape failure: inspect authentication, metrics configuration and CSV ingestion errors. Cgroup read failures warn and skip resource updates without failing the scrape or workload; investigate missing/stale resource gauges separately and never fabricate zero usage.
- Updated artifact/settings: trigger after the previous run ends; the same pod can reuse its agent process without old source or counters.
- Updated image: roll out compatible configuration, then purge/relaunch collections. Rollback follows the same process using the previous tested tuple/digest.

Conformance is a real public-protocol runtime check, not a guarantee that a particular Shibuya deployment has been integrated. Record the actual exercised `(Shibuya release, adapter image digest, selected SDK version, selected k6 version, metrics shibuya-v1)` tuple and live smoke results before production use.

The adapter's `k6/conformance` tests consume the optional public `github.com/reqfleet/engine-sdk/conformance` toolkit from the pinned SDK dependency, not a copied adapter-local harness. Its container suite exercises SDK lifecycle and authenticated serving, validates normalized metrics against real k6 CSV output, and runs positive-overflow, zero-label-budget and 1023/1024/1025-byte URL scenarios, including custom Unicode grouping names, oversized-sample rejection without prefix merging, and unchanged default aggregate bytes. Custom-counter scenarios compare exact binary, UTF-8 and decoded gzip body sizes with request-labelled byte totals, exercise explicit source selection, absent samples, invalid options/samples, source preservation, retention and custom/default/custom reuse. k6-specific hooks, scheduling, assignment and transport-error cases remain in the adapter's tests. `make -C k6 test-unit` also runs the SDK metrics validator's unit tests and validates exporter exposition for ASCII and multibyte UTF-8 URL boundaries, rejected samples followed by valid samples, repeated samples, shared admission across byte/request samples, positive/zero budgets and reset. See [the reusable suite contract](../AGENTS.md#reusable-adapter-conformance) when adding another adapter.

Pinned-engine regressions use a controlled local build-service peer inside the network-isolated container. Positive controls prove that unsupported imports and engine/extension requirements can provision and execute the peer's marker executable without the adapter policy. Adapter runs must make zero provisioning/download requests and never execute that replacement, including with a conflicting inherited resolution setting. Unsupported-plan cases report protocol and terminal-state failures through their own subtests. A post-preparation source mutation independently exercises the workload invocation's guard; ordinary HTTP plans and compatible version constraints still pass the real-engine lifecycle and metrics checks.

The container target first runs shared runtime regressions with a small SDK-based workload, covering completion before the first progress poll, reaping exited orphans while the adapter remains active, and cleanup of descendants in separate process groups/sessions. It then runs the real k6 suite. The shared validator's unit tests cover retained-series uniqueness, histogram invariants, request-label byte limits and optional LLM response histograms. k6 LLM scenarios compare custom timing CSV observations with both normalized histogram profiles, including overflow, zero budget, independent HTTP counts, optional selection, final drain and reused-run reset.

TPS conformance uses stock k6 WebSocket callbacks against a controlled token stream, including different UTF-8 payload sizes, protocol-only messages, interrupted generations and a teardown stream. It compares unit-valued CSV token events with normalized counts, includes the first token, and excludes control messages and completion summaries. It also verifies independent HTTP outcomes and byte-source selection, missing observations, invalid selectors/samples, overflow/zero budgets, retained final metrics and reused-run reset. This proves token-event ingestion, not stock-image SSE client support.
