# HTTP k6 reference adapter

Pinned engine: [k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0). Public SDK: [v0.3.0](https://github.com/reqfleet/engine-sdk/tree/v0.3.0). Metrics: `shibuya-v1`. See the [root guide](../README.md) for manual onboarding, publication prerequisites, image release and operator responsibilities.

## Supported plan

Upload a single bundled JavaScript HTTP test with a default function. Optional named `setup` and `teardown` functions are forwarded, including setup data. Native imports such as `k6/http`, `k6` and `k6/data` remain native imports. Bundle third-party JavaScript helpers into the primary `.js` before uploading; extra modules, arbitrary directory archives, browser/gRPC reporting and uploaded dependency manifests are not supported. Custom `handleSummary` is deliberately not forwarded: results use the adapter's CSV-normalization path.

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

Collection scheduling owns these options: `scenarios`, `vus`, `duration`, `iterations`, `stages`, `executionSegment`, `executionSegmentSequence`. Presence of any on uploaded `options`, even null, fails inside k6 with `k6 scheduling options are managed by the collection`. A missing default function fails with `k6 plan must export a default function`. Other options, including native threshold selectors, are retained. If `options.systemTags` is explicit, it must be an array; the wrapper adds `status` without discarding other tags. Default k6 tags remain unchanged when it is absent.

The adapter creates `Workspace.Temp/entry.js` atomically and leaves `/test-data/script.js` byte-for-byte original. Preparation respects cancellation and invalidates previously prepared run IDs. Only that successfully prepared run ID may build a command. Command execution is direct, not shell interpolation:

```text
/usr/local/bin/k6 run --no-usage-report --out csv=<run.ResultFile> <generated-entry-path>
```

Working directory is `Workspace.Inputs`; `K6_CSV_SAVE_INTERVAL=1s` is explicit. SDK v0.3.0 owns process groups, wait/reap, cancellation, SIGTERM and the default ten-second escalation timeout. Nonzero k6 exits, including threshold failures, remain failures rather than being reported as successful load tests. No second process supervisor runs here.

## CSV inputs

Use absolute paths, e.g. `open('/test-data/targets.csv')`, because the generated wrapper is in a different directory. See [csv.js](testdata/csv.js) and [targets.csv](testdata/targets.csv). That small example uses headerless, unquoted, one-URL-per-line data; bundle a real CSV parser if your input needs quoted fields or headers.

`replicated` and `materialized` assignments are consumed whole. The coordinator already chooses whether each agent receives all CSV rows or its materialized subset. The adapter must not shard those bytes again using agent ID, VU ID or shard metadata. `native` and unknown assignment modes fail explicitly. Local per-VU iteration through the supplied data is allowed; it is not a second cross-agent partition. The SDK provides run-local files and resets them before the next run.

## Accurate aggregate metrics

k6 CSV contains independent metric points, **not** complete request events. Passing each row through the SDK request-event parser would fabricate requests/latency/bytes. This adapter instead runs its own synchronized normalized endpoint on **127.0.0.1:6566/metrics**, started before the SDK. A bind failure aborts startup. The SDK's authenticated `/metrics` endpoint on 8080 proxies it with `NativeMetricsSchema: shibuya-v1`; `MetricParser` is nil and native SDK folding is off.

All request-labelled families use `label="k6"`; `label_limit=0` uses the SDK convention `label="others"`. `label_limit` defaults to 10 and must be a nonnegative decimal integer. The exporter never exports original request-name tags. This truthful aggregate contract does **not** claim per-request-name byte attribution.

| CSV point | Normalized effect |
|---|---|
| `http_req_duration` | One latency observation in milliseconds and one status count, using its HTTP status tag; status `0` represents transport failure |
| `vus` | Set latest actual per-agent concurrency, including genuine zero; no request count |
| `data_received` | Add received-byte delta to the historical `shibuya_egress_bytes_counter`; no request count |
| `http_reqs`, `data_sent`, other names | Ignore; no double counting and no synthetic request observations |

The exporter resolves required CSV header names, uses `encoding/csv` including quoted/multiline values, retains its committed offset, retries incomplete tails and processes records once under the same lock for observation and scrapes. Invalid recognized values, complete malformed records, required-column errors and post-start truncation/read errors fail instead of producing fabricated data. Readiness stays pending until a valid standard sample is available. The SDK separately handles a process that exits before readiness is polled.

Collection, plan, run, agent and zone identity come from the current `RunContext`. Histogram boundaries and names follow the public SDK schema. Resource gauges use real cgroup memory and CPU-usage deltas over elapsed wall time (v2 microseconds; v1 nanoseconds). CPU is not invented before a second sample. Unavailable resources fail scrapes clearly. The listener persists after k6 exits so final CSV rows can be drained and cumulative values stay available until the next preparation. Reset clears identities, collectors, cursor and CPU baseline together, including preparation attempts that subsequently fail settings validation. Quiet request periods do not insert fake zero concurrency. Raw CSV is disposable run-local SDK output, not permanent result storage.

## Container and operational failures

`../Makefile` builds `k6/Dockerfile`'s `runtime` target. Official v2.3.0 archives are verified against the published checksum file, itself pinned to the release metadata checksum. Both Linux amd64 and arm64 are supported. The CA-equipped runtime executes as UID/GID **65532**, with writable `/test-data` and `/shibuya-agent/{test-conf,test-result,run-tmp}` and `AGENT_ROOT=/shibuya-agent`.

The SDK owns port 8080, `/healthz`, coordinator TLS/CA/authentication and lifecycle. The loopback metrics port is not exposed outside the container. Supply existing coordinator settings/credentials at deployment; none are baked into the image. Do not loosen scheduler security settings to run the adapter. The harness root process is a test-only privileged-port requirement; it explicitly starts the adapter non-root.

- Missing or blank deployment image: install an approved immutable image digest; uploading JavaScript cannot register an engine.
- Image pull error: check digest, package visibility, architecture and `pull_secret`.
- Startup error: inspect lifecycle/error logs for strict settings, invalid script/default export, managed scheduling options, missing bundled helpers or wrong CSV path.
- Failed threshold: k6 exits nonzero; inspect thresholds and actual target behavior, not just metric availability.
- Scrape failure: inspect CSV corruption and cgroup availability; do not replace unavailable resource samples with zero.
- Updated artifact/settings: trigger after the previous run ends; the same pod can reuse its agent process without old source or counters.
- Updated image: roll out compatible configuration, then purge/relaunch collections. Rollback follows the same process using the previous tested tuple/digest.

Conformance is a real public-protocol runtime check, not a guarantee that a particular Shibuya deployment has been integrated. Record the actual exercised `(Shibuya release, adapter image digest, SDK v0.3.0, k6 v2.3.0, metrics shibuya-v1)` tuple and live smoke results before production use.
