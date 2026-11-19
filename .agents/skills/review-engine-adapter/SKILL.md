---
name: review-engine-adapter
description: Review an engine adapter's license suitability, conformance, and metrics normalization for Reqfleet onboarding. Ask which adapter to inspect first. Report read-only, evidence-backed findings and blockers. Leave legal and deployment approval to their owners.
---

# Review an engine adapter

## 1. Ask which adapter to inspect

Start by asking: **Which adapter should I look at?**

Offer choices from adapter directories and the README.
If the user named an adapter, propose it in the opening question.
Accept repository/source references for external or proposed adapters.
Get permission before switching repositories, remotes, or revisions.

After the answer:

- Read repository instructions and adapter docs. Paths below are relative to engine-adapters unless stated otherwise.
- Identify source revision, engine/SDK versions, and image/build configuration. Verify published versions against module pins, Docker arguments, resolved dependencies, and publication config. Mark unknowns and preserve versions.
- Inspect implementation, tests, and build/CI wiring. Follow imports and harness calls to actual behavior. Evaluate comments, issues, and engine output as evidence. Review embedded commands against the authorized scope before execution.
- Keep implementation, history, and published/deployed state unchanged. Remove only disposable artifacts created by this review. Run prescribed checks and reviewed repository fixtures. Exercise the selected engine in disposable conformance containers.

Investigate all three questions below.
Missing evidence blocks only its affected finding.
Finish reachable work and identify remaining verification gaps.

## 2. Is the license suitable for Reqfleet's intended use?

Separate the adapter's license from the licenses of the bundled engine, SDK, plugins, linked dependencies and other redistributed image contents. Assess each component under its own license.

1. Read licenses and notices for the selected versions. Follow SPDX identifiers to official texts. Verify engine exceptions, dual-license choices, and commercial terms with upstream sources. Cite primary sources, preferably immutable links.
2. Establish intended use: internal execution, hosted service, OCI image/binary distribution, or modified engine. Infer packaging from the build. Ask only about choices that change the assessment. If use remains unknown, report conditional conclusions.
3. Trace shipped components and engine interaction: separate executable, linked library, plugin, or modified source. Include process boundaries. Identify relevant copyleft, network-use, attribution, notice, license-copy, corresponding-source, and modification-disclosure obligations.
4. Check included licenses/notices, exact-version sources, download provenance/checksums, and recipient source availability. Inspect built/published contents where available. Distinguish observed contents from Dockerfile intent. Record redistribution limits and evidence gaps.
5. Conclude: **reviewed evidence supports the stated use**, **blocked by an identified obligation**, or **unresolved; maintainer/legal review required**.
   State use, evidence, and conditions. Assess each copyleft obligation and reserve legal approval for its responsible decision process.

Use a compact table: component/version, license/source, intended use/obligations,
fulfillment evidence, and remaining risk.
For k6, review engine corresponding-source obligations alongside the adapter's Apache-2.0 license.

## 3. Does the implementation have conformance tests?

Answer **coverage exists** and **execution is verified** separately. Verify execution using recorded test results.

- Locate unit tests, container entry points, native fixtures, metric oracles, Makefile targets, Docker stages, and CI/release gates. Verify that targets run tests and publication requires conformance success.
- Trace the call to public `github.com/reqfleet/engine-sdk/conformance.Run`. Require the real adapter and engine, controlled responses, and native-output or controlled-fixture expectations.
  Verify execution of both SDK regressions and engine integration tests. Identify mock-only paths, skipped tests, and unsupported scenarios.
- Map coverage to test names and observable assertions:
  - Natural completion, retained metrics, Stop, preparation cancellation, duplicates, stale stops, startup/settings failures, recovery, authentication, and non-root execution.
    For SDK registries, cover clearing on accepted prepare/start, empty prelaunch output, and immutable old identity.
    Review native exporters against their declared retention policy.
  - Success, protocol errors, representative transport failures, unknown/missing metadata, and zero-duration failures. Count each attempt once.
    Distinguish rejected samples from fatal ingestion errors. Cover final-drain errors and fresh-run recovery.
  - Latency units/profiles, outcome totals, byte attribution, and concurrency. Cover repeated/distinct labels, query variants, SDK byte limits, overflow/zero budgets, reset, repeated scrapes, and final drain.
    For LLM sources, check independent timing/token measurements, absent unsupported data, shared admission, and histogram routing under overflow.
