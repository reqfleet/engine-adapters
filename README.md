# External engine adapters

Independent, operator-managed engine adapters for Reqfleet Shibuya. The reference is an HTTP-only [k6 adapter](k6/README.md), using the public [engine-sdk v0.3.0](https://github.com/reqfleet/engine-sdk/tree/v0.3.0). Adapter, engine, SDK, and Shibuya versions are independent.

**Publication:** source is maintained at [reqfleet/engine-adapters](https://github.com/reqfleet/engine-adapters). Versioned OCI images use `ghcr.io/reqfleet/engine-adapters/k6`. Check [releases](https://github.com/reqfleet/engine-adapters/releases), successful publication workflow output, package visibility, and the immutable digest before deployment. A published adapter does not mean your Shibuya deployment admits or installs it.

## Build and exercise the standalone module

Prerequisites: Go **1.26.10**, Docker with Linux containers, and public HTTPS access to Go modules, Debian packages, and the k6 release. There is one module, no workspace, private dependency, Shibuya checkout, or `replace` directive.

```sh
make test-unit       # go test -race -count=1 ./...
make test-conformance
make image IMAGE=k6-adapter:local
# Or run both test targets:
make test
```

`make test-conformance` builds the Dockerfile's `conformance` target and runs a disposable container with **no host mounts, published ports, or external network**. Its privileged test process supplies an ephemeral HTTPS artifact peer on 443, TLS command peer on 2416, and HTTP target on 18080. The real adapter runs as UID/GID 65532 and exposes the SDK server on 8080. The harness exercises real k6 HTTP 200/500 responses, a full one-minute run, optional hooks, original source preservation, CSV/normalized metric equality, retained final metrics, reused-process reset, duplicate starts, stale stops, blocked download and in-flight cancellation, settings/JavaScript/scheduling failures, failed thresholds, assignment modes, and recovery. It is intentionally not a standalone engine runner or substitute coordinator.

Do **not** invoke the integration test binary directly on your machine: it owns fixed ports and `/test-data`. Conformance takes longer than a minute because it proves natural completion, not just startup. Ordinary unit tests require no k6 binary. The harness is opt-in through the `integration` build tag inside its container. Source delivery is not evidence of successful execution: record actual test output and an immutable digest before claiming a tested release.

The initial reference was exercised on Linux arm64 and Linux amd64 (under QEMU), using real k6 container conformance. Its Shibuya integration was exercised in Kind with two agents, two then three VUs per agent, shared CSV split/copy, mixed Replay logs, natural completion, Stop, failed startup, cancellation and same-pod recovery. That integration used a modified Shibuya checkout based on `44e1381b7ad31b0172aadba107e583663e16c74b`, **not a released compatibility guarantee**. Select a Shibuya release containing the static k6 admission/UI/chart changes and repeat the deployment smoke before production rollout.

Dependency proof used by CI:

```sh
GOWORK=off go list -deps -test -json ./... | jq -e -s 'all(.[]; (.ImportPath | startswith("github.com/reqfleet/shibuya") | not))'
GOWORK=off go list -m -json all | jq -e -s 'any(.[]; .Path == "github.com/reqfleet/engine-sdk" and .Version == "v0.3.0") and all(.[]; .Replace == null)'
```

## Responsibilities and manual onboarding

### Adapter author

1. Use SDK callbacks (`BeforeStart`, `BuildCommand`, `Observe`) and let the SDK own downloads, run isolation, process launch/wait, cancellation and escalation. Do not implement a second supervisor.
2. Define the primary artifact, settings units, per-agent assignment and CSV delivery rules. Bundle nonnative JavaScript dependencies before upload. The supported onboarding contract is one primary artifact plus existing CSV delivery, not arbitrary archives or dependency manifests.
3. Normalize accurate metrics to `shibuya-v1`; document unsupported attribution instead of manufacturing request events. Use native normalized-endpoint mode when engine samples are not request events.
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

Upload one bundled `.js` plus optional CSVs, choose per-agent VUs, duration in minutes, rampup seconds and engine count, then trigger and inspect aggregate `k6` results. Two engines with concurrency 2 produce four total VUs; the adapter never divides that assignment again. See [the detailed contract and examples](k6/README.md).

## Releases and compatibility

PR CI runs race/unit tests, public dependency checks and Docker conformance. A version tag such as `v0.1.0` reruns conformance and publishes the Dockerfile's `runtime` target for Linux amd64 and arm64 to `ghcr.io/reqfleet/engine-adapters/k6:<adapter-release>`, with provenance and SBOM. Maintainers must configure GHCR permissions/public visibility and retain release evidence. Production operators select the resulting digest, not a floating tag.

Record this **manually tested** tuple for every rollout:

```text
(Shibuya release, adapter image digest, SDK v0.3.0, k6 v2.3.0, metrics shibuya-v1)
```

Do not fill unspecified Shibuya releases or digests with presumed compatibility. SDK v0.3.0 has no wire-protocol negotiation/version field. This relaxed issue #1155 item 4 supports maintainer-managed external onboarding; it does **not** satisfy the original no-source-rebuild or automatic unsupported-protocol-version rejection criteria. No dynamic catalog, hot reload or automatic registration is claimed.

## Licensing

Adapter code is Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The public SDK harness and cgroup code are attributed adaptations. The OCI image also distributes unmodified k6 v2.3.0, a separate AGPLv3 program; its license and corresponding upstream source archive are included under `/usr/share/licenses/engine-adapters/`, with SDK transitive notices. Review upstream and transitive licenses before redistributing. Publication maintainers must preserve corresponding-source availability and notices for all shipped versions.
