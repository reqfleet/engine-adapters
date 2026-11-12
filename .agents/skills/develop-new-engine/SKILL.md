---
name: develop-new-engine
description: Develop a new Shibuya load-testing engine adapter using the latest released reqfleet engine-sdk. Use when adding an engine kind, integrating an engine runtime, choosing between request-event parsing and normalized metrics, or designing engine metrics reporting. Requires a developer discussion of metric semantics and collection ownership before implementation; uses k6 as a reference.
---

# Develop a new engine

First establish the engine's reporting semantics with the developer, then implement the selected SDK integration. Coordinate Shibuya control-plane changes and rollout with the Reqfleet team.

## 1. Establish the release and scope

- Read the repository's applicable instructions. Paths below are relative to the engine-adapters repository root.
- Resolve the latest stable published SDK release from [GitHub releases](https://github.com/reqfleet/engine-sdk/releases/latest) or the [release API](https://api.github.com/repos/reqfleet/engine-sdk/releases/latest). Record the exact tag and source commit; exclude drafts and prereleases unless explicitly requested. Do not assume the repository pin or an SDK README install command is current.
- Read that tag's `go.mod`, `agent/metrics.md`, and relevant public API source before coding. Pin `github.com/reqfleet/engine-sdk` to the resolved tag, never `main` or a floating version. Use its required Go toolchain; align module dependencies, Docker defaults, notices, and adapter compatibility documentation. In this repository, keep the shared module rather than creating a nested module.
- At skill authoring, the latest release was [v0.5.0](https://github.com/reqfleet/engine-sdk/releases/tag/v0.5.0). Its [metrics contract](https://github.com/reqfleet/engine-sdk/blob/v0.5.0/agent/metrics.md) contains the APIs described here. Re-resolve at invocation. Historical prose can still say an API is unreleased: verify against the selected tag's source and release history. If release discovery is unavailable, disclose that limitation; do not call an unverified pin “latest.”
- Inspect the target engine's official output/schema documentation and capture representative real output for the selected engine version. Inspect existing adapters before proposing architecture.
- Establish the exact kind, engine version, primary artifact and unique suffix, supported protocols, scheduling units, per-agent workload assignment, optional CSV delivery, and target Shibuya checkout/release. Infer repo-provided facts; ask only about unresolved product choices.

## 2. Discuss metrics before implementing

Do not silently select a mode because an engine writes CSV or exposes Prometheus. File/HTTP is transport; the decisive distinction is **complete request events versus independent samples or cumulative snapshots**.

Present findings, a recommended mode and its tradeoffs, then ask the developer to resolve material reporting choices. Use short rounds rather than an exhaustive questionnaire. Do not implement the reporting path until its semantics and mode are agreed. If the user has already specified them, restate the decision and proceed without redundant confirmation.

### First round: evidence and desired reports

Show one real successful request, one failed attempt, and any separate concurrency/byte samples or native snapshot. Explain what each source record actually represents. Ask only what the evidence cannot answer:

- What is one counted operation: HTTP attempt, transaction, task, stream, or something else? How do retries, redirects and subrequests affect it?
- Which label should users see: URL, operation, sampler name, or another engine-defined value? Discuss cardinality and secrets. Do not silently discard query strings or replace labels.
- Which reports are required: outcome counts, latency, received bytes, live concurrency, optional LLM timings? Which are available per request versus only per agent?
- What does latency measure: full wall time, response time, or an engine-specific interval? Are pre-response failures recorded, and can their duration legitimately be zero or absent?

Distinguish unavailable data from measured zero. Address missing required reporting data by changing the engine output configuration or adding measured instrumentation where needed; explicitly agree on and document any unsupported reporting.

### Second round: choose one of two reporting approaches

| Approach | Appropriate source | SDK configuration and ownership | Tradeoff |
| --- | --- | --- | --- |
| SDK request-event ingestion | Each result record supplies a complete request event compatible with `agent.ShibuyaMetric` | Set `ResultFile` and `MetricParser`; leave `MetricsHandler`, `MetricsURL`, and `NativeMetricsSchema` unset. SDK tails events, supplies run identity, accumulates collectors, enforces request-label admission and reports resources. | Less exporter code, but coupled event semantics must match the source. |
| Producer-owned normalized exposition | Independent samples, cumulative metrics, or an engine with compatible normalized Prometheus output | Set `NativeMetricsSchema: agent.MetricsSchemaShibuyaV1` and either `MetricsHandler` or `MetricsURL`; leave `MetricParser` nil. Producer owns the complete `shibuya-v1` exposition, identities, normalization, budget, resources and run state. | More explicit control and responsibility; preserves attribution boundaries that do not fit request events. |

The second approach has two delivery variants:

- Prefer **`MetricsHandler`** for an in-process exporter. The SDK mounts the handler on its authenticated `/metrics` route; no adapter listener or loopback proxy. `ResultFile` may still name engine output that the exporter parses itself. The SDK neither tails that file nor merges its registry, resets, closes or caches the handler.
- Use **`MetricsURL`** for an existing HTTP(S) producer supplying the complete normalized schema. Arbitrary engine Prometheus metrics are not automatically compatible. Normalize names, identities, units, buckets and semantics first. Default proxying streams without folding or schema validation; the producer must remain reachable for final scrapes.
- `MetricParser`, `MetricsHandler` and `MetricsURL` are mutually exclusive. Handler mode does not support folding options. URL mode can opt into `FoldNativeMetrics` only as a last-resort budget fallback; review the selected release's buffering, validation and snapshot limits. Folding does not repair missing attribution, incompatible histogram bounds or producer resets.
- Registry-only configuration requires additional request reporting integration. Verify schema conformance with real output and behavioral checks.

**Critical event-path check:** `ShibuyaMetric.ToPrometheus` records latency, increments a status counter, sets concurrency and adds bytes together. Never feed it a VU-only sample, byte-only sample, cumulative snapshot, or synthetic timing record and assume those side effects disappear. If any required event values cannot truthfully be supplied, prefer separate normalized collectors or change the source. Parser errors drop events; rejecting all metadata-poor failures is not an acceptable way to achieve complete failure accounting.

### Record the agreed mapping

Before code, provide a compact mapping table in the conversation, then preserve the agreed contract in the adapter README. Each row must specify:

- Source field/event and evidence; event, delta, cumulative counter, histogram or gauge.
- Target metric family, units and exact conversion.
- Request/agent attribution and label policy; identity source.
- Success, known failure, unknown and missing metadata handling.
- Accumulation, final drain, reset and unavailable-data behavior.

Conclude with the selected approach and delivery variant, why the other approach is unsuitable, unsupported reports, ownership of each stateful component, and concrete acceptance scenarios. Resolve material disagreements with the developer before implementing.

## 3. Apply the reporting contract

Read the selected release's full `agent/metrics.md`; use public `metricschema` constants and label lists rather than inventing a second schema.

| Family | Meaning and attribution |
| --- | --- |
| `shibuya_latency_label_milliseconds` | Classic request histogram in milliseconds; identity + `label` |
| `shibuya_status_counter` | Exactly one outcome per reported attempt; identity + `label` + `status` |
| `shibuya_egress_bytes_counter` | Received/response bytes despite the historical name; identity + available `label` attribution |
| `shibuya_threads_gauge` | Actual current per-agent concurrency; identity only |
| `shibuya_cpu_gauge` | Container CPU cores consumed; identity only |
| `shibuya_mem_gauge` | Container-accounted memory bytes; identity only |

- Identity is `{collection_id, plan_id, run_id, engine_no, zone}`. Event ingestion injects assigned identity. Native producers must supply it from resolved engine metadata and the current run.
- Ordinary histogram finite upper bounds in milliseconds are `1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000`, plus `+Inf`. Preserve cumulative buckets, sum and count. Convert seconds to milliseconds in bounds and sum; preserve counts unchanged. Never average percentiles or invent finer buckets from summaries. Consult the separate SDK profile for optional LLM timings; omit unmeasured timings.
- Optional LLM timing uses lowercase reserved labels: `ttft` in the ordinary histogram and `llm_resp` in the separate LLM response histogram. Preserve legitimate folded `others`/`_other_` series; do not substitute URLs or arbitrary names for reserved LLM response labels.
- Keep counters and histograms cumulative throughout a run, including inactive series. An unchanged second scrape must not increase totals. Retain final metrics after workload exit and drain remaining output; the SDK does not cache native snapshots.
- Validate numeric observations before recording them. Every `MetricParser` must reject negative, NaN or infinite `Latency`, `EgressBytes` and `Threads` before returning a successful `ShibuyaMetric`. SDK v0.5.0 forwards these values to collectors without numeric validation: negative `EgressBytes`, for example, reaches `Counter.Add` and panics. Native producers must validate their numeric observations too. Parser errors drop events; if the source lacks a required measurement, choose a reporting approach that preserves available measurements and failure counts rather than fabricating zeros or silently dropping failures.
- Enforce labels shorter than 1024 bytes and use the resolved `LabelsUsageLimit`; for a new adapter default to stable first-N admission plus `others`, zero budget meaning only overflow. Share the mapping across request families, bound admission memory and reset per run. SDK event ingestion owns label-length checks and label admission, not numeric validation; an in-process handler must implement both label checks and admission itself.
- Keep unavailable per-request bytes unavailable; do not divide aggregate bytes across requests, copy totals to every label, or join independent rows by order/timestamp. Aggregate samples must not consume request-label slots or create request counts. Never infer zero concurrency from quiet request traffic.
- Translate engine error types/codes into deterministic readable descriptions, retaining native codes when available. Preserve actual DNS, refusal, connection-timeout, response-timeout and TLS classifications. Unknown codes remain identifiable; missing cause is an unspecified failure. Do not use raw error messages as labels. Genuine protocol statuses remain distinct from transport failures. Count failed attempts once, including zero-duration and pre-response failures, without inventing latency or bytes that the source did not measure.
- Native exporters must use SDK `containerstats.ReadCPUUsage` and `ReadMemoryUsage`. CPU is cumulative microseconds: delta CPU / actual elapsed wall-clock microseconds gives cores. Omit CPU until two successful samples, use a monotonic clock, reset on run boundaries/counter decrease, and do not advance the baseline on read failures. Memory includes cache. Propagate unavailable resources as scrape failures, never host/process fallback or fake zeros. Document supported cgroup mounts.
- Synchronize ingestion, scrapes and reset. Cover pre-run scrapes, cancellation, partial output, malformed records, truncation, duplicate ingestion, final drain and failed preparation. Reset producer identities, collectors, admission, cursors and resource baselines together before preparing a new run; late data must not contaminate its successor.

### k6 reference: apply the reasoning to your engine

Read `k6/README.md`, `k6/cmd/agent/main.go`, `k6/adapter/adapter.go`, and `k6/metrics/exporter.go` with its tests.

k6 writes CSV **independent metric samples**, so it uses an adapter-owned parser and `MetricsHandler`:

- `http_req_duration` supplies duration, status and URL together: one latency observation and one outcome. `http_reqs` is not counted again.
- Labels preserve the emitted CSV `url`: repeated URLs accumulate; query variants remain distinct within the budget. Custom source grouping may already have rewritten the URL; do not reconstruct an original value.
- `data_received` is an independent byte delta, aggregate under `k6` (`others` at zero budget); `vus` updates concurrency only. Neither creates a request.
- CSV uses `encoding/csv` and named columns. Required tags are retained when the plan specifies custom tags. Do not transfer k6-specific column names, units or error codes to another engine without evidence.

## 4. Implement the SDK lifecycle

1. Resolve deployment configuration once with `agent.LoadAgentServerOptions`. Initialize any exporter with its resolved `LabelsUsageLimit`, add callbacks without discarding resolved fields, and start `agent.NewAgentServer(options).Run()`. Do not call `MakeAgentServer` afterward and re-resolve the environment. Let the SDK own listener, bearer authentication and coordinator connections.
2. Define `TestFileName`, output needs and supported assignment modes. Consume per-agent settings without dividing them again. Consume replicated/materialized CSV bytes as delivered; reject unsupported native partitioning explicitly. Define exact units, bounds and schedule precedence.
3. In `BeforeStart`, honor context cancellation, invalidate previous prepared state, reset producer state if applicable, validate settings/assignments and atomically prepare run-local configuration in `RunContext.Workspace`. Preserve the original uploaded artifact. Mark only successful preparation as ready for command construction.
4. In `BuildCommand`, require the prepared run ID and return an `agent.Command` using an executable and argument vector, appropriate directory and explicit environment overrides. Do not interpolate uploaded values into a shell command.
5. Implement `Observe` using genuine engine readiness/progress evidence and propagate errors. The SDK owns downloads, workspace isolation, launch, process groups, wait/reap, cancellation, stop escalation and run serialization. Do not add a second process supervisor or turn nonzero engine exits into success.
6. Produce a non-root Linux image with writable SDK workspace paths, verified engine downloads, required CA certificates, source/license obligations and SDK notices. Use adapter-directory build targets with repository-root Docker context.
7. You MUST create an adapter-local `Makefile` to manage the build process, following `k6/Makefile` conventions. Provide targets for building the adapter, building its runtime image, unit/race tests (`test-unit`), container-only conformance (`test-conformance`), and both test suites (`test`). Scope Go commands to the adapter and shared conformance tests; keep the repository root as Docker's build context. Document invocation as `make -C <adapter> <target>` from the repository root, or `make <target>` inside the adapter directory.

## 5. Verify and deliver

Turn the agreed mapping into behavior-focused regression coverage and container conformance, using real engine output.

Import the optional public `github.com/reqfleet/engine-sdk/conformance` toolkit for the adapter's container integration tests; do not copy a generic harness into this repository. Follow `k6/conformance` as the first consumer: supply the real adapter executable, engine-native plans, settings and source-derived metric expectations to `conformance.Run`. Reuse its coordinator/artifact peers, process lifecycle and authenticated scrape checks. Keep engine-specific parsing, scheduling and failure scenarios in the adapter's test package.

Both SDK metric approaches must satisfy the shared normalized metrics assertions. Derive expected outcomes, latency observations and received bytes from real engine output or controlled fixtures. Exercise the configured label budget, including overflow and zero, and verify final snapshots across repeated scrapes. Keep parser unit tests alongside the adapter.

Build both the SDK's shared regression binary (`go test -c -tags=integration github.com/reqfleet/engine-sdk/conformance`) and the adapter-specific integration binary into the Dockerfile's `conformance` target. Run both sequentially using `docker run --rm --network none`, with no published ports or host mounts. Include `go test github.com/reqfleet/engine-sdk/conformance` in the unit target, wire unit and container tests into CI, and run the adapter's container conformance before publication. See the repository's development instructions for the fixture contract.

Cover these observable behaviors:

- Success and protocol error; representative DNS/refusal/timeouts/TLS failures where supported; unknown/missing error metadata; zero-duration failures; an all-failing run. Compare exact outcome totals to emitted attempts.
- Repeated/distinct labels, query variants where applicable, overflow, zero budget, stable admission, reset, and independent status groups. Verify separate gauges/bytes never create requests or fake URL attribution.
- Unit conversions, compatible histogram buckets/sums/counts, unchanged repeated scrapes, final retained totals, no duplicate ingestion and truthful unavailable values.
- Natural completion, Stop, cancellation during preparation, duplicate starts, stale stops, failed startup/settings, failed preparation reset, reused-process recovery, non-root execution and assignment modes. Exercise the actual `/metrics` surface and authentication with real lifecycle peers.

Run `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` from the repository root, replacing `<adapter>` with the adapter directory. For k6 metric changes specifically, run `make -C k6 test-unit` and `make -C k6 test-conformance`. Never run the integration binary directly on the host: it owns fixed ports and `/test-data`. Add a live smoke for behavior conformance does not exercise. Container conformance alone does not prove Shibuya onboarding or deployment compatibility.

Keep the adapter README's mappings, limitations and compatibility metadata aligned with implementation. Release-please owns changelogs and versions; do not edit them manually. Add a component and gated conformance/publication jobs for the new adapter, without releasing unrelated images. For shared SDK updates, include compatibility metadata under each affected adapter directory and verify each affected adapter.

Deliver exact executed checks and results, remaining blockers, and the manually tested tuple `(Shibuya release, immutable adapter image digest, SDK version, engine version, shibuya-v1)`. Never invent untested releases or digests. Publication requires observed release/workflow success, package access and immutable digest. Hand off control-plane integration and rollout to the Reqfleet team with the adapter's reporting contract and verification evidence.