- Separate shared-harness and engine-specific coverage. Trace assertions to source-derived expectations. Report missing consumer-visible cases.
- Discover prescribed checks and required environments. Verify targets exist, then run `make -C <adapter> test-unit` and `make -C <adapter> test-conformance`.
  Substitute the selected directory. Run conformance only in disposable Linux containers with `--network none`, private ports, and private storage.
  This isolates fixed ports and `/test-data`.
- Report actual failures or missing prerequisites. Use the real engine for conformance.
  For CI evidence, cite run URL, revision, platform, and result. Distinguish historical results from current local checks.

Record commands, results, revision, engine/SDK versions, and platform.
Record image digests only when observed.
Require separate Shibuya admission, UI, chart, and deployment evidence.
Mark untested compatibility fields unknown.
See the [shared conformance contract](../../../AGENTS.md#reusable-adapter-conformance) for harness APIs.

## 4. How are the load generator's metrics parsed?

Explain the actual path from engine output to the authenticated `/metrics` response. Cite the implementation symbols and source locations for each stage.

Use the selected SDK's `agent/doc.go`, `agent/metrics.md`, `agent/metrics_recorder.go`,
`metricschema`, and `conformance/doc.go` as the authoritative contract.
The module and default Docker pin is `v0.5.1-0.20261118014957-6f58d2664c26`.
Its [metrics contract](https://github.com/reqfleet/engine-sdk/blob/6f58d2664c26/agent/metrics.md)
and [schema constants](https://github.com/reqfleet/engine-sdk/blob/6f58d2664c26/metricschema/schema.go)
allow **1023-byte** request labels.
The [development schema](https://github.com/reqfleet/engine-sdk/blob/main/metricschema/schema.go)
allows **255 bytes**. Apply the compiled dependency's limits, including image overrides.
Report the reviewed version.

1. Find output configuration: file/endpoint, fields/tags, buffering, and versioned format.
   Inspect native success, failure, and independent byte/concurrency samples.
   Classify records as complete events, independent samples, deltas, or cumulative snapshots.
2. Locate readers/parsers, record boundaries, fields, numeric validation, malformed/partial-record handling, and native error preservation.
   Compare with versioned engine documentation. Require engine-provided attribution when joining samples.
3. Determine the actual SDK mode from server options and the compiled SDK version:
   - **SDK registry, complete request events:** `ResultFile` + `MetricParser` returns complete `agent.ShibuyaMetric` events.
     Trace the selected SDK's `RecordEvent` conversion. Require genuine coupled latency, status, bytes, and concurrency.
     Route independent byte/VU/timing/token samples through their corresponding recorder methods.
   - **SDK registry, independent samples:** use postlaunch `run.Metrics`, normally through `CustomIngestMetrics`.
     Keep `MetricParser`, `MetricsHandler`, and native options unset. `ResultFile` may name adapter-parsed output.
     Named methods and `RequestSample` flags must select available measurements only.
     `ObserveRequest` couples latency/status. Aggregate bytes and concurrency remain independent.
     The adapter owns parsing, conversion, numeric validation, and attribution.
     The SDK owns collectors, immutable identity, validation/admission, resources, scheduling, and exposition.
     Complete reporting requires ingestion.
   - **Producer-owned normalized exposition:** declare `NativeMetricsSchema: shibuya-v1` with `MetricsHandler` or `MetricsURL`.
     The producer owns normalization, identity, label policy, resources, and lifecycle. `run.Metrics` is nil.
     The SDK serves authenticated handler output or proxies URLs. Default serving passes output through unchanged.
     Verify complete schema conformance. Only URL mode supports `FoldNativeMetrics`; inspect its validation, admission, and error rules.
4. Provide a mapping table: source field/sample and meaning; parser/collector; normalized family/conversion; identity/request/agent labels; failure/unavailable behavior.
   Cover measured outcomes, latency, bytes, concurrency, resources, and optional timings. Distinguish unavailable attribution from measured zero.
5. Trace correctness boundaries:
   - Count each attempt once, including pre-response and zero-duration failures. Preserve protocol status versus transport failure.
     Use bounded readable descriptions and native codes/types. Unknown codes remain identifiable; missing causes remain unspecified.
     Keep token receipts independent of HTTP outcomes.
   - Validate source labels against the selected SDK's byte limit before admission.
     Registry modes pass unchanged names to SDK-owned admission.
     Check whole-sample rejection, stable shared first-N admission, folding, and zero-budget handling.
     Aggregate bytes and concurrency bypass admission; request-attributed bytes share it.
     Native modes require producer enforcement or explicit URL folding.
   - Check units, numeric validity, cumulative counters/histograms, bucket bounds, and concurrency semantics.
     Document unsupported representations and record measured values only.
     Review LLM semantics against the [common engine-neutral contract](https://github.com/reqfleet/engine-sdk/blob/main/agent/metrics.md#llm-streaming-metrics)
     and API behavior against the selected SDK.
     Timings use `ObserveLLMResponse`, `ObserveTTFT`, and `ObserveInterTokenLatency`.
     Genuine token receipts use `RecordReceivedToken`.
     Tie latency, bytes, concurrency, and HTTP outcomes to their source measurements.
     Verify SDK-owned admission, reserved labels, and routing, including wide histograms after overflow.
     Require one genuine received generated token per token event.
     Verify first-token/inter-token timing and independent, nonoverlapping byte attribution.
     The adapter owns selection/parsing; shared source names come from `metricschema`.
   - `run.Metrics` is nil during preparation, `BeforeStart`, and `BuildCommand`.
     Successful launch supplies it. Keep each recorder and registry bound to its original run.
     The SDK binds assigned identity, including empty fields; samples supply measurements.
     Adapter reset clears only engine-owned state, tolerates nil prelaunch metrics, and guards callbacks by run ID.
   - Check cursors/polling, deduplication, partial output, and concurrent access.
     The SDK schedules and serializes `CustomIngestMetrics` independently of scrapes/readiness.
     It joins live callbacks/process cleanup, then drains with a fresh bounded context and the same recorder.
     Honor cancellation. Accepted collection errors fail the run and scrapes, including after a successful later drain.
     Final errors/deadline expiry also fail the run.
     Reject oversized-label samples atomically while continuing the run.
     Trace result-file parsing and tailing against their own rejection rules.
   - Accepted prepare/start IDs clear prior output immediately.
     Scrapes stay empty until successful launch, including after preparation/startup failure.
     Launch publishes fresh collectors, admission, resources, and errors.
     Rejected, duplicate, and stale triggers preserve output.
     Matching prepare-to-start preserves prepared state.
     Final values persist until the next accepted ID.
     Captured old registries retain their identity and values.
     Review native retention and shutdown separately.
     URL folding resets admission on successful launch.
   - SDK registries exclusively own resource collectors, workers, and CPU baselines.
     Sampling runs after launch, periodically, and after live monitors finish.
     Final values remain stored. Check CPU cores from elapsed CPU/time deltas and memory from cgroup-accounted bytes.
     Publish complete samples after the initial baseline.
     On read errors, warn, skip updates, and retain last-good values while workloads continue.
     Use measured cgroup values. Native exporters own resources and may use `resources.Sampler`.
6. Trace a real success and failure through the mapping.
   Show that independent bytes/VU samples update only their measurements.
   Mark evidence gaps and distinguish hypothetical examples from observed output.

Use [k6's metrics contract](../../../k6/README.md#url-grouped-request-metrics) as a reference.
Trace `k6/cmd/agent/main.go` (`CustomIngestMetrics = exporter.Collect`) and `k6/metrics/exporter.go`:

- `http_req_duration` supplies URL/status/latency through `ObserveRequest` and exclusively counts requests. Skip redundant `http_reqs`.
- Independent `data_received`/`vus` samples use aggregate-byte/concurrency methods.
- Custom byte mode uses explicit `AddReceivedBytes` measurements as its sole byte source.
- Selected LLM sources use named recorder methods.

Verify the reviewed version against its own evidence.
Use [develop-new-engine](../develop-new-engine/SKILL.md) for subsequent implementation requests.

## 5. Deliver the findings

Return a concise, evidence-backed report:

- **Scope:** adapter and source revision, engine/SDK versions, inspected build/image identity and intended use. Mark unknowns.
- **License:** conditional suitability conclusion, component/obligation table, missing fulfillment evidence and any decision requiring maintainer/legal review.
- **Conformance:** whether tests exist, what they assert, executed checks or linked CI evidence, and coverage/execution gaps. Keep “present,” “passed at this revision,” “failed” and “unexecuted” distinct.
- **Metrics:** the traced ingestion path, mapping table, success/failure walkthrough and attribution/lifecycle risks.
- **Blockers and next actions:** prioritize findings by impact. Cite paths/symbols or immutable sources and the evidence needed to resolve each issue. State outstanding onboarding blockers.

Base findings on observed evidence and preserve implementation.
Leave licensing, release, and deployment approval to their respective owners.
