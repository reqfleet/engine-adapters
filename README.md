# External engine adapters

Independent, operator-managed engine adapters for Reqfleet Shibuya, built with the public [engine-sdk](https://github.com/reqfleet/engine-sdk). The current adapter supports HTTP load tests with [k6](k6/README.md).

## Development

Requires Go 1.26.10 and Docker with Linux containers.

```sh
make -C k6 test-unit
make -C k6 test-conformance
make -C k6 image IMAGE=k6-adapter:local
```

See [AGENTS.md](AGENTS.md) for detailed development, onboarding and release instructions, and the [k6 guide](k6/README.md) for supported plans, settings and metrics.

New adapters reuse the [container conformance suite](AGENTS.md#reusable-adapter-conformance), supplying engine-native fixtures and expected metrics. Run it alongside adapter unit tests for either SDK metrics approach.

### Add an engine with the skill

Open this checkout in an agent that supports repository skills and invoke `develop-new-engine`. If it is not discovered automatically, ask the agent to read and follow [`.agents/skills/develop-new-engine/SKILL.md`](.agents/skills/develop-new-engine/SKILL.md):

> Use develop-new-engine to add an adapter for my engine. Resolve the latest stable engine-sdk release, inspect the engine's output, and discuss request-event parsing versus normalized metrics with me before implementing reporting.

Bring the engine/version, a sample plan and the reports you need. The skill guides SDK integration, metric semantics, verification and Shibuya onboarding; an adapter alone does not register a new kind.

### Commit and open a pull request

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

Use your fork as `origin` if you lack push access, and target `reqfleet/engine-adapters:main`. Use Conventional Commits: `fix(<adapter>): ...` for patches, `feat(<adapter>): ...` for features, and `!` or a `BREAKING CHANGE` footer for breaking changes. Keep the PR title conventional for squash merges. Do not manually bump versions or edit changelogs.

### After opening the PR

- First, tag @iandyh in a PR comment to request review.
- CI currently runs k6 unit/race tests and real-engine Docker conformance. Address failed checks and review feedback; include checks and release wiring for any new adapter.
- Integration testing with the Reqfleet control plane (Shibuya) is required before rollout and will be performed by the Reqfleet team. Coordinate this testing with them; adapter container conformance does not replace it.
- After merge, release-please can open or update a component release PR. Selection is path-based: shared SDK changes need compatibility metadata under each affected adapter, not just `go.mod`. Documentation-only changes need not produce a release.
- Merging the release PR creates the component tag/release; release-gated tests run against that exact SHA before image publication. Bot release PRs do not automatically get PR CI with the current `GITHUB_TOKEN`; manually pushing a tag does not publish.
- Check publication success and the immutable digest in the workflow summary. Publication is not deployment: an operator must approve the image and deploy compatible Shibuya admission/UI/chart changes, then run a deployment smoke.

See [release and compatibility details](AGENTS.md#releases-and-compatibility) for component setup and rollout requirements.

## Releases

Adapter images are independently versioned at `ghcr.io/reqfleet/engine-adapters/k6`. See [releases](https://github.com/reqfleet/engine-adapters/releases). Deploy an approved immutable digest with a compatible Shibuya release.

Releasing a new engine kind also requires changes to the Reqfleet control plane (Shibuya), with help from the Reqfleet team. Coordinate admission, UI and deployment configuration changes with them before rollout; publishing the adapter image alone does not make the engine available.

## Licensing

Adapter code is Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). Container images also include k6, licensed under AGPLv3, with its license and corresponding source.
