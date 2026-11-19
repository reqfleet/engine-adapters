---
name: develop-new-engine
description: Develop a Shibuya load-testing engine adapter with the latest stable reqfleet engine-sdk. Use for new engines, SDK integration, or metrics design. Select the load generator and assess its commercial-use license first. Agree metric semantics and ownership before implementation. Use k6 as a reference.
---

# Develop a new engine

Identify the load generator and assess its license for the intended commercial use.
Agree reporting semantics with the developer, then implement the selected SDK mode.
Coordinate Shibuya changes and rollout with the Reqfleet team.

## 1. Check the load generator's license

- Establish the engine, version, and intended commercial use: internal testing, hosted service, or image/binary distribution. Ask only for missing choices.
- Read official licenses, notices, and commercial terms for that version. Include bundled plugins, dependencies, packaging, and adapter interaction.
- Assess permitted use and attribution, license-copy, corresponding-source, modification-disclosure, and network-use obligations. Follow the [license-review checklist](../review-engine-adapter/SKILL.md#2-is-the-license-suitable-for-reqfleets-intended-use).
- Present source-linked conclusions, obligations, and unresolved questions. Resolve licensing blockers with maintainers or counsel before implementation.

## 2. Establish the release and scope

- Read the repository's applicable instructions. Paths below are relative to the engine-adapters repository root.
- Verify the latest stable engine-sdk through [GitHub releases](https://github.com/reqfleet/engine-sdk/releases/latest) or the [release API](https://api.github.com/repos/reqfleet/engine-sdk/releases/latest). Use that release for new integrations. Report discovery failures. If required APIs are missing, agree a resolution before proceeding.
- Read that version's `go.mod`, `agent/doc.go`, `agent/metrics.md`, `agent/metrics_recorder.go`, and `conformance/doc.go`. Pin the version and use its Go toolchain. Align dependencies, Docker defaults, notices, and compatibility docs. Keep the shared module.
- Read the engine's versioned output/schema docs and capture real output. Inspect existing adapters before proposing architecture.
- Establish the kind, engine version, primary artifact, unique suffix, protocols, scheduling units, per-agent assignment, optional CSVs, and target Shibuya release. Resolve repository facts directly; ask only about product choices.

## 3. Discuss metrics before implementing

Choose reporting from the measurements' meaning and completeness.
Distinguish **complete request events, independent measurements, and cumulative normalized exposition**.
File and HTTP are delivery transports.

Present evidence, a recommended mode, and tradeoffs.
Resolve material reporting choices with the developer in short rounds before implementation.
Use already-specified choices directly.

### First round: evidence and desired reports

Show real output: a successful request, a failed attempt, and separate
concurrency/byte samples or a native snapshot. Explain each record's meaning.
Resolve remaining questions:

- What counts as an operation: HTTP attempt, transaction, task, or stream? How do retries, redirects, and subrequests count?
- Which label should users see: URL, operation, sampler name, or another entity? Discuss cardinality and secrets. Preserve selected values, including query strings.
- Which reports are required: outcomes, latency, received bytes, concurrency, or LLM timings/token counts? Which measurements are independent, request-attributed, or agent-level?
- What does latency measure? How are pre-response failures represented? Can their duration be zero or absent?

Distinguish missing data from measured zero.
Obtain required measurements through engine configuration or instrumentation.
Agree and document unsupported reports.

### Second round: choose reporting ownership

**Prefer SDK-owned metrics through `MetricParser` or `CustomIngestMetrics`, according to each source record.**
The SDK owns collectors, admission, identity, scheduling, final draining, resources, and HTTP.
Choose producer-owned exposition when an existing exporter supplies the complete normalized schema and owns its lifecycle.

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

**Critical event-path check:** `MetricParser` and `MetricsRecorder.RecordEvent`
record latency, outcome, concurrency, and bytes together.
Use them only for complete source records.
Record independent VU, byte, token, and timing samples through their corresponding methods.
Use `ObserveLatency`, `RecordStatus`, `ObserveRequest`, `AddReceivedBytes`,
`AddAggregateReceivedBytes`, and `SetConcurrency` for independent measurements.
For other combinations, use `RecordSample(agent.RequestSample{...})` with the corresponding `Has*` flags.
Treat measured zero as present.
Use the supplied `run.Metrics` recorder's public APIs.
Parser errors drop events; preserve failure accounting even with limited error metadata.

### Record the agreed mapping

Before coding, provide a compact mapping table. Preserve the agreed contract in the adapter README.
Each row must specify:

- Source field/event and evidence; event, delta, cumulative counter, histogram or gauge.
- Target metric family, units and exact conversion.
- Request/agent attribution and label policy; identity source.
- Success, known failure, unknown and missing metadata handling.
- Accumulation, final drain, reset and unavailable-data behavior.

State the selected mode and delivery variant, reasons for rejecting alternatives,
unsupported reports, state ownership, and acceptance scenarios.
Resolve material disagreements before implementation.

## 4. Apply the reporting contract

Read the selected version's full `agent/metrics.md`.
With `ResultFile` + `MetricParser`, return parsed `agent.ShibuyaMetric` events.
With `CustomIngestMetrics`, record normalized samples through `run.Metrics`.
**The SDK owns the registry, collectors, admission, resource sampling, and authenticated endpoint.**

Use public `metricschema` constants for shared source names. Native exporters that construct their own exposition must also use its label lists and histogram profiles. The table below summarizes the metric families.

| Family | Meaning and attribution |
| --- | --- |
| `shibuya_latency_label_milliseconds` | Classic request histogram in milliseconds; identity + `label` |
| `shibuya_status_counter` | Exactly one outcome per reported attempt; identity + `label` + `status` |
| `shibuya_egress_bytes_counter` | Received/response bytes despite the historical name; identity + available `label` attribution |
| `shibuya_threads_gauge` | Actual current per-agent concurrency; identity only |
| `shibuya_cpu_gauge` | Container CPU cores consumed; identity only |
| `shibuya_mem_gauge` | Container-accounted memory bytes; identity only |

- Identity is `{collection_id, plan_id, run_id, engine_no, zone}`. SDK event/recorder modes bind assigned identity immutably, including empty fields. They ignore sample identity. Native producers supply resolved identity.
- Use ordinary and wide LLM histogram profiles from the selected SDK's `metricschema`. Convert units exactly. Preserve native cumulative buckets, sums, and counts. Derive aggregate percentiles from distributions with source bucket resolution.
- Use `ObserveLLMResponse`, `ObserveTTFT`, and `ObserveInterTokenLatency` for independent millisecond timings. Call `RecordReceivedToken` once per genuine received generated token, including the first. Token counts are independent of HTTP outcomes; inter-token timing starts with token two.
  Configure sources using `metricschema` constants: `LLMResponseDurationMetric`, `LLMTimeToFirstTokenMetric`, `LLMInterTokenDurationMetric`, and `LLMTokenEventsMetric`.
  Each method records only its named measurement. `ObserveLatency`/`ObserveRequest` always use ordinary histograms, including for `llm_resp`. See the canonical LLM contract for reserved labels and folding.
- Keep counters, histograms, and inactive series cumulative throughout a run. Repeated scrapes retain totals until new samples arrive. SDK registries retain final values. Native producers drain output and retain final exposition.
- Require finite, nonnegative latency, bytes, and concurrency. Apply engine-specific numeric validation to general event/recorder inputs. Named LLM timing methods validate their arguments. Parser errors drop events.
  In recorder mode, discard invalid-label samples atomically and return unrecoverable ingestion errors. Record available measurements and preserve missing values as absent.
- Use the selected SDK's `metricschema.MaxRequestLabelBytes` and `ShibuyaMetric.LabelIsValid`. Pass original labels to SDK event/recorder modes, which validate before admission and reject whole invalid samples.
  `LabelsUsageLimit` controls stable first-N admission plus `others`; zero allows only overflow. The SDK owns shared request-family admission. Native exporters own validation, bounded admission, and synchronization; reuse SDK helpers where applicable.
- Record request bytes only from request-attributed measurements. Preserve aggregate totals under aggregate labels, independently of request admission and counts. Treat independent rows as separate samples. Record concurrency only from measured observations.
- Map native error types/codes to deterministic readable descriptions and retain available native codes. Preserve DNS, refusal, connection-timeout, response-timeout, and TLS classifications.
  Unknown codes remain identifiable; missing causes remain unspecified. Bound error labels to descriptions and native codes/types. Keep protocol statuses distinct from transport failures.
  Count each failed attempt once, including zero-duration and pre-response failures. Record only measured latency and bytes.
- SDK registries own launch, periodic, and final CPU/memory collection. Read failures warn and skip updates while scrapes and workloads continue. Native producers supply resources through the selected SDK's APIs and contract.
  Use measured container-cgroup values. Document supported mounts and unavailable-resource behavior.
- Scope parsing/cursors, prepared state, and cached readiness/progress to each run. Synchronize access shared by ingestion, `Observe`, and `Progress`. Treat settings and artifact maps as read-only.
  Reset only adapter state before preparation. SDK recorders remain bound to their original run and registry.
  Cover partial output, malformed records, truncation, duplicates, and failed preparation. Isolate late data to its original run.

### k6 reference: apply the reasoning to your engine

Read `k6/README.md`, `k6/cmd/agent/main.go`, `k6/adapter/adapter.go`, and `k6/metrics/exporter.go` with its tests.

k6 writes CSV **independent metric samples**, so it sets `CustomIngestMetrics = exporter.Collect` and uses the SDK recorder. Read its behavior against its pinned SDK:

- `http_req_duration` supplies duration, status and URL together through `ObserveRequest` and is the sole request-count source; skip the redundant `http_reqs` sample during normalization.
- Labels preserve the emitted CSV `url`: repeated URLs accumulate; query variants remain distinct within the budget. Treat custom source grouping as authoritative when it rewrites the emitted URL.
- By default, `data_received` is an aggregate byte delta under `k6` via `AddAggregateReceivedBytes`, independent of the request budget even at zero. The opt-in `request_received_bytes` source supplies URL-attributed bytes through `AddReceivedBytes`; select exactly one byte source per run. `vus` calls `SetConcurrency`. Byte and VU samples update only their respective measurements.
- Optional selected LLM sources use the named timing/token recorder methods. CSV uses `encoding/csv` and named columns; required tags survive custom tag selection. The exporter owns source parsing and readiness/error state only, while the SDK owns metrics and collection lifecycle. Establish each engine's columns, units and error codes from its own output evidence.

## 5. Implement the SDK lifecycle

1. Resolve deployment configuration once with `agent.LoadAgentServerOptions`. Preserve resolved fields when adding callbacks and call `agent.NewAgentServer(options).Run()`.
   Native exporters use resolved `LabelsUsageLimit`; recorder integrations use supplied admission state. Reuse resolved options throughout startup. The SDK owns listeners, authentication, and coordinator connections.
2. Define `TestFileName`, output needs, and supported assignment modes. Consume per-agent settings and replicated/materialized CSVs unchanged. Reject unsupported native partitioning. Define units, bounds, and schedule precedence.
   If supporting dependencies, implement `PrepareDependencies` reconciliation, including removal when declarations are absent.
3. In `BeforeStart`, honor cancellation and invalidate prior prepared state. Reset adapter-owned parsing/exporter state. Validate settings/assignments and atomically prepare configuration in `RunContext.Workspace`. Preserve original uploads.
   Require successful preparation before command construction. `run.Metrics` is nil in `PrepareDependencies`, `BeforeStart`, and `BuildCommand`.
4. In `BuildCommand`, require the prepared run ID. Return an `agent.Command` with an executable, literal arguments, directory, and explicit environment overrides.
5. After launch, SDK-managed modes receive fresh immutable `run.Metrics` before postlaunch callbacks. Native modes always receive nil.
   `CustomIngestMetrics` runs initially and every 250ms, independently of scrapes, `Observe`, and `Progress`.
   Honor cancellation, ingest only new samples, and synchronize shared readiness/error state.
   The SDK joins live callbacks/processes before final ingestion with a fresh two-second `FinalMetricsTimeout` context. Drain remaining output within it. Live and final ingestion are serial.
   Accepted collection errors fail the run and registry scrapes. Final errors or deadline expiry also fail the run. HTTP gathers stored values. The SDK exclusively owns scheduling and final draining.
   - Successful launch publishes the registry. Completion retains it until prepare/start accepts a new run ID. Acceptance clears prior output immediately. Scrapes remain empty through preparation or launch failure. Duplicate, stale, and rejected triggers preserve output. Native exporters own their reset policy.
   - Optional `Observe` reports genuine readiness/progress within its deadline. Without it, launch acknowledges readiness. Optional `Progress` runs separately as a read-only lifecycle view. Use cached summaries for its final snapshot.
     Successful short runs may finish before readiness polling. Process failure fails the run, including after `WorkComplete` or readiness.
   - The SDK owns downloads, workspaces, launch, process groups, wait/reap, cancellation, stop escalation, and run serialization. Optional `Stop` must honor its deadline and leave primary-process waiting/reaping to the SDK. Preserve nonzero exits as failures.
6. Build a non-root Linux image with writable SDK paths, verified engine downloads, CA certificates, required sources/licenses, and SDK notices. Use adapter-directory targets with repository-root Docker context.
7. Create an adapter-local `Makefile` following `k6/Makefile`. Provide adapter build, runtime-image build, `test-unit`, `test-conformance`, and combined `test` targets.
   Scope Go commands to the adapter and shared conformance tests. Keep Docker context at the repository root.
   Document root invocation as `make -C <adapter> <target>` and adapter-directory invocation as `make <target>`.

## 6. Verify and deliver

Turn the agreed mapping into behavior-focused regression coverage and container conformance, using real engine output.

Import `github.com/reqfleet/engine-sdk/conformance` for shared container tests.
Follow `k6/conformance`: pass the real executable, native plans, settings, and source-derived expectations to `conformance.Run`.
Reuse its coordinator/artifact peers, lifecycle, and authenticated scrapes.
Keep engine parsing, scheduling, and failure scenarios in adapter tests.

Every reporting mode must pass shared normalized-metrics assertions.
Derive outcomes, latency, and bytes independently from native output or controlled workload evidence.
Keep the oracle independent of production parsing/exporting and normalized scrapes.
Exercise overflow and zero budgets.
Compare repeated/final request snapshots separately from changing resources.
Keep parser unit tests alongside the adapter.

Build shared regressions (`go test -c -tags=integration github.com/reqfleet/engine-sdk/conformance`)
and adapter integration tests into the Dockerfile's `conformance` target.
Run both sequentially with `docker run --rm --network none`, private ports, and private storage.
Include `go test github.com/reqfleet/engine-sdk/conformance` in the unit target.
Wire unit and container checks into CI and require container conformance before publication.
See repository instructions for the fixture contract.

Cover these observable behaviors:

- Success and protocol error; representative DNS/refusal/timeouts/TLS failures where supported; unknown/missing error metadata; zero-duration failures; an all-failing run. Compare exact outcome totals to emitted attempts.
- Repeated/distinct labels, query variants where applicable, overflow, zero budget, stable admission, reset, and independent status groups. Verify that separate gauge/byte samples update only their respective measurements and preserve source-provided attribution.
- Unit conversions, histogram buckets/sums/counts, repeated scrapes, final totals, exactly-once ingestion, and unavailable values. Test latency/status/bytes/concurrency and LLM effects independently.
  Cover whole-sample invalid-label rejection, the selected SDK's byte limit, and aggregate bytes at zero budget. Derive request counts exclusively from reported attempts.
- Natural completion, Stop, preparation cancellation, duplicate starts, stale stops, startup/settings failures, preparation reset, recovery, non-root execution, and assignment modes.
  For recorder modes, cover final drain, fatal collection errors, scrape failure, recovery, and concurrent readiness/progress.
  Verify empty output from accepted prepare/start until launch. Exercise authenticated `/metrics` with real peers. Check metrics availability and workload readiness independently.

Run `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` from the repository root.
Replace `<adapter>` with its directory. For k6 metrics changes, use `make -C k6 test-unit` and `make -C k6 test-conformance`.
Run integration binaries only in disposable containers with isolated ports and `/test-data`.
Add live smoke coverage for other behavior. Verify Shibuya onboarding and deployment separately.

Keep the adapter README's mappings, limitations and compatibility metadata aligned with implementation. For shared SDK updates, include compatibility metadata under each affected adapter directory and verify each affected adapter.

### Wire GitHub Actions publication

Follow the existing k6 release and publication wiring for each new adapter:

1. Register the adapter directory and component in [release-please-config.json](../../../release-please-config.json) and bootstrap its version in [.release-please-manifest.json](../../../.release-please-manifest.json). Let release-please generate subsequent versions, changelogs, component tags and GitHub releases. Keep each adapter independently versioned.
2. Add the adapter's `make -C <adapter> test-unit` and `make -C <adapter> test-conformance` checks to [.github/workflows/pr.yml](../../../.github/workflows/pr.yml).
3. Extend [.github/workflows/publish.yml](../../../.github/workflows/publish.yml) with adapter-specific release-please job outputs sourced from `<adapter>--release_created`, `<adapter>--version` and `<adapter>--sha`. Keep publication driven by those outputs in the existing push-to-main workflow.
4. Add adapter-specific conformance and publication jobs gated on that component's `release_created == 'true'`. Check out the exact released SHA in both jobs. Run `make -C <adapter> test` in the conformance job and make publication depend on its success. Scope each job to its adapter's release and image.
5. Create `.github/actions/publish-<adapter>/action.yml` using the [publish-k6 composite action](../../../.github/actions/publish-k6/action.yml).
   Accept `version` and `registry-token`; expose immutable `digest`. Configure QEMU, Buildx, and GHCR login.
   Build `<adapter>/Dockerfile` with repository-root context, `runtime` target, and `linux/amd64,linux/arm64`.
   Enable provenance/SBOM and push `ghcr.io/reqfleet/engine-adapters/<adapter>:<version>`.
6. Invoke the action with a `v`-prefixed release-please version and `GITHUB_TOKEN`.
   Grant release-please `contents: write`, `issues: write`, and `pull-requests: write`.
   Grant publication `contents: read`, `packages: write`, `attestations: write`, and `id-token: write`.
   Enable release PR creation in Actions permissions. Verify GHCR access and visibility for the intended distribution.
7. Write `ghcr.io/reqfleet/engine-adapters/<adapter>@<digest>` to `GITHUB_STEP_SUMMARY`.
   Verify conformance, publication, package access, and digest before operator handoff.
   Keep published tags and image versions immutable.

Deliver executed checks, results, blockers, and the tested tuple:
`(Shibuya release, immutable adapter image digest, SDK version, engine version, shibuya-v1)`.
Mark unverified releases or digests unknown.
Require observed workflow success, package access, and an immutable digest before publication handoff.
Give the Reqfleet team the reporting contract and evidence for control-plane integration and rollout.
