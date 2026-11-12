# Repository Instructions

## Pull request mentions

- Check the PR author's username before tagging `@iandyh`. Only tag `@iandyh` when the PR author is not `andyh`; never tag `@iandyh` on a PR authored by `andyh`.

## Release management

- Use release-please to manage releases and generate changelog updates.
- Do not manually create or edit `CHANGELOG.md`; leave changelog changes to release-please.

## Documentation

- Keep the root `README.md` limited to minimal human-facing project information, quick-start commands and links.
- Keep detailed agent development, verification, onboarding and release instructions in this file. Keep adapter usage and metrics contracts in `k6/README.md`.

## Report readable errors across all adapters

- Every adapter MUST translate engine-specific error codes or types into stable, easily readable descriptions when reporting errors in metrics. Do not expose only opaque codes or failure sentinels; retain the native code or type alongside the description for diagnostics when available.
- Preserve the engine's actual failure classification. Distinguish DNS, connection refusal, connection timeout, response timeout and TLS failures when the engine provides that information. Never infer a more specific cause from the target configuration or reinterpret a timeout as a refusal.
- Preserve native error metadata during ingestion. Unknown codes must remain identifiable with an explicit unknown-error description; missing details must be reported as an unspecified failure, not success or a guessed cause. Keep genuine protocol response statuses distinct from transport failures.
- Use bounded, deterministic descriptions for metric labels. Do not label metrics with raw error messages containing changing addresses, ports, URLs, request IDs or sensitive data.
- Count each reported failed request exactly once, including failures before a response or with zero recorded duration. Preserve available attribution and timing without fabricating response statuses, latency or bytes.
- Document each adapter's error mapping and verify representative failures, unknown or missing metadata, and unchanged successful-response reporting through regression coverage and live conformance where supported.

## Request labels are a reporting contract

For the k6 adapter, group request metrics by the CSV `url` value, subject to the byte limit and admission policy below:

- Use the URL as `label` for latency histograms and status counters. Repeated valid labels accumulate together; distinct values remain separate within the label budget.
- Do not collapse request metrics under an engine-wide `k6` label. Missing URL attribution for received bytes is not a reason to discard URL attribution from latency or status metrics.
- Do not substitute the CSV `name` column, normalize paths, truncate values, or remove query strings. k6 can rewrite the `url` tag when a script uses custom name/grouping options; do not invent a discarded original URL.
- Validate request labels with the SDK's `agent.ShibuyaMetric.LabelIsValid()`. Preserve valid values exactly. Drop an oversized `http_req_duration` sample, including both its latency observation and status count, before admission without failing the run. Labels of 1024 bytes or more must not consume slots, enter overflow, or merge into shorter prefixes. This applies equally to URLs and grouping names; independent byte and VU samples remain unaffected.
- Apply the same request-name admission decision to latency and status with the SDK's `metricschema.AdmitRequestName`; use `metricschema.OverflowRequestName` instead of an adapter-local overflow string. Admit the first `label_limit` distinct valid request names per run, keep admitted names stable, and fold additional valid names into `others`. The reserved overflow name never consumes a slot. A zero limit folds all valid request names into `others`; it does not bypass validation. Request names are emitted under the unchanged Prometheus `label` key.
- Bound admission state by the label limit and clear it on every run reset, together with metric collectors. Keep run, plan, collection, agent and zone identity in their own schema labels.

## Preserve metric attribution boundaries

k6 CSV rows are independent metric samples, not complete request records:

- An `http_req_duration` sample supplies latency, HTTP status and URL together. Use it for the URL-grouped latency observation and status count; do not also count `http_reqs` and double-count requests.
- Standard `data_received` samples lack request URL attribution. Keep received bytes aggregate under `label="k6"` (`others` when the label limit is zero). They must not consume URL admission slots or produce per-URL byte series.
- Per-URL bandwidth is unavailable, not zero. Never assign bytes to requests by row order, matching timestamps, or copying totals to every URL.
- VUs, CPU and memory remain agent-level metrics, not request-labelled measurements.
- Record independent samples through SDK `MetricsRegistry.ObserveRequest`, `AddReceivedBytes`, and `SetConcurrency`. Do not route VU/byte samples through `RecordEvent` or `MetricParser`, which also create request counts and latency observations.

