---
name: develop-new-engine
description: Develop a new Shibuya load-testing engine adapter using the latest stable reqfleet engine-sdk release. Use when adding an engine kind, integrating an engine runtime, choosing between complete events, independent SDK recorder samples and native normalized exposition, or designing engine metrics reporting. Requires a developer discussion of metric semantics and collection ownership before implementation; uses k6 as a reference.
---

# Develop a new engine

First establish the engine's reporting semantics with the developer, then implement the selected SDK integration. Coordinate Shibuya control-plane changes and rollout with the Reqfleet team.

## 1. Establish the release and scope

- Read the repository's applicable instructions. Paths below are relative to the engine-adapters repository root.
- Always seek and verify the latest stable engine-sdk release through [GitHub releases](https://github.com/reqfleet/engine-sdk/releases/latest) or the [release API](https://api.github.com/repos/reqfleet/engine-sdk/releases/latest) before selecting the dependency. Use that release for a new integration; do not assume an existing repository pin or development `main` is the latest stable release. If required APIs are unavailable in that release, report the gap and agree on a resolution before proceeding. Disclose unavailable release discovery rather than calling an unverified version “latest.”
- Read the selected version's `go.mod`, `agent/doc.go`, `agent/metrics.md`, `agent/metrics_recorder.go`, and `conformance/doc.go` before coding. Pin the exact version, never `main` or a floating version. Use its required Go toolchain; align module dependencies, Docker defaults, notices, and adapter compatibility documentation. Keep the shared module rather than creating a nested module.
- Inspect the target engine's official output/schema documentation and capture representative real output for the selected engine version. Inspect existing adapters before proposing architecture.
- Establish the exact kind, engine version, primary artifact and unique suffix, supported protocols, scheduling units, per-agent workload assignment, optional CSV delivery, and target Shibuya checkout/release. Infer repo-provided facts; ask only about unresolved product choices.

## 2. Discuss metrics before implementing

Do not silently select a mode because an engine writes CSV or exposes Prometheus. File/HTTP is transport; distinguish **complete request events, independently available measurements, and cumulative normalized exposition**.

Present findings, a recommended mode and its tradeoffs, then ask the developer to resolve material reporting choices. Use short rounds rather than an exhaustive questionnaire. Do not implement the reporting path until its semantics and mode are agreed. If the user has already specified them, restate the decision and proceed without redundant confirmation.

### First round: evidence and desired reports

Show one real successful request, one failed attempt, and any separate concurrency/byte samples or native snapshot. Explain what each source record actually represents. Ask only what the evidence cannot answer:

- What is one counted operation: HTTP attempt, transaction, task, stream, or something else? How do retries, redirects and subrequests affect it?
- Which label should users see: URL, operation, sampler name, or another engine-defined value? Discuss cardinality and secrets. Do not silently discard query strings or replace labels.
- Which reports are required: outcome counts, latency, received bytes, live concurrency, optional LLM response/TTFT/inter-token timings and received-token counts? Which are independently available, request-attributed or only per agent?
- What does latency measure: full wall time, response time, or an engine-specific interval? Are pre-response failures recorded, and can their duration legitimately be zero or absent?

Distinguish unavailable data from measured zero. Address missing required reporting data by changing the engine output configuration or adding measured instrumentation where needed; explicitly agree on and document any unsupported reporting.

### Second round: choose reporting ownership

**Prefer SDK-owned metrics.** Use `CustomIngestMetrics` as the default for independent engine samples; use `MetricParser` when each result record genuinely supplies a complete request event. These paths avoid adapters rebuilding collectors, admission, identity binding, scheduling, final draining, resource sampling and HTTP serving. Choose producer-owned exposition when an existing exporter already supplies the complete normalized schema and correctly owns its lifecycle, not merely because the engine exposes HTTP or writes a file.

| Approach | Appropriate source | SDK configuration and ownership | Tradeoff |
| --- | --- | --- | --- |
| SDK complete-event ingestion | Each result record supplies all measurements of an `agent.ShibuyaMetric` event | Set `ResultFile` and `MetricParser`; leave `CustomIngestMetrics` and native options unset. SDK tails events and owns identity, collectors, request-label validation/admission and resources. | Minimal ingestion code, but all coupled event measurements must be truthful. |
| SDK independent-sample recorder | Request measurements, byte deltas, concurrency or LLM observations arrive independently or with missing fields | Set `CustomIngestMetrics func(context.Context, agent.RunContext) error` and record through `run.Metrics`; leave `MetricParser`, `MetricsHandler` and native options unset. `ResultFile` may identify output parsed by the adapter. SDK owns scheduling, registry, identity, admission, resources and final collection. | Default for independent samples. Adapter owns source parsing, conversion, cursor/deduplication and measured attribution, not registry management. |
| Producer-owned normalized exposition | An existing producer supplies complete compatible cumulative `shibuya-v1` snapshots and owns their lifecycle | Set `NativeMetricsSchema: agent.MetricsSchemaShibuyaV1` and either `MetricsHandler` or `MetricsURL`; leave `MetricParser` and `CustomIngestMetrics` nil. Producer owns identities, normalization, budget, resources and run state. | More responsibility; SDK does not merge registry samples into native output. |

When producer-owned exposition is warranted, choose its delivery variant:

- Use **`MetricsHandler`** for an existing in-process normalized exporter. The SDK mounts the handler on its authenticated `/metrics` route; no adapter listener or loopback proxy. `ResultFile` may still name engine output that the exporter parses itself. The SDK neither tails that file nor merges its registry, resets, closes or caches the handler. Do not add a custom handler merely to expose SDK recorder values; the SDK already serves them.
- Use **`MetricsURL`** for an existing HTTP(S) producer supplying the complete normalized schema. Arbitrary engine Prometheus metrics are not automatically compatible. Normalize names, identities, units, buckets and semantics first. Default proxying streams without folding or schema validation; the producer must remain reachable for final scrapes.
- `MetricParser`, `CustomIngestMetrics`, `MetricsHandler` and `MetricsURL` are mutually exclusive. Handler mode does not support folding options. URL mode can opt into `FoldNativeMetrics` only as a last-resort budget fallback; review the selected version's buffering, validation and snapshot limits. Folding does not repair missing attribution, incompatible histogram bounds or producer resets.
- With custom ingestion or native delivery, `ResultFile` can name ordinary engine output without SDK tailing. Otherwise pair it with `MetricParser`. Registry-only serving is not complete reporting unless postlaunch callbacks actually record measurements; verify the selected integration with real output.

**Critical event-path check:** `MetricParser` and `MetricsRecorder.RecordEvent` record latency, outcome, concurrency and bytes together. Never fabricate complete events from VU-only, byte-only, token or timing samples. Use `ObserveLatency`, `RecordStatus`, `ObserveRequest`, `AddReceivedBytes`, `AddAggregateReceivedBytes` and `SetConcurrency` for their independent measurements; use `RecordSample(agent.RequestSample{...})` with only the corresponding `Has*` flags for other combinations. Measured zero is present, not missing. Do not use the removed process-global `ShibuyaMetric.ToPrometheus` or direct registry recording APIs. Parser errors drop events; rejecting metadata-poor failures is not a substitute for truthful failure accounting.

### Record the agreed mapping

Before code, provide a compact mapping table in the conversation, then preserve the agreed contract in the adapter README. Each row must specify:

- Source field/event and evidence; event, delta, cumulative counter, histogram or gauge.
- Target metric family, units and exact conversion.
- Request/agent attribution and label policy; identity source.
- Success, known failure, unknown and missing metadata handling.
- Accumulation, final drain, reset and unavailable-data behavior.

Conclude with the selected approach and any delivery variant, why alternatives are unsuitable, unsupported reports, ownership of each stateful component, and concrete acceptance scenarios. Resolve material disagreements with the developer before implementing.

## 3. Apply the reporting contract

Read the selected version's full `agent/metrics.md`. With `ResultFile` + `MetricParser`, return parsed `agent.ShibuyaMetric` events and let the SDK record them. With `CustomIngestMetrics`, add normalized samples through the supplied `run.Metrics` API. **Neither approach manages the registry:** do not construct, inject, reset or expose a registry, define collectors, or implement admission/resource sampling yourself. The SDK owns those responsibilities and the authenticated metrics endpoint.

Use public `metricschema` constants for shared source names. Native exporters that construct their own exposition must also use its label lists and histogram profiles rather than copying a second schema. The table below is an orientation, not a replacement for the SDK contract.

| Family | Meaning and attribution |
| --- | --- |
| `shibuya_latency_label_milliseconds` | Classic request histogram in milliseconds; identity + `label` |
| `shibuya_status_counter` | Exactly one outcome per reported attempt; identity + `label` + `status` |
| `shibuya_egress_bytes_counter` | Received/response bytes despite the historical name; identity + available `label` attribution |
| `shibuya_threads_gauge` | Actual current per-agent concurrency; identity only |
| `shibuya_cpu_gauge` | Container CPU cores consumed; identity only |
| `shibuya_mem_gauge` | Container-accounted memory bytes; identity only |

- Identity is `{collection_id, plan_id, run_id, engine_no, zone}`. SDK event/recorder modes bind assigned identity immutably and ignore sample identity, even if an assigned field is empty. Native producers must supply resolved identity themselves.
- Use the selected SDK's ordinary and wide LLM histogram profiles from `metricschema`. Convert source units exactly; preserve cumulative buckets, sums and counts for native snapshots. Never average percentiles or invent finer buckets from summaries.
- Use `ObserveLLMResponse`, `ObserveTTFT` and `ObserveInterTokenLatency` for independently measured millisecond timings, and `RecordReceivedToken` once per genuinely received generated token, including the first. Token receipt is not an HTTP outcome; the first token has no inter-token gap. Use the `metricschema` source constants (`LLMResponseDurationMetric`, `LLMTimeToFirstTokenMetric`, `LLMInterTokenDurationMetric`, `LLMTokenEventsMetric`) when configuring those SDK-defined sources. These methods create no unrelated measurements; ordinary `ObserveLatency`/`ObserveRequest` remain ordinary histograms even for a request named `llm_resp`. See the canonical LLM contract for reserved labels and folding.
- Keep counters and histograms cumulative throughout a run, including inactive series. An unchanged second scrape must not increase totals. SDK registry mode retains final values; native producers must retain their own final exposition and drain remaining output because the SDK does not cache native snapshots.
- Validate numeric observations before recording: reject negative, NaN or infinite latency, bytes and concurrency. General event/recorder methods do not replace engine-specific numeric validation; the named LLM timing methods validate their timing arguments. Parser errors drop events. In recorder mode distinguish invalid-label sample rejection from fatal ingestion errors: discard an invalid-label sample without partial updates, but return unrecoverable collection errors rather than serving partial totals. Preserve available status/other measurements when a source measurement is absent instead of fabricating zeros.
- Use the selected SDK's `metricschema.MaxRequestLabelBytes` and `ShibuyaMetric.LabelIsValid`, not a copied numeric limit. SDK event/recorder modes validate the original source label before admission and reject the whole invalid sample; never truncate or pre-admit recorder labels. The resolved `LabelsUsageLimit` governs stable first-N admission plus `others`, with zero allowing only overflow. SDK owns the shared request-family admission state. Native exporters own validation, bounded admission and synchronization; reuse SDK helpers where applicable.
- Keep unavailable per-request bytes unavailable; do not divide aggregate bytes across requests, copy totals to every label, or join independent rows by order/timestamp. Aggregate samples must not consume request-label slots or create request counts. Never infer zero concurrency from quiet request traffic.
- Translate engine error types/codes into deterministic readable descriptions, retaining native codes when available. Preserve actual DNS, refusal, connection-timeout, response-timeout and TLS classifications. Unknown codes remain identifiable; missing cause is an unspecified failure. Do not use raw error messages as labels. Genuine protocol statuses remain distinct from transport failures. Count failed attempts once, including zero-duration and pre-response failures, without inventing latency or bytes that the source did not measure.
- SDK registry mode owns CPU/memory collection after launch, periodically and finally; read failures warn and skip updates, not fail scrapes or the workload. Do not add adapter resource sampling. Native producers must supply the complete resource exposition using the selected SDK's resource APIs and contract, never host/process fallbacks or fabricated zeros; document supported cgroup mounts and unavailable-resource behavior.
- Keep engine parsing/cursors, prepared state and cached readiness/progress run-scoped. Synchronize shared state accessed by ingestion, `Observe` and `Progress`; do not mutate supplied settings or artifact maps. Reset only adapter-owned state before preparation. SDK recorders have no reset/rebind or registry injection API. Cover partial output, malformed records, truncation, duplicates and failed preparation without carrying late data into a successor.

### k6 reference: apply the reasoning to your engine

Read `k6/README.md`, `k6/cmd/agent/main.go`, `k6/adapter/adapter.go`, and `k6/metrics/exporter.go` with its tests.

k6 writes CSV **independent metric samples**, so it sets `CustomIngestMetrics = exporter.Collect` and uses the SDK recorder, not `MetricsHandler`. Read its behavior against its pinned SDK, not development `main`:

- `http_req_duration` supplies duration, status and URL together through `ObserveRequest`; `http_reqs` is not counted again.
- Labels preserve the emitted CSV `url`: repeated URLs accumulate; query variants remain distinct within the budget. Custom source grouping may already have rewritten the URL; do not reconstruct an original value.
- By default, `data_received` is an aggregate byte delta under `k6` via `AddAggregateReceivedBytes`, independent of the request budget even at zero. The opt-in `request_received_bytes` source instead supplies URL-attributed bytes through `AddReceivedBytes`; select one source to avoid double counting. `vus` calls `SetConcurrency`. Neither creates a request.
- Optional selected LLM sources use the named timing/token recorder methods, not synthetic HTTP events. CSV uses `encoding/csv` and named columns; required tags survive custom tag selection. The exporter owns source parsing and readiness/error state only, while the SDK owns metrics and collection lifecycle. Do not transfer k6-specific columns, units or error codes without evidence.

## 4. Implement the SDK lifecycle

1. Resolve deployment configuration once with `agent.LoadAgentServerOptions`. Add callbacks without discarding resolved fields and start `agent.NewAgentServer(options).Run()`. Native exporters use the resolved `LabelsUsageLimit`; recorder integrations use the supplied recorder rather than creating their own admission store. Do not call `MakeAgentServer` afterward and re-resolve the environment. Let the SDK own listener, authentication and coordinator connections.
2. Define `TestFileName`, output needs and supported assignment modes. Consume per-agent settings without dividing them again. Consume replicated/materialized CSV bytes as delivered; reject unsupported native partitioning explicitly. Define exact units, bounds and schedule precedence. If dependencies are supported, use `PrepareDependencies` to reconcile staged declarations, including removing prior dependencies when absent.
3. In `BeforeStart`, honor cancellation, invalidate previous prepared state, reset adapter-owned parsing/native exporter state as appropriate, validate settings/assignments and atomically prepare run-local configuration in `RunContext.Workspace`. Preserve the original uploaded artifact. Mark only successful preparation ready for command construction. `run.Metrics` is nil in all prelaunch hooks, including `PrepareDependencies`, `BeforeStart` and `BuildCommand`.
4. In `BuildCommand`, require the prepared run ID and return an `agent.Command` using an executable and argument vector, appropriate directory and explicit environment overrides. Do not interpolate uploaded values into a shell command.
5. After successful launch, SDK-managed modes receive a fresh immutable `run.Metrics` before postlaunch callbacks; native modes always receive nil. `CustomIngestMetrics` runs initially and every 250ms independently of scrapes, `Observe` and `Progress`. It must honor cancellation, ingest only new samples and synchronize shared readiness/error state. SDK joins live callbacks/processes before one final ingestion call with a fresh two-second `FinalMetricsTimeout` context; drain remaining output within it. Live and final ingestion do not overlap. Accepted collection errors fail both the run and its registry scrapes; final errors/deadline expiry also fail the run. HTTP only gathers stored values. Do not add polling goroutines, scrape-triggered parsing or a second final-drain owner.
   - Successful launch, not readiness, publishes the registry. Completion retains it until a new run ID is accepted through prepare/start, which clears prior SDK output immediately; scrapes remain empty through preparation or launch failure. Duplicate, stale or rejected triggers do not clear it. These rules do not reset native exporter output.
   - Optional `Observe` reports genuine readiness/progress evidence within its bounded context; without it launch acknowledges readiness. Optional `Progress` runs separately and cannot alter lifecycle outcomes; support its bounded final snapshot with cached summaries. A successful short run may finish before readiness polling. Neither `WorkComplete` nor readiness overrides a failed process.
   - The SDK owns downloads, workspace isolation, launch, process groups, wait/reap, cancellation, stop escalation and run serialization. Optional `Stop` must honor its bounded context and never wait/reap the primary process. Do not add another supervisor or turn nonzero exits into success.
6. Produce a non-root Linux image with writable SDK workspace paths, verified engine downloads, required CA certificates, source/license obligations and SDK notices. Use adapter-directory build targets with repository-root Docker context.
7. You MUST create an adapter-local `Makefile` to manage the build process, following `k6/Makefile` conventions. Provide targets for building the adapter, building its runtime image, unit/race tests (`test-unit`), container-only conformance (`test-conformance`), and both test suites (`test`). Scope Go commands to the adapter and shared conformance tests; keep the repository root as Docker's build context. Document invocation as `make -C <adapter> <target>` from the repository root, or `make <target>` inside the adapter directory.

## 5. Verify and deliver

Turn the agreed mapping into behavior-focused regression coverage and container conformance, using real engine output.

Import the optional public `github.com/reqfleet/engine-sdk/conformance` toolkit for the adapter's container integration tests; do not copy a generic harness into this repository. Follow `k6/conformance` as the first consumer: supply the real adapter executable, engine-native plans, settings and source-derived metric expectations to `conformance.Run`. Reuse its coordinator/artifact peers, process lifecycle and authenticated scrape checks. Keep engine-specific parsing, scheduling and failure scenarios in the adapter's test package.

All reporting modes must satisfy the shared normalized metrics assertions. Derive expected outcomes, latency observations and received bytes independently from native output or controlled workload evidence, never from the production parser/exporter under test or the normalized scrape. Exercise the configured label budget, including overflow and zero, and compare repeated/final snapshots without conflating independently changing resources. Keep parser unit tests alongside the adapter.

Build both the SDK's shared regression binary (`go test -c -tags=integration github.com/reqfleet/engine-sdk/conformance`) and the adapter-specific integration binary into the Dockerfile's `conformance` target. Run both sequentially using `docker run --rm --network none`, with no published ports or host mounts. Include `go test github.com/reqfleet/engine-sdk/conformance` in the unit target, wire unit and container tests into CI, and run the adapter's container conformance before publication. See the repository's development instructions for the fixture contract.

Cover these observable behaviors:

- Success and protocol error; representative DNS/refusal/timeouts/TLS failures where supported; unknown/missing error metadata; zero-duration failures; an all-failing run. Compare exact outcome totals to emitted attempts.
- Repeated/distinct labels, query variants where applicable, overflow, zero budget, stable admission, reset, and independent status groups. Verify separate gauges/bytes never create requests or fake URL attribution.
- Unit conversions, compatible histogram buckets/sums/counts, unchanged repeated scrapes, final retained totals, no duplicate ingestion and truthful unavailable values. Test independent latency/status/bytes/concurrency and LLM timing/token effects separately, including invalid-label whole-sample rejection, the selected SDK's label-length boundary, aggregate bytes at zero budget, and no synthetic request counts.
- Natural completion, Stop, cancellation during preparation, duplicate starts, stale stops, failed startup/settings, failed preparation reset, reused-process recovery, non-root execution and assignment modes. For recorder integration, cover final drain, fatal collection errors and scrape failure, fresh state after failure, empty output after accepted prepare/start until launch, and concurrent readiness/progress reads. Exercise the actual authenticated `/metrics` surface with real lifecycle peers; metrics availability is not readiness.

Run `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` from the repository root, replacing `<adapter>` with the adapter directory. For k6 metric changes specifically, run `make -C k6 test-unit` and `make -C k6 test-conformance`. Never run the integration binary directly on the host: it owns fixed ports and `/test-data`. Add a live smoke for behavior conformance does not exercise. Container conformance alone does not prove Shibuya onboarding or deployment compatibility.

Keep the adapter README's mappings, limitations and compatibility metadata aligned with implementation. Release-please owns changelogs and versions; do not edit them manually. Add a component and gated conformance/publication jobs for the new adapter, without releasing unrelated images. For shared SDK updates, include compatibility metadata under each affected adapter directory and verify each affected adapter.

Deliver exact executed checks and results, remaining blockers, and the manually tested tuple `(Shibuya release, immutable adapter image digest, SDK version, engine version, shibuya-v1)`. Never invent untested releases or digests. Publication requires observed release/workflow success, package access and immutable digest. Hand off control-plane integration and rollout to the Reqfleet team with the adapter's reporting contract and verification evidence.
