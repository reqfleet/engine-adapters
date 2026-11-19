---
name: develop-new-engine
description: Develop a new Shibuya load-testing engine adapter using the latest stable reqfleet engine-sdk release. Use when adding an engine kind, integrating an engine runtime, choosing between complete events, independent SDK recorder samples and native normalized exposition, or designing engine metrics reporting. Starts with load-generator selection and a license assessment for the intended commercial use. Requires a developer discussion of metric semantics and collection ownership before implementation; uses k6 as a reference.
---

# Develop a new engine

First identify the load generator and assess its license for the intended commercial use. Then establish the engine's reporting semantics with the developer and implement the selected SDK integration. Coordinate Shibuya control-plane changes and rollout with the Reqfleet team.

## 1. Check the load generator's license

- Ask the user which load generator and version they plan to integrate and how they intend to use it commercially: internal testing, a hosted service, or distribution of an image/binary. Use details already supplied and ask only for missing choices.
- Read the selected engine version's official license, notices and relevant commercial terms. Check the planned packaging and interaction with the adapter, including bundled plugins and dependencies.
- Assess whether the intended commercial use is permitted and identify applicable attribution, license-copy, corresponding-source, modification-disclosure and network-use obligations. Follow the [license-review checklist](../review-engine-adapter/SKILL.md#2-is-the-license-suitable-for-reqfleets-intended-use).
- Present an evidence-backed conclusion with source links, required obligations and unresolved questions. Escalate unresolved legal questions to maintainers or counsel and resolve licensing blockers before implementation.

## 2. Establish the release and scope

- Read the repository's applicable instructions. Paths below are relative to the engine-adapters repository root.
- Always seek and verify the latest stable engine-sdk release through [GitHub releases](https://github.com/reqfleet/engine-sdk/releases/latest) or the [release API](https://api.github.com/repos/reqfleet/engine-sdk/releases/latest) before selecting the dependency. Use that verified release for a new integration. If required APIs are unavailable in that release, report the gap and agree on a resolution before proceeding. Report release-discovery failures explicitly.
- Read the selected version's `go.mod`, `agent/doc.go`, `agent/metrics.md`, `agent/metrics_recorder.go`, and `conformance/doc.go` before coding. Pin the exact version. Use its required Go toolchain; align module dependencies, Docker defaults, notices, and adapter compatibility documentation. Keep the shared module.
- Inspect the target engine's official output/schema documentation and capture representative real output for the selected engine version. Inspect existing adapters before proposing architecture.
- Establish the exact kind, engine version, primary artifact and unique suffix, supported protocols, scheduling units, per-agent workload assignment, optional CSV delivery, and target Shibuya checkout/release. Infer repo-provided facts; ask only about unresolved product choices.

## 3. Discuss metrics before implementing

Select the reporting mode from the meaning and completeness of the engine's measurements. File/HTTP is transport; distinguish **complete request events, independently available measurements, and cumulative normalized exposition**.

Present findings, a recommended mode and its tradeoffs, then ask the developer to resolve material reporting choices. Use short rounds. Implement the reporting path after its semantics and mode are agreed. If the user has already specified them, restate the decision and proceed.

### First round: evidence and desired reports

Show one real successful request, one failed attempt, and any separate concurrency/byte samples or native snapshot. Explain what each source record actually represents. Ask only about remaining uncertainties:

- What is one counted operation: HTTP attempt, transaction, task, stream, or something else? How do retries, redirects and subrequests affect it?
- Which label should users see: URL, operation, sampler name, or another engine-defined value? Discuss cardinality and secrets. Agree on the label policy and preserve the selected source values, including query strings.
- Which reports are required: outcome counts, latency, received bytes, live concurrency, optional LLM response/TTFT/inter-token timings and received-token counts? Which are independently available, request-attributed or only per agent?
- What does latency measure: full wall time, response time, or an engine-specific interval? Are pre-response failures recorded, and can their duration legitimately be zero or absent?

Distinguish unavailable data from measured zero. Address missing required reporting data by changing the engine output configuration or adding measured instrumentation where needed; explicitly agree on and document any unsupported reporting.

### Second round: choose reporting ownership

**Prefer SDK-owned metrics through either `MetricParser` or `CustomIngestMetrics`, selected by the contents of each source record.** These paths delegate collectors, admission, identity binding, scheduling, final draining, resource sampling and HTTP serving to the SDK. Choose producer-owned exposition when an existing exporter already supplies the complete normalized schema and correctly owns its lifecycle.

| Approach | Appropriate source | SDK configuration and ownership | Tradeoff |
| --- | --- | --- | --- |
| SDK complete-event ingestion | Each result-file line supplies all measurements of an `agent.ShibuyaMetric` event, as with JMeter or Locust output configured to emit complete per-request records | Set `ResultFile` and `MetricParser`; leave `CustomIngestMetrics` and native options unset. SDK tails events and owns identity, collectors, request-label validation/admission and resources. | Minimal ingestion code; each record must supply all coupled event measurements truthfully. |
| SDK independent-sample recorder | Each line supplies an individual metric or partial measurement, as with metric-only Locust output or k6 CSV samples | Set `CustomIngestMetrics func(context.Context, agent.RunContext) error` and record through `run.Metrics`; leave `MetricParser`, `MetricsHandler` and native options unset. `ResultFile` may identify output parsed by the adapter. SDK owns scheduling, registry, identity, admission, resources and final collection. | Adapter owns source parsing, conversion, cursor/deduplication and measured attribution. |
| Producer-owned normalized exposition | An existing producer supplies complete compatible cumulative `shibuya-v1` snapshots and owns their lifecycle | Set `NativeMetricsSchema: agent.MetricsSchemaShibuyaV1` and either `MetricsHandler` or `MetricsURL`; leave `MetricParser` and `CustomIngestMetrics` nil. Producer owns identities, normalization, budget, resources and run state. | More responsibility; the producer supplies the entire native exposition. |

When producer-owned exposition is warranted, choose its delivery variant:

- Use **`MetricsHandler`** for an existing in-process normalized exporter. The SDK mounts the handler directly on its authenticated `/metrics` route. `ResultFile` may still name engine output that the exporter parses itself. The producer owns file parsing, complete exposition, reset, close and final-snapshot retention. Use the SDK's built-in metrics endpoint for recorder values.
- Use **`MetricsURL`** for an existing HTTP(S) producer supplying the complete normalized schema. Verify and normalize the engine's Prometheus names, identities, units, buckets and semantics first. Default proxying streams the producer's exposition unchanged; the producer owns schema compliance and label-budget enforcement and must remain reachable for final scrapes.
- `MetricParser`, `CustomIngestMetrics`, `MetricsHandler` and `MetricsURL` are mutually exclusive. Keep folding options exclusive to URL mode. Use `FoldNativeMetrics` only as a last-resort budget fallback; review the selected version's buffering, validation and snapshot limits. Require valid source attribution, compatible histogram bounds and correct producer run state before folding.
- With custom ingestion or native delivery, `ResultFile` can name engine output parsed by the adapter. Pair SDK-tailed output with `MetricParser`. Registry-only serving requires postlaunch callbacks to record measurements; verify the selected integration with real output.

**Critical event-path check:** `MetricParser` and `MetricsRecorder.RecordEvent` record latency, outcome, concurrency and bytes together. Use complete-event ingestion only for records containing all those measurements. Record independent VU, byte, token and timing samples through the corresponding recorder methods. Use `ObserveLatency`, `RecordStatus`, `ObserveRequest`, `AddReceivedBytes`, `AddAggregateReceivedBytes` and `SetConcurrency` for their independent measurements; use `RecordSample(agent.RequestSample{...})` with only the corresponding `Has*` flags for other combinations. Treat measured zero as a present value. Use the supplied `run.Metrics` recorder's current public APIs for recording. Parser errors drop events; preserve failure accounting even when error metadata is limited.

### Record the agreed mapping

Before code, provide a compact mapping table in the conversation, then preserve the agreed contract in the adapter README. Each row must specify:

- Source field/event and evidence; event, delta, cumulative counter, histogram or gauge.
- Target metric family, units and exact conversion.
- Request/agent attribution and label policy; identity source.
- Success, known failure, unknown and missing metadata handling.
- Accumulation, final drain, reset and unavailable-data behavior.

Conclude with the selected approach and any delivery variant, why alternatives are unsuitable, unsupported reports, ownership of each stateful component, and concrete acceptance scenarios. Resolve material disagreements with the developer before implementing.

## 4. Apply the reporting contract

Read the selected version's full `agent/metrics.md`. With `ResultFile` + `MetricParser`, return parsed `agent.ShibuyaMetric` events and let the SDK record them. With `CustomIngestMetrics`, add normalized samples through the supplied `run.Metrics` API. **The SDK owns the registry, collectors, admission, resource sampling and authenticated metrics endpoint.**

Use public `metricschema` constants for shared source names. Native exporters that construct their own exposition must also use its label lists and histogram profiles. The table below summarizes the metric families.

| Family | Meaning and attribution |
| --- | --- |
| `shibuya_latency_label_milliseconds` | Classic request histogram in milliseconds; identity + `label` |
| `shibuya_status_counter` | Exactly one outcome per reported attempt; identity + `label` + `status` |
| `shibuya_egress_bytes_counter` | Received/response bytes despite the historical name; identity + available `label` attribution |
| `shibuya_threads_gauge` | Actual current per-agent concurrency; identity only |
| `shibuya_cpu_gauge` | Container CPU cores consumed; identity only |
| `shibuya_mem_gauge` | Container-accounted memory bytes; identity only |

- Identity is `{collection_id, plan_id, run_id, engine_no, zone}`. SDK event/recorder modes bind assigned identity immutably and ignore sample identity, even if an assigned field is empty. Native producers must supply resolved identity themselves.
- Use the selected SDK's ordinary and wide LLM histogram profiles from `metricschema`. Convert source units exactly; preserve cumulative buckets, sums and counts for native snapshots. Compute aggregate percentiles from underlying distributions and retain source bucket resolution.
- Use `ObserveLLMResponse`, `ObserveTTFT` and `ObserveInterTokenLatency` for independently measured millisecond timings, and `RecordReceivedToken` once per genuinely received generated token, including the first. Record token receipts independently of HTTP outcomes; inter-token timing begins with the second token. Use the `metricschema` source constants (`LLMResponseDurationMetric`, `LLMTimeToFirstTokenMetric`, `LLMInterTokenDurationMetric`, `LLMTokenEventsMetric`) when configuring those SDK-defined sources. These methods record only their respective timing or token measurements; ordinary `ObserveLatency`/`ObserveRequest` remain ordinary histograms even for a request named `llm_resp`. See the canonical LLM contract for reserved labels and folding.
- Keep counters and histograms cumulative throughout a run, including inactive series. Repeated scrapes retain totals until new samples arrive. SDK registry mode retains final values; native producers must drain remaining output and retain their own final exposition for the SDK to serve directly.
- Validate numeric observations before recording: require finite, nonnegative latency, bytes and concurrency. Apply engine-specific numeric validation to general event/recorder inputs; the named LLM timing methods validate their timing arguments. Parser errors drop events. In recorder mode distinguish invalid-label sample rejection from fatal ingestion errors: discard invalid-label samples atomically and return unrecoverable collection errors. Record each available measurement and preserve absence for measurements missing from the source.
- Use the selected SDK's `metricschema.MaxRequestLabelBytes` and `ShibuyaMetric.LabelIsValid` for request-label validation. Pass original source labels unchanged to SDK event/recorder modes, which validate them before admission and reject the whole invalid sample. The resolved `LabelsUsageLimit` governs stable first-N admission plus `others`, with zero allowing only overflow. SDK owns the shared request-family admission state. Native exporters own validation, bounded admission and synchronization; reuse SDK helpers where applicable.
- Record per-request bytes only from explicitly request-attributed measurements. Preserve aggregate byte totals under aggregate labels, independently of request-label admission and request counts. Treat independent source rows as separate samples. Record concurrency only from measured concurrency observations.
- Translate engine error types/codes into deterministic readable descriptions, retaining native codes when available. Preserve actual DNS, refusal, connection-timeout, response-timeout and TLS classifications. Unknown codes remain identifiable; missing cause is an unspecified failure. Use bounded descriptions and native codes/types for error labels. Genuine protocol statuses remain distinct from transport failures. Count failed attempts once, including zero-duration and pre-response failures, and record only source-measured latency and bytes.
- SDK registry mode owns all CPU/memory collection after launch, periodically and finally; read failures warn and skip updates while scrapes and the workload continue. Native producers must supply the complete resource exposition using the selected SDK's resource APIs and contract. Use measured container-cgroup resources and document supported cgroup mounts and unavailable-resource behavior.
- Keep engine parsing/cursors, prepared state and cached readiness/progress run-scoped. Synchronize shared state accessed by ingestion, `Observe` and `Progress`; treat supplied settings and artifact maps as read-only. Reset only adapter-owned state before preparation. SDK recorders remain bound to their original run and SDK-owned registry throughout their lifetime. Cover partial output, malformed records, truncation, duplicates and failed preparation, keeping late data isolated to its original run.

### k6 reference: apply the reasoning to your engine

Read `k6/README.md`, `k6/cmd/agent/main.go`, `k6/adapter/adapter.go`, and `k6/metrics/exporter.go` with its tests.

k6 writes CSV **independent metric samples**, so it sets `CustomIngestMetrics = exporter.Collect` and uses the SDK recorder. Read its behavior against its pinned SDK:

- `http_req_duration` supplies duration, status and URL together through `ObserveRequest` and is the sole request-count source; skip the redundant `http_reqs` sample during normalization.
- Labels preserve the emitted CSV `url`: repeated URLs accumulate; query variants remain distinct within the budget. Treat custom source grouping as authoritative when it rewrites the emitted URL.
- By default, `data_received` is an aggregate byte delta under `k6` via `AddAggregateReceivedBytes`, independent of the request budget even at zero. The opt-in `request_received_bytes` source supplies URL-attributed bytes through `AddReceivedBytes`; select exactly one byte source per run. `vus` calls `SetConcurrency`. Byte and VU samples update only their respective measurements.
- Optional selected LLM sources use the named timing/token recorder methods. CSV uses `encoding/csv` and named columns; required tags survive custom tag selection. The exporter owns source parsing and readiness/error state only, while the SDK owns metrics and collection lifecycle. Establish each engine's columns, units and error codes from its own output evidence.

## 5. Implement the SDK lifecycle

1. Resolve deployment configuration once with `agent.LoadAgentServerOptions`. Preserve resolved fields when adding callbacks and start `agent.NewAgentServer(options).Run()`. Native exporters use the resolved `LabelsUsageLimit`; recorder integrations use the supplied recorder and its admission state. Reuse the resolved options throughout startup. Let the SDK own listener, authentication and coordinator connections.
2. Define `TestFileName`, output needs and supported assignment modes. Consume per-agent settings exactly as assigned. Consume replicated/materialized CSV bytes as delivered; reject unsupported native partitioning explicitly. Define exact units, bounds and schedule precedence. If dependencies are supported, use `PrepareDependencies` to reconcile staged declarations, including removing prior dependencies when absent.
3. In `BeforeStart`, honor cancellation, invalidate previous prepared state, reset adapter-owned parsing/native exporter state as appropriate, validate settings/assignments and atomically prepare run-local configuration in `RunContext.Workspace`. Preserve the original uploaded artifact. Mark only successful preparation ready for command construction. `run.Metrics` is nil in all prelaunch hooks, including `PrepareDependencies`, `BeforeStart` and `BuildCommand`.
4. In `BuildCommand`, require the prepared run ID and return an `agent.Command` using an executable and argument vector, appropriate directory and explicit environment overrides. Pass uploaded values as literal arguments.
5. After successful launch, SDK-managed modes receive a fresh immutable `run.Metrics` before postlaunch callbacks; native modes always receive nil. `CustomIngestMetrics` runs initially and every 250ms independently of scrapes, `Observe` and `Progress`. It must honor cancellation, ingest only new samples and synchronize shared readiness/error state. SDK joins live callbacks/processes before one final ingestion call with a fresh two-second `FinalMetricsTimeout` context; drain remaining output within it. Live and final ingestion execute serially. Accepted collection errors fail both the run and its registry scrapes; final errors/deadline expiry also fail the run. HTTP only gathers stored values. Keep ingestion scheduling and final-drain ownership exclusively with the SDK.
   - Successful launch publishes the registry. Completion retains it until a new run ID is accepted through prepare/start, which clears prior SDK output immediately; scrapes remain empty through preparation or launch failure. Duplicate, stale or rejected triggers preserve the registry. Native exporters retain ownership of their reset policy.
   - Optional `Observe` reports genuine readiness/progress evidence within its bounded context; with `Observe` unset, launch acknowledges readiness. Optional `Progress` runs separately as a read-only view of lifecycle outcomes; support its bounded final snapshot with cached summaries. A successful short run may finish before readiness polling. Process failure determines a failed outcome, including when `WorkComplete` or readiness has been reported.
   - The SDK is the sole supervisor and owns downloads, workspace isolation, launch, process groups, wait/reap, cancellation, stop escalation and run serialization. Optional `Stop` must honor its bounded context and delegate primary-process waiting/reaping to the SDK. Preserve nonzero process exits as failures.
6. Produce a non-root Linux image with writable SDK workspace paths, verified engine downloads, required CA certificates, source/license obligations and SDK notices. Use adapter-directory build targets with repository-root Docker context.
7. You MUST create an adapter-local `Makefile` to manage the build process, following `k6/Makefile` conventions. Provide targets for building the adapter, building its runtime image, unit/race tests (`test-unit`), container-only conformance (`test-conformance`), and both test suites (`test`). Scope Go commands to the adapter and shared conformance tests; keep the repository root as Docker's build context. Document invocation as `make -C <adapter> <target>` from the repository root, or `make <target>` inside the adapter directory.

## 6. Verify and deliver

Turn the agreed mapping into behavior-focused regression coverage and container conformance, using real engine output.

Import the optional public `github.com/reqfleet/engine-sdk/conformance` toolkit as the shared harness for the adapter's container integration tests. Follow `k6/conformance` as the first consumer: supply the real adapter executable, engine-native plans, settings and source-derived metric expectations to `conformance.Run`. Reuse its coordinator/artifact peers, process lifecycle and authenticated scrape checks. Keep engine-specific parsing, scheduling and failure scenarios in the adapter's test package.

All reporting modes must satisfy the shared normalized metrics assertions. Derive expected outcomes, latency observations and received bytes exclusively from native output or controlled workload evidence, independently of the production parser/exporter and normalized scrape. Exercise the configured label budget, including overflow and zero, and compare repeated/final request snapshots separately from independently changing resources. Keep parser unit tests alongside the adapter.

Build both the SDK's shared regression binary (`go test -c -tags=integration github.com/reqfleet/engine-sdk/conformance`) and the adapter-specific integration binary into the Dockerfile's `conformance` target. Run both sequentially using `docker run --rm --network none`, with container-private ports and filesystem. Include `go test github.com/reqfleet/engine-sdk/conformance` in the unit target, wire unit and container tests into CI, and run the adapter's container conformance before publication. See the repository's development instructions for the fixture contract.

Cover these observable behaviors:

- Success and protocol error; representative DNS/refusal/timeouts/TLS failures where supported; unknown/missing error metadata; zero-duration failures; an all-failing run. Compare exact outcome totals to emitted attempts.
- Repeated/distinct labels, query variants where applicable, overflow, zero budget, stable admission, reset, and independent status groups. Verify that separate gauge/byte samples update only their respective measurements and preserve source-provided attribution.
- Unit conversions, compatible histogram buckets/sums/counts, unchanged repeated scrapes, final retained totals, exactly-once ingestion and truthful unavailable values. Test independent latency/status/bytes/concurrency and LLM timing/token effects separately, including invalid-label whole-sample rejection, the selected SDK's label-length boundary, aggregate bytes at zero budget, and request counts derived exclusively from reported attempts.
- Natural completion, Stop, cancellation during preparation, duplicate starts, stale stops, failed startup/settings, failed preparation reset, reused-process recovery, non-root execution and assignment modes. For recorder integration, cover final drain, fatal collection errors and scrape failure, fresh state after failure, empty output after accepted prepare/start until launch, and concurrent readiness/progress reads. Exercise the actual authenticated `/metrics` surface with real lifecycle peers; verify metrics availability and workload readiness independently.

Run `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` from the repository root, replacing `<adapter>` with the adapter directory. For k6 metric changes specifically, run `make -C k6 test-unit` and `make -C k6 test-conformance`. Run integration binaries exclusively in their disposable containers, which isolate their fixed ports and `/test-data`. Add a live smoke for behavior outside conformance coverage. Verify Shibuya onboarding and deployment compatibility separately.

Keep the adapter README's mappings, limitations and compatibility metadata aligned with implementation. For shared SDK updates, include compatibility metadata under each affected adapter directory and verify each affected adapter.

### Wire GitHub Actions publication

Follow the existing k6 release and publication wiring for each new adapter:

1. Register the adapter directory and component in [release-please-config.json](../../../release-please-config.json) and bootstrap its version in [.release-please-manifest.json](../../../.release-please-manifest.json). Let release-please generate subsequent versions, changelogs, component tags and GitHub releases. Keep each adapter independently versioned.
2. Add the adapter's `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` checks to [.github/workflows/pr.yml](../../../.github/workflows/pr.yml).
3. Extend [.github/workflows/publish.yml](../../../.github/workflows/publish.yml) with adapter-specific release-please job outputs sourced from `<adapter>--release_created`, `<adapter>--version` and `<adapter>--sha`. Keep publication driven by those outputs in the existing push-to-main workflow.
4. Add adapter-specific conformance and publication jobs gated on that component's `release_created == 'true'`. Check out the exact released SHA in both jobs. Run `make -C <adapter> test` in the conformance job and make publication depend on its success. Scope each job to its adapter's release and image.
5. Create `.github/actions/publish-<adapter>/action.yml` following the [publish-k6 composite action](../../../.github/actions/publish-k6/action.yml). Accept `version` and `registry-token` inputs and expose the build's immutable `digest` output. Configure QEMU, Buildx and GHCR login; build `<adapter>/Dockerfile` with repository-root context, the `runtime` target, `linux/amd64,linux/arm64`, provenance and SBOM enabled, and push `ghcr.io/reqfleet/engine-adapters/<adapter>:<version>`.
6. Invoke the local action from the publication job with `v` prefixed to the release-please version and `GITHUB_TOKEN` as the registry token. Give release-please `contents: write`, `issues: write` and `pull-requests: write`; give publication `contents: read`, `packages: write`, `attestations: write` and `id-token: write`. Confirm repository Actions permissions allow release PR creation and that GHCR access and package visibility meet the intended distribution.
7. Write `ghcr.io/reqfleet/engine-adapters/<adapter>@<digest>` to `GITHUB_STEP_SUMMARY`. Verify the release workflow's conformance and publication results, package accessibility and immutable digest before handing the image to operators. Preserve published tags and image versions as immutable release artifacts.

Deliver exact executed checks and results, remaining blockers, and the manually tested tuple `(Shibuya release, immutable adapter image digest, SDK version, engine version, shibuya-v1)`. Mark unverified releases or digests as unknown. Publication requires observed release/workflow success, package access and immutable digest. Hand off control-plane integration and rollout to the Reqfleet team with the adapter's reporting contract and verification evidence.
