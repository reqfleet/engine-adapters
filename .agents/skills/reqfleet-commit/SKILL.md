---
name: reqfleet-commit
description: ALWAYS use this skill when committing code changes — never commit directly without it. Creates Conventional Commit messages for Reqfleet repositories so release-please can derive releases and changelogs. Trigger on any commit, git commit, save changes, or commit message task.
---

# Reqfleet Commit Skill

Follow these conventions when creating commits for Reqfleet repositories.

## Prerequisites

Read the target repository's agent instructions and relevant development skills.
Run its required checks before committing, using its prescribed execution environment.
Do not assume a language, directory layout, test command, or VM.

Before committing, always check the current branch:

```bash
git branch --show-current
```

**If you're on `main` or `master`, you MUST create a feature branch first** — unless the user explicitly asked to commit to main. Do not ask for confirmation; default to creating the branch.

```bash
# Create and switch to a new branch
git checkout -b <type>-<short-description>
```

Branch naming should follow the pattern `<type>-<short-description>`, where the type matches the commit type (for example, `feat-add-user-auth`, `fix-null-response`, or `refactor-extract-validation`).

## Pull Requests

- Creating a commit does not authorize opening a PR. Create a PR only when requested.
- Open PRs as ready for review (non-draft). Do not create draft PRs or pass `--draft` to `gh pr create`.

## Conventional Commit Format

Reqfleet commit messages MUST follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```text
<type>[optional scope][!]: <description>

[optional body]

[optional footer(s)]
```

The header is required. The scope, body, and footers are optional. Use `!` or a `BREAKING CHANGE:` footer for a breaking change. Keep every line under 100 characters.

## Release Automation

Reqfleet uses [release-please](https://github.com/googleapis/release-please) for releases. Release-please reads Conventional Commit messages on the default branch, opens or updates a release pull request, and derives the changelog and next semantic version. Merging the release pull request creates the version tag and GitHub release.

Release-please evaluates Conventional Commits that become reachable from the default branch. Apply the rule for the repository's merge method:

- **Squash merge:** The final squash commit MUST follow this format. Feature-branch commit types are not retained.
- **Rebase merge:** Every rebased commit MUST follow this format because each commit lands on the default branch.
- **Merge commit:** Every landed branch commit MUST follow this format. Keep the synthetic merge commit as merge metadata; do not repeat the change as another Conventional Commit.

Commit types have these default release effects:

| Commit | Changelog section | Semantic version effect |
|--------|-------------------|-------------------------|
| `fix` | Bug Fixes | Patch release |
| `deps` | Dependencies | Patch release |
| `perf` | Performance Improvements | Patch release |
| `revert` | Reverts | Patch release |
| `feat` | Features | Minor release |
| Any type with `!` or a `BREAKING CHANGE:` footer | Breaking Changes | Major release |
| `docs`, `style`, `chore`, `refactor`, `test`, `build`, `ci` | Hidden unless breaking | No release by themselves |

Choose the type from the actual change; never mislabel a commit only to force a version bump. Do not manually update release versions, tags, or changelogs for a normal release; leave those changes to the release pull request.

Use `deps` for production or runtime dependency updates that require a new artifact to be
published or deployed. Use `build` for build-system, toolchain, or dependency changes that do
not affect the shipped runtime artifact and should not trigger a release. Classify the change by
its release impact, not merely by which manifest or lockfile changed.

## Commit Types

| Type | Purpose |
|------|---------|
| `feat` | New feature |
| `fix` | Bug fix |
| `deps` | Production or runtime dependency update that requires a release |
| `perf` | Performance improvement |
| `refactor` | Refactoring with no feature or bug fix |
| `docs` | Documentation only |
| `test` | Test additions or corrections |
| `build` | Build system, toolchain, or non-releasable dependency change |
| `ci` | CI configuration |
| `chore` | Maintenance not covered by another type |
| `style` | Formatting with no logic change |
| `revert` | Revert a previous commit |

Use `refactor`, not the nonstandard abbreviation `ref`. Scopes such as `api`, `gui`, `replay`, or `billing` identify the affected area; they do not change release behavior.

## Subject Line Rules

- Use the exact form `<type>[optional scope][!]: <description>`
- Use a lowercase type and an optional noun scope describing the affected area
- Use imperative, present tense: "Add feature" rather than "Added feature"
- Capitalize the first letter of the description
- Do not end the description with a period
- Keep the complete header under 70 characters

## Body Guidelines

- Explain **what** and **why**, not how
- Use imperative mood and present tense
- Include motivation for the change
- Contrast with previous behavior when relevant

## Footers

Separate footers from the body with one blank line. Use Conventional Commit trailer syntax:

```text
Fixes #1234
Refs: GH-1234
Refs: LINEAR-ABC-123
```

- `Fixes #1234` closes the GitHub issue when merged
- `Refs` links related work without closing it
- Footer tokens use hyphens instead of spaces, except for the exact token `BREAKING CHANGE`

For a breaking change, add `!` immediately before the colon, a `BREAKING CHANGE:` footer, or both:

```text
BREAKING CHANGE: Describe the incompatible behavior and migration path
```

## AI-Generated Changes

When changes were primarily generated by a coding agent, include the `Co-Authored-By` attribution in the commit footer and replace the placeholder with the active model's own name:

```text
Co-Authored-By: <model name>
```

This is the only indicator of AI involvement that should appear in commits. Do not add phrases such as "Generated by AI", "Written with Claude", or similar markers elsewhere in the commit message.

## Examples

### Simple fix

```text
fix(api): Handle null response in user endpoint

The user API could return null for deleted accounts, causing a crash
in the dashboard. Add null check before accessing user properties.

Fixes #5678
Co-Authored-By: <model name>
```

### Feature with scope

```text
feat(alerts): Add Slack thread replies for alert updates

When an alert is updated or resolved, post a reply to the original
Slack thread instead of creating a new message. This keeps related
notifications grouped together.

Refs: GH-1234
```

### Releasable dependency update

```text
deps(api): Upgrade OpenTelemetry SDK to 1.38.0

Publish the runtime with the updated SDK to include its production fixes.
```

### Refactor

```text
refactor: Extract common validation logic

Move duplicate validation code from three endpoints into a shared validator.
Preserve existing behavior.
```

### Breaking change

```text
feat(api)!: Remove deprecated v1 endpoints

Remove all v1 API endpoints that were deprecated in version 23.1.
Clients should migrate to v2 endpoints.

BREAKING CHANGE: v1 endpoints no longer available
Fixes #9999
```

## Revert Format

```text
revert: Restore the previous authentication flow

Revert the replacement because deployed clients still use the old endpoints.

Refs: abc123def456
```

## Principles

- Each commit should be a single, stable change
- Commits should be independently reviewable
- The repository should be in a working state after each commit
- The final commit on the default branch should produce accurate release notes

## References

- [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
- [Release Please](https://github.com/googleapis/release-please)