## Use SDK-owned container resource reporting

- Adapters using the SDK registry MUST NOT define duplicate collectors, instantiate a resource sampler, or maintain CPU baselines. The SDK owns the canonical metrics and resource collection; `resources` owns sampling policy and `containerstats` owns raw cgroup reads.
- The SDK samples resources after launch, every five seconds and once after live monitors finish, then retains completed values without further reads. Baseline-only attempts omit both CPU and memory; complete samples publish both. Resource read failures warn and skip the update, retaining last-good values without failing HTTP or the workload.
- CPU is average cores over the actual monotonic interval. Memory is cgroup-accounted bytes, including cache. Both measure all processes in the container's cgroup, not just the engine PID. Never add host/process fallbacks or fabricate zero usage.
- Standalone native exporters remain responsible for their complete exposition and may use `resources.Sampler` directly. Native handler/URL modes do not also collect unused SDK resources. See [the resource reporting contract](k6/README.md#normalization-and-run-lifecycle).

## Use SDK-owned configuration and HTTP serving

- Resolve deployment options once with `agent.LoadAgentServerOptions` and use `AgentServerOptions.LabelsUsageLimit` for the exporter. Do not add adapter-local environment parsing.
- Preserve resolved options when adding adapter callbacks. Start with `agent.NewAgentServer`, not `MakeAgentServer`, which would resolve environment values again.
- Create one `agent.MetricsRegistry` and pass it to the exporter and `AgentServerOptions.MetricsRegistry`. Set `CustomIngestMetrics` to the exporter's ingestion callback for engine samples that do not fit `MetricParser`'s complete-request event model; leave `MetricParser`, `MetricsHandler`, `MetricsURL`, and native options unset. The SDK owns authentication, resource sampling, ingestion scheduling and exposition.
- Keep CSV parsing, normalization and URL admission in the exporter. Collect with the callback context, synchronizing adapter state with observation. The SDK joins live callbacks/processes before a final collection call with a fresh two-second deadline. Call `registry.Reset()` together with parser/admission reset in `BeforeStart`. HTTP only gathers stored metrics; do not wrap it to drain output or sample resources.

## Preserve and verify the contract

- Retain both `url` and `status` in k6 `systemTags`, including when a plan supplies an explicit tag list; preserve the other requested tags.
- Parse CSV with `encoding/csv`, locate required columns by name, and reject missing URL columns or empty URLs on HTTP duration samples. Non-request samples may have empty URL fields.
- Regression coverage must distinguish multiple URLs, repeated requests to one URL, query-string variants, and URL versus name columns. Cover ASCII and multibyte byte-length boundaries, oversized-sample rejection before admission, custom grouping names, no prefix merging, overflow, zero budget, reset, independent status counts, and aggregate-only bytes.
- For metrics behavior changes, run `make -C k6 test-unit` and container-only `make -C k6 test-conformance` from the repository root, plus a live smoke when adding behavior not exercised by conformance. Never run the integration binary directly on the host; it owns fixed ports and `/test-data`.
- Keep [the metrics documentation](k6/README.md#url-grouped-request-metrics), exporter tests, and live conformance assertions aligned with these semantics.

**Publication:** source is maintained at [reqfleet/engine-adapters](https://github.com/reqfleet/engine-adapters). Versioned OCI images use `ghcr.io/reqfleet/engine-adapters/k6`. Check [releases](https://github.com/reqfleet/engine-adapters/releases), successful publication workflow output, package visibility, and the immutable digest before deployment. A published adapter does not mean your Shibuya deployment admits or installs it.

## Build and exercise the standalone module

Prerequisites: Go **1.26.10**, Docker with Linux containers, and public HTTPS access to Go modules, Debian packages, and the k6 release. There is one module, no workspace, private dependency, Shibuya checkout, or `replace` directive.

Build targets belong to each adapter's directory. From the repository root, use `make -C k6` for the k6 adapter; inside `k6/`, use `make` directly. Go builds are scoped to that adapter; its unit target also runs the SDK conformance validator tests. Docker builds keep the repository root as their context for the shared module and licenses; the harness comes from the pinned SDK dependency.

```sh
make -C k6 test-unit       # adapter and shared conformance validator, with race detection
make -C k6 test-conformance
make -C k6 image IMAGE=k6-adapter:local
# Or run both test targets:
make -C k6 test
```

`make -C k6 test-conformance` builds the Dockerfile's `conformance` target and runs a disposable container with **no host mounts, published ports, or external network**. Its privileged test process supplies an ephemeral HTTPS artifact peer on 443, TLS command peer on 2416, and HTTP target on 18080. The real adapter runs as UID/GID 65532 and exposes the SDK server on 8080. The harness exercises real k6 HTTP 200/500 responses, a full one-minute run, optional hooks, original source preservation, CSV/normalized metric equality, retained final metrics, reused-process reset, duplicate starts, stale stops, blocked download and in-flight cancellation, settings/JavaScript/scheduling failures, failed thresholds, assignment modes, and recovery. It is intentionally not a standalone engine runner or substitute coordinator.

Do **not** invoke the integration test binary directly on your machine: it owns fixed ports and `/test-data`. Conformance takes longer than a minute because it proves natural completion, not just startup. Ordinary unit tests require no k6 binary. The harness is opt-in through the `integration` build tag inside its container. Source delivery is not evidence of successful execution: record actual test output and an immutable digest before claiming a tested release.

The initial reference was exercised on Linux arm64 and Linux amd64 (under QEMU), using real k6 container conformance. Its Shibuya integration was exercised in Kind with two agents, two then three VUs per agent, shared CSV split/copy, mixed Replay logs, natural completion, Stop, failed startup, cancellation and same-pod recovery. That integration used a modified Shibuya checkout based on `44e1381b7ad31b0172aadba107e583663e16c74b`, **not a released compatibility guarantee**. Select a Shibuya release containing the static k6 admission/UI/chart changes and repeat the deployment smoke before production rollout.

### Reusable adapter conformance

The optional public [`github.com/reqfleet/engine-sdk/conformance`](https://pkg.go.dev/github.com/reqfleet/engine-sdk/conformance) package supplies container lifecycle checks and normalized `shibuya-v1` metric assertions. The first consumer is `k6/conformance`; keep engine-specific plans, raw-output parsing and scheduling/error checks there. New adapters should import the SDK toolkit and supply their own engine-native fixtures. Do not restore an adapter-local generic harness.

`conformance.Contract` takes `Executable`, `Artifacts` (filename to content), optional `DataArtifacts` (SDK artifact URL path to content), a controlled `Target` handler, `LabelLimit`, `Basic`, `Budgets` and an optional `Scenarios` callback. `BasicScenarios` supplies `Complete` and `Reuse` fixtures (`File` and JSON `Settings`), `CompletionTimeout`, and `AssertMetrics(t, runtime, runID)`. That oracle reads native output under `runtime.Root` and calls `conformance.AssertMetrics` with expected identity, per-label outcomes/latencies, bytes, concurrency and resource availability. It returns a resource-independent `MetricsSnapshot` for retention comparisons. `BudgetScenario` supplies a fixture, label limit, minimum request count and exact expected request/byte labels. Use the k6 consumer for a complete runnable example.

The shared lifecycle sequence covers natural completion, retained repeated scrapes, duplicate starts, stale stops, reused-run cancellation, blocked artifact download cancellation and recovery. A completion fixture may finish before the first progress poll; cancellation scenarios still require an observable running state. The runtime also checks bearer rejection and the adapter's UID/GID. Adapter callbacks use `Runtime.Start`, `Running`, `Stop`, `Terminal`, `Scrape` and the other exported helpers for additional scenarios.

Each scenario launches the adapter through a dedicated Linux child-subreaper process. This supervisor owns engine descendants across separate process groups and reaps exited adopted descendants while the adapter is still running. After graceful Stop, cleanup forcibly terminates remaining descendants within a bounded interval, including children orphaned by an adapter exit. Unconfirmed cleanup terminates the container suite before another scenario can inherit those processes. The supervisor remains root; the adapter and workloads retain UID/GID 65532.

Cleanup must stop the admitted active run even when a later overlapping Start was rejected; an attempted run ID must not displace the active run's graceful cancellation and finalization. Use one graceful-cleanup deadline across all Stop writes and progress requests, then enter bounded supervisor teardown even if the adapter is unresponsive.

Build the shared runtime regression binary with `go test -c -tags=integration github.com/reqfleet/engine-sdk/conformance` alongside the adapter-local test binary. The k6 container runs the shared binary first, then its real-engine suite; both must succeed. Shared runtime regressions exercise an SDK-based short workload, unsuccessful terminal states and cleanup after an unresponsive or exited adapter while an unrelated sibling process remains alive.

Prepare regressions must send the nested Prepare verb with its per-agent assignment and exercise a dependency artifact and preparation hook. Prove cancellation while the adapter is still alive, before forced teardown; a closed download connection after killing the adapter is insufficient evidence.

- Add an adapter-local integration test calling `conformance.Run(t, contract)`. Supply the real executable, artifact contents, settings and controlled target behavior. The shared runtime supplies TLS coordinator/artifact peers, authenticated scrapes and a non-root child process.
- Provide successful-completion and reuse fixtures plus source-derived metric expectations. Exercise both successful and failed outcomes, exact request counts, received-byte attribution, concurrency, latency units and histogram buckets. The shared metric validator accepts normalized output from either SDK request-event ingestion or producer-owned exposition.
- Optional `shibuya_latency_llm_resp_milliseconds` observations use `RequestExpectation.LLMResponseLatenciesMS` and the SDK's separate wide histogram bounds. Use the reserved lowercase `llm_resp` label, or `others`/`_other_` for folded observations; arbitrary request URLs are not LLM response labels. The snapshot exposes `LLMResponseLatency`. Keep ordinary `LatenciesMS` independent: a nil slice requires that histogram to be absent; a non-nil empty slice expects an explicit zero-observation histogram.
- Supply label-budget fixtures with repeated labels and distinct query variants where relevant. Run positive overflow and zero-budget cases against the real adapter. Preserve the adapter's declared byte attribution when folding request labels.
- Keep additional engine-specific checks for parser failures, native error mappings, scheduling, CSV assignment, hooks and unsupported options in the adapter's scenario callback and unit tests.
- State the producer's run-retention policy in its fixtures. SDK result-file registries can retain prior-run series; validate current-run values independently and explicitly allow only known prior identities. Retained series must remain unique per metric family and complete label set. Histograms must have the complete profile's bucket bounds, cumulative counts and `+Inf` count matching the sample count; zero observations require a zero sum. Request labels must be shorter than 1024 bytes in current and retained series. For exporters that reset their collectors, require old-run series to disappear. Final request snapshots exclude changing CPU/memory gauges when checking repeated scrapes.
- Histogram component samples (`_bucket`, `_count`, `_sum`) must each be unique by complete label set, regardless of label ordering. Before typed parsing, reject base-family samples, require `le` only on buckets, validate each component's schema labels, and require raw bucket/count values to be finite nonnegative integers below `2^64`. These checks apply to current and retained runs and prevent the parser from discarding malformed samples or converting out-of-range counts.
- Build the adapter-local test package with `go test -c -tags=integration` in the Dockerfile and run that binary only in its disposable `conformance` target. Keep the existing isolation: no host mounts, published ports or external network; root harness, UID/GID 65532 adapter.
- Include SDK validator unit tests (`go test github.com/reqfleet/engine-sdk/conformance`) in each consumer's unit target and wire container conformance into PR and release CI. SDK toolkit upgrades require exercising every consumer.

The suite verifies adapter behavior through the public SDK protocol and `/metrics`. Coordinate Shibuya admission, UI and deployment verification with the Reqfleet team separately. Report executed checks and engine versions; a fixture definition alone supplies no runtime evidence.

### Configuring the engine SDK version

`k6/Dockerfile` defaults to `ENGINE_SDK_VERSION=v0.5.1-0.20261112221055-126a81e3e04d`, a pinned SDK snapshot providing the public conformance toolkit alongside per-agent canonical collectors, `CustomIngestMetrics` scheduling, final post-exit draining and ingestion-failure propagation. This argument selects the SDK compiled into the adapter and conformance executables, the bundled `SDK_THIRD_PARTY_NOTICES`, and the runtime image description. It is not a metadata-only override.

```sh
docker build -f k6/Dockerfile --target runtime \
  --build-arg ENGINE_SDK_VERSION=v0.5.1-0.20261112221055-126a81e3e04d \
  -t k6-adapter:sdk-126a81e .
```

Use an exact Go release version or pseudo-version. The build resolves the requested dependency after copying the source, then checks that its resolved version matches the argument; mutable aliases such as `main` and `latest` are rejected. An SDK that lacks the APIs used by this adapter will fail compilation rather than silently falling back.

The override changes module files only inside the Docker build. The checkout's `go.mod` and `go.sum`, ordinary Go commands and host CI still use the repository pin. Keep the Dockerfile default, module files and documentation aligned when upgrading the repository's SDK. Before publishing an overridden image, build and run the `conformance` target with the same SDK argument and verify the executable's SDK version, image metadata and notices.

### Configuring the k6 version

`k6/Dockerfile` defaults to `K6_VERSION=v2.3.0`. Use the upstream release tag, including its leading `v`. The argument controls the binary download, corresponding source archive, license download and runtime image description.

When selecting another release, also override `K6_CHECKSUMS_SHA256` with the SHA-256 digest of that release's `k6-<version>-checksums.txt` asset from trusted release metadata. This is the checksum file's digest, not the binary archive's digest. The build verifies both the checksum file and the selected architecture archive; a mismatched pin fails the build.

For example, to build the verified v2.2.0 release:

```sh
docker build -f k6/Dockerfile --target runtime \
  --build-arg K6_VERSION=v2.2.0 \
  --build-arg K6_CHECKSUMS_SHA256=cecbd810434043492a60fc6f2d32f60036118927e0164757271823f6c96c64f2 \
  -t k6-adapter:k6-v2.2.0 .
```

Before publishing an override, build the `conformance` target with the same arguments and run it with `docker run --rm --network none`. Verify the runtime's `k6 version`, image description and versioned source archive under `/usr/share/licenses/engine-adapters/`. Do not assume another engine version is compatible without conformance and deployment smoke evidence. `k6/Makefile` and the release workflow use the Dockerfile defaults; change both default arguments together when updating the published engine version.

## Responsibilities and manual onboarding

### Review an adapter

Invoke [`review-engine-adapter`](.agents/skills/review-engine-adapter/SKILL.md) for an evidence-backed onboarding review. It starts by asking which adapter to inspect, then investigates license suitability for the intended Reqfleet use, conformance coverage and execution evidence, and the path from native engine output to normalized metrics.

The review is read-only apart from disposable verification artifacts. It identifies obligations, reporting risks and missing evidence; it does not grant legal, release or deployment approval. Use [`develop-new-engine`](.agents/skills/develop-new-engine/SKILL.md) when implementation work is requested.

### Adapter author

1. Use SDK callbacks (`BeforeStart`, `BuildCommand`, `Observe`) and let the SDK own downloads, run isolation, process launch/wait, cancellation and escalation. Do not implement a second supervisor.
2. Define the primary artifact, settings units, per-agent assignment and CSV delivery rules. Bundle nonnative JavaScript dependencies before upload. The supported onboarding contract is one primary artifact plus existing CSV delivery, not arbitrary archives or dependency manifests.
3. Normalize accurate metrics to `shibuya-v1`; document unsupported attribution instead of manufacturing request events. Prefer the SDK registry's independent recording methods for independent samples. Retain native endpoint mode when the engine already supplies a complete normalized exposition.
4. Run conformance, review licenses/provenance, then publish an independently versioned image. Record the compatibility tuple below with observed test evidence.

### Shibuya maintainer: adding the next kind

Static onboarding intentionally requires source changes and rebuild/redeployment:

- Add the exact kind constant to `model/plan.go` and the independent `SupportedKinds` execution allowlist. Explicitly decide `UsesCollectionDataFile` and dependency-file support. Keep unsupported capabilities rejected.
- Add its primary suffix to `engines.NewPlanConfigRegistry`. Registry suffixes are **globally unique and case-sensitive**. If a suffix is already owned, choose a distinct engine-specific suffix and stage it under the engine's native filename in the adapter; do not weaken the registry.
- Optional safe upload validation uses `planvalidation.FromValidator`. Never execute uploaded scripts in the API. With a nil validator, preflight is explicitly skipped, not passed.
- Add operator-owned image/resources to chart values and render the full image reference. A configured image key must not admit unknown kinds to execution.
- Regenerate Swagger and the GUI API enum through the existing generators; add the static selector, filename/suffix and editor actions. Do not advertise AI generation or dependencies unless separately supported.
- Reuse coordinator original-artifact forwarding, explicit per-agent `RunSpec.Settings`, CSV copy/materialization, existing progress, Stop/Purge and reporting. No new coordinator switch, runtime registry, database schema or plugin loader is necessary.
- New richer assets or native non-CSV partitioning require a separately reviewed contract; they are not implied by this example.

### Operator

Review executable image provenance, licenses, resource needs and measured behavior. Configure pull credentials via existing `pull_secret` if private. Record and deploy a **full immutable digest** (not the Shibuya image tag) in `runtime.executors.k6.image`, alongside CPU and memory. The default image value must remain blank until an adapter is approved. A blank/missing image should fail as `engine "k6" has no execution image configuration`; pull failures require registry access/digest investigation, not relaxing pod security.

Deploy compatible API/controller code and image configuration before exposing a GUI that offers the kind. Restart config consumers, then purge/relaunch affected collections to replace agent pods after an image change. Update private-region watcher/CLI binaries from the same Shibuya release because they compile the shared lifecycle allowlist. A rollback uses the prior approved digest and compatible Shibuya release, followed by the same restart/relaunch procedure. Changing only a plan artifact or collection settings uses a new trigger after the previous run ends; it does not require Launch.

Run a real deployment smoke through API upload, collection creation, launch, trigger, metrics, Stop and Purge before advertising availability. Check per-agent VUs and data delivery, non-root pods, reused runs and failures. Container conformance does not prove Shibuya UI/admission/chart integration or private-region rollout.

### Plan author

Upload one bundled `.js` plus optional CSVs, choose per-agent VUs, duration in minutes, rampup seconds and engine count, then trigger and inspect URL-grouped request latency and status counts. Received bytes remain an agent/run aggregate because k6 does not attribute those samples to request URLs. Two engines with concurrency 2 produce four total VUs; the adapter never divides that assignment again. See [the detailed contract and examples](k6/README.md).

## Commit and open a pull request

Create a topic branch, run the new adapter's unit and container conformance targets, and document the metric mapping and actual test results. Stage only intended files. For example, for a new engine named `myengine` (replace it with your engine's name):

```sh
git switch -c feat/myengine-adapter
# Implement and verify the adapter, including CI and release wiring.
git add myengine/
# Also stage the specific shared module, CI and release configuration files you changed.
git diff --cached
git commit -m "feat(myengine): add engine adapter"
git push -u origin HEAD
gh pr create --base main --title "feat(myengine): add engine adapter" \
  --body "Describe the change, metrics contract, verification results and compatibility limits."
```

Use your fork as `origin` if you lack push access, and target `reqfleet/engine-adapters:main`. Follow the Conventional Commit rules in [Releases and compatibility](#releases-and-compatibility), and keep the PR title conventional for squash merges. Do not manually bump versions or edit changelogs.

After opening the PR:

- First, tag @iandyh in a PR comment to request review.
- Address failed CI checks and review feedback; include unit/race tests, real-engine container conformance and release wiring for any new adapter.
- Coordinate required Shibuya integration testing with the Reqfleet team before rollout; adapter container conformance does not replace it.
- Follow [Releases and compatibility](#releases-and-compatibility) for release PRs and publication. Check publication success and the immutable digest in the workflow summary, then follow the [operator rollout requirements](#operator).

## Releases and compatibility

PR CI runs race/unit tests and Docker conformance. On each push to `main`, [the release workflow](.github/workflows/publish.yml) runs release-please using [the component configuration](release-please-config.json) and [the version manifest](.release-please-manifest.json). Each adapter has its own version and release PR; there is no repository-wide adapter version or coupling to upstream engine versions.

The `k6` component tracks `k6/` with the Go release strategy. Release-please owns `k6/CHANGELOG.md` and the `k6` entry in the version manifest. The initial manifest version is `0.1.0`, and `bootstrap-sha` points to the existing historical `v0.1.0` release commit. Do not recreate or move that tag. Release-please manages subsequent version updates; do not manually edit changelogs or bump the manifest after bootstrap.

Use Conventional Commits for releasable changes: `fix(k6): ...` for patches and `feat(k6): ...` for features. Breaking changes use `!` or a `BREAKING CHANGE` footer; before 1.0.0 they bump the minor version. Selection is based on changed paths, not the commit scope. For shared changes such as SDK upgrades, include the corresponding compatibility or dependency metadata update under each affected adapter's directory in the releasable commit. A `go.mod`-only change does not select `k6`, even with a `k6` scope.

Merging the k6 release PR lets release-please create a GitHub release and a tag such as `k6/v0.2.0`. Its `k6--release_created`, `k6--version` and `k6--sha` outputs control the remaining jobs in the same workflow. No new k6 release means no k6 conformance or publication job. Both jobs check out the exact released SHA; publication requires conformance to pass.

The `k6--version` output is `0.2.0`, without a leading `v`. The workflow passes `v0.2.0` to the local [publish-k6 action](.github/actions/publish-k6/action.yml), which publishes the Dockerfile's `runtime` target for Linux amd64 and arm64 as `ghcr.io/reqfleet/engine-adapters/k6:v0.2.0`, with provenance and SBOM. The resulting immutable digest is recorded in the job summary.

The workflow uses `GITHUB_TOKEN`, matching the SDK repository. GitHub Actions must be allowed to create pull requests. Token-created tags and release PRs do not trigger other workflows, so publication deliberately uses release outputs instead of a tag-push trigger. Bot release PRs do not automatically receive pull-request CI with this token; the human merge into `main` triggers CI and the release workflow, including release-gated conformance. If required checks must run before merging bot release PRs, configure an appropriate GitHub App token. Manually pushing a tag does not publish an image.

When adding another adapter, add its directory/component to the release configuration, bootstrap its manifest version and add component-specific release outputs, gated conformance and publication jobs. Adapter-specific changes must not release unrelated images. Shared changes require testing and releasing each affected adapter independently. Existing tags and published image versions are immutable.

The action retains QEMU because the Dockerfile's target-platform `base` stage runs `apt-get`; building arm64 on the amd64 runner requires emulation even though Go is cross-compiled. Maintainers must configure GHCR permissions/public visibility and retain release evidence. Production operators select the resulting digest, not a floating tag.

Record this **manually tested** tuple for every rollout:

```text
(Shibuya release, adapter image digest, selected SDK version, selected k6 version, metrics shibuya-v1)
```

Do not fill unspecified Shibuya releases or digests with presumed compatibility. This relaxed issue #1155 item 4 supports maintainer-managed external onboarding; it does **not** satisfy the original no-source-rebuild or automatic unsupported-protocol-version rejection criteria. No dynamic catalog, hot reload or automatic registration is claimed.

## Licensing

Adapter code is Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The public SDK harness is an attributed adaptation; the SDK owns canonical collectors and container resource sampling. The OCI image also distributes the selected unmodified k6 release, a separate AGPLv3 program; its license and corresponding upstream source archive are included under `/usr/share/licenses/engine-adapters/`, with SDK transitive notices. Review upstream and transitive licenses before redistributing. Publication maintainers must preserve corresponding-source availability and notices.
