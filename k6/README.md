# HTTP k6 reference adapter

Default engine: [k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0), configurable at image build time. Default public SDK: [893538a](https://github.com/reqfleet/engine-sdk/commit/893538a901e0), pinned in `go.mod` as `v0.5.1-0.20261112015540-893538a901e0` for the shared request-name admission API. Metrics: `shibuya-v1`. See [AGENTS.md](../AGENTS.md) for detailed development, onboarding and release instructions.

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

Inspection stdout is discarded. Stderr capture is limited to its final 16 KiB while excess output is drained; oversized diagnostics include a truncation marker. This bounds memory and error payloads while retaining the final diagnostic after verbose init logging.

Command execution is direct, not shell interpolation:

```text
/usr/local/bin/k6 run --no-usage-report --out csv=<run.ResultFile> <generated-entry-path>
```

Working directory is `Workspace.Inputs`; `K6_CSV_SAVE_INTERVAL=1s` and `K6_AUTO_EXTENSION_RESOLUTION=false` are explicit for all three invocations. The SDK owns process groups, wait/reap, cancellation, SIGTERM and the default ten-second escalation timeout. Nonzero k6 exits, including threshold failures, remain failures rather than being reported as successful load tests. No second process supervisor runs here.

## CSV inputs

Use absolute paths, e.g. `open('/test-data/targets.csv')`, because the generated wrapper is in a different directory. See [csv.js](testdata/csv.js) and [targets.csv](testdata/targets.csv). That small example uses headerless, unquoted, one-URL-per-line data; bundle a real CSV parser if your input needs quoted fields or headers.

`replicated` and `materialized` assignments are consumed whole. The coordinator already chooses whether each agent receives all CSV rows or its materialized subset. The adapter must not shard those bytes again using agent ID, VU ID or shard metadata. `native` and unknown assignment modes fail explicitly. Local per-VU iteration through the supplied data is allowed; it is not a second cross-agent partition. The SDK provides run-local files and resets them before the next run.

## URL-grouped request metrics

The exporter **collects metrics by parsing the CSV file** written by `k6 run --out csv=<run.ResultFile>`. It reads new records with Go's `encoding/csv` and converts each supported sample into the corresponding `shibuya-v1` metric. Its HTTP endpoint exposes the converted results; it is not the collection source.

The SDK `MetricParser` callback is not suitable for these rows: it returns a `ShibuyaMetric` representing a complete request, and `ToPrometheus` always records latency, increments a status counter, sets concurrency and adds bytes together. k6 CSV instead contains independent metric samples. Passing a `vus` or `data_received` row through that callback would fabricate a request and a latency observation. The exporter therefore parses CSV directly and updates only the relevant metric family.

The exporter is passed directly as `AgentServerOptions.MetricsHandler` with `NativeMetricsSchema: shibuya-v1`. The SDK serves it behind bearer authentication at `/metrics` on port 8080; there is no extra TCP listener or loopback proxy. `MetricParser` and `MetricsURL` are unset and native SDK folding is off. `ResultFile` names k6's CSV output only; the adapter owns normalization, URL admission and reset.

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

The header depends on the enabled system tags. The exporter locates `metric_name`, `metric_value`, `status`, `url` and optional `error_code` by name rather than fixed positions and ignores unrelated columns. Missing required columns or an empty URL on an HTTP duration sample fail ingestion; non-request samples may leave `url` empty. Values containing commas, quotes or newlines use standard CSV quoting; splitting lines on commas is not sufficient.

### Normalization and run lifecycle

HTTP latency histograms and status counters use the exact CSV `url` value as `label` when it fits the schema and is admitted within the label budget, not the `name` column or a fixed `k6` label. Repeated requests to the same admitted label accumulate in the same histogram and status counters. The adapter does not truncate labels, normalize paths, or remove query strings. Avoid secrets in URL tags and account for query-string cardinality when choosing a label budget.

The entrypoint resolves configuration once with `agent.LoadAgentServerOptions`, passes `AgentServerOptions.LabelsUsageLimit` to the exporter, and starts `agent.NewAgentServer` with the resolved options. The adapter does not parse environment variables itself. The SDK defaults an unset or empty `label_limit` to 10, accepts decimal integers with an optional sign (for example, `+4`), and rejects negative values, malformed input, whitespace and integer overflow at startup.

Request labels must be **shorter than 1024 bytes**, measured in UTF-8 bytes rather than characters. The adapter validates them with the SDK's `agent.ShibuyaMetric.LabelIsValid()`. Values of 1024 bytes or more cause the entire `http_req_duration` sample to be dropped before admission: neither its latency observation nor its status count is recorded. This matches SDK result-event ingestion and applies equally to URLs and plan-defined grouping names. Oversized values are not truncated and do not merge into valid prefixes. Dropping these samples does not latch a CSV/exporter error; readiness still requires a valid standard sample. Independent received-byte and VU samples remain unaffected.

The exporter uses the SDK's `metricschema.AdmitRequestName` helper and `metricschema.OverflowRequestName` constant (`others`) to enforce the request-name budget. A request name identifies a request or request group and is emitted under the unchanged Prometheus `label` key. The first N distinct valid request names observed in each run receive their own series; additional valid names fold into `label="others"`. A source name already equal to `others` does not consume a slot. Latency and status share the same decision. Admitted names remain stable after the budget fills. Rejected names consume no admission slots and are not retained. A zero limit folds every valid request name into `others`, but still drops oversized samples. Reset clears the admitted name set along with the metrics.

The source value is always the CSV `url` tag. k6's [request grouping](https://grafana.com/docs/k6/latest/using-k6/http-requests/#group-urls-under-one-tag), such as a custom `tags.name` or `http.url` template, can replace the emitted `url` with a grouped value before CSV export. The same byte limit applies to that grouping value; the adapter cannot reconstruct URLs omitted by k6. To retain distinct labels, keep emitted values below 1024 bytes, avoid source grouping, and provide enough label slots.

Received-byte samples lack request URL attribution and remain aggregate under `label="k6"` (`label="others"` when the limit is zero). They do not consume URL admission slots or create byte series for individual URLs. Per-URL bandwidth is unavailable, not zero. VUs, CPU and memory remain agent-level metrics.

| CSV point | Normalized effect |
|---|---|
| `http_req_duration` | One latency observation in milliseconds and one status count, grouped by URL within the label budget and byte limit; transport failures use the error statuses below, including when duration is zero |
| `vus` | Set latest actual per-agent concurrency, including genuine zero; no request count |
| `data_received` | Add received-byte delta to the aggregate historical `shibuya_egress_bytes_counter`; no request count or URL attribution |
| `http_reqs`, `data_sent`, other names | Ignore; no double counting and no synthetic request observations |

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

The exporter resolves required CSV header names, uses `encoding/csv` including quoted/multiline values, retains its committed offset, retries incomplete tails and processes records once under the same lock for observation and scrapes. Invalid recognized values, complete malformed records, required-column errors and post-start truncation/read errors fail instead of producing fabricated data. Readiness stays pending until a valid standard sample is available. The SDK separately handles a process that exits before readiness is polled.

Collection, plan, run, agent and zone identity come from the current `RunContext`. Histogram boundaries and names follow the public SDK schema.

Resource gauges use the SDK's `containerstats.ReadCPUUsage` and `ReadMemoryUsage`. CPU is the cumulative microsecond delta divided by elapsed wall-clock microseconds, yielding average cores consumed; no CPU gauge is emitted before a second successful sample. Memory is cgroup-accounted bytes, including cache. These measurements cover all processes in the container's cgroup, not just the k6 PID. The SDK expects that cgroup mounted at `/sys/fs/cgroup`: v2 uses `cpu.stat` and `memory.current`; v1 requires the combined `cpu,cpuacct/cpuacct.usage` and `memory/memory.usage_in_bytes` paths. V1 CPU nanoseconds are truncated to microseconds by the SDK. Unavailable resources fail scrapes clearly without advancing the CPU baseline; no host or process fallback is used.

The listener persists after k6 exits so final CSV rows can be drained and cumulative values stay available until the next preparation. Reset clears identities, collectors, URL admission, cursor and CPU baseline together, including preparation attempts that subsequently fail settings validation. Quiet request periods do not insert fake zero concurrency. Raw CSV is disposable run-local SDK output, not permanent result storage.

## Container and operational failures

[Makefile](Makefile) builds [Dockerfile](Dockerfile)'s `runtime` target with the repository root as its build context. The default engine is v2.3.0; `K6_VERSION` and its matching `K6_CHECKSUMS_SHA256` build argument select another release. Official archives are verified against the selected release's checksum file, itself checked against the pinned digest. The image includes that release's source and license. See [build configuration](../AGENTS.md#configuring-the-k6-version). Both Linux amd64 and arm64 are supported. The CA-equipped runtime executes as UID/GID **65532**, with writable `/test-data` and `/shibuya-agent/{test-conf,test-result,run-tmp}` and `AGENT_ROOT=/shibuya-agent`.

`ENGINE_SDK_VERSION` selects the SDK compiled into the image, its bundled notices and image metadata; the default matches the SDK pseudo-version above. Image-only overrides do not change the checkout's dependency files. See [SDK build configuration](../AGENTS.md#configuring-the-engine-sdk-version).

The SDK owns port 8080, `/healthz`, authenticated `/metrics`, coordinator TLS/CA/authentication and lifecycle. Supply existing coordinator settings/credentials at deployment; none are baked into the image. Do not loosen scheduler security settings to run the adapter. The harness root process is a test-only privileged-port requirement; it explicitly starts the adapter non-root.

- Missing or blank deployment image: install an approved immutable image digest; uploading JavaScript cannot register an engine.
- Image pull error: check digest, package visibility, architecture and `pull_secret`.
- Startup error: inspect lifecycle/error logs for strict settings, invalid script/default export, managed scheduling options, missing bundled helpers or wrong CSV path.
- Failed threshold: k6 exits nonzero; inspect thresholds and actual target behavior, not just metric availability.
- Scrape failure: inspect CSV corruption and cgroup availability; do not replace unavailable resource samples with zero.
- Updated artifact/settings: trigger after the previous run ends; the same pod can reuse its agent process without old source or counters.
- Updated image: roll out compatible configuration, then purge/relaunch collections. Rollback follows the same process using the previous tested tuple/digest.

Conformance is a real public-protocol runtime check, not a guarantee that a particular Shibuya deployment has been integrated. Record the actual exercised `(Shibuya release, adapter image digest, selected SDK version, selected k6 version, metrics shibuya-v1)` tuple and live smoke results before production use.

The adapter's `k6/conformance` tests consume the shared `conformance` package. Its container suite exercises SDK lifecycle and authenticated serving, validates normalized metrics against real k6 CSV output, and runs positive-overflow, zero-label-budget and 1023/1024/1025-byte URL scenarios, including custom Unicode grouping names, oversized-sample rejection without prefix merging, and unchanged aggregate-only bytes. k6-specific hooks, scheduling, assignment and transport-error cases remain in the adapter's tests. `make -C k6 test-unit` also runs the shared metrics validator's unit tests and validates exporter exposition for ASCII and multibyte UTF-8 URL boundaries, rejected samples followed by valid samples, repeated samples, positive/zero budgets and reset. See [the reusable suite contract](../AGENTS.md#reusable-adapter-conformance) when adding another adapter.

Pinned-engine regressions use a controlled local build-service peer inside the network-isolated container. Positive controls prove that unsupported imports and engine/extension requirements can provision and execute the peer's marker executable without the adapter policy. Adapter runs must make zero provisioning/download requests and never execute that replacement, including with a conflicting inherited resolution setting. Unsupported-plan cases report protocol and terminal-state failures through their own subtests. A post-preparation source mutation independently exercises the workload invocation's guard; ordinary HTTP plans and compatible version constraints still pass the real-engine lifecycle and metrics checks.

The container target first runs shared runtime regressions with a small SDK-based workload, covering completion before the first progress poll, reaping exited orphans while the adapter remains active, and cleanup of descendants in separate process groups/sessions. It then runs the real k6 suite. The shared validator's unit tests cover retained-series uniqueness, histogram invariants, request-label byte limits and optional LLM response histograms; k6 itself continues to report the HTTP metrics described above.
