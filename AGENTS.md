# Repository Instructions

## Release management

- Use release-please to manage releases and generate changelog updates.
- Do not manually create or edit `CHANGELOG.md`; leave changelog changes to release-please.

## Request labels are a reporting contract

For the k6 adapter, group request metrics by the exact CSV `url` value:

- Use the URL as `label` for latency histograms and status counters. Repeated URLs accumulate together; distinct URLs remain separate within the label budget.
- Do not collapse request metrics under an engine-wide `k6` label. Missing URL attribution for received bytes is not a reason to discard URL attribution from latency or status metrics.
- Do not substitute the CSV `name` column, normalize paths, or remove query strings. Preserve the URL tag as k6 emits it. k6 can rewrite that tag when a script uses custom name/grouping options; do not invent a discarded original URL.
- Apply the same label admission decision to latency and status. Admit the first `label_limit` distinct URLs per run, keep admitted labels stable, and fold additional URLs into `others`. A zero limit folds all request URLs into `others`.
- Bound admission state by the label limit and clear it on every run reset, together with metric collectors. Keep run, plan, collection, agent and zone identity in their own schema labels.

## Preserve metric attribution boundaries

k6 CSV rows are independent metric samples, not complete request records:

- An `http_req_duration` sample supplies latency, HTTP status and URL together. Use it for the URL-grouped latency observation and status count; do not also count `http_reqs` and double-count requests.
- Standard `data_received` samples lack request URL attribution. Keep received bytes aggregate under `label="k6"` (`others` when the label limit is zero). They must not consume URL admission slots or produce per-URL byte series.
- Per-URL bandwidth is unavailable, not zero. Never assign bytes to requests by row order, matching timestamps, or copying totals to every URL.
- VUs, CPU and memory remain agent-level metrics, not request-labelled measurements.
- Do not route independent VU/byte samples through an SDK conversion that unconditionally creates a request count and latency observation. Preserve the semantics when changing ingestion APIs.

## Use SDK-owned configuration and HTTP serving

- Resolve deployment options once with `agent.LoadAgentServerOptions` and use `AgentServerOptions.LabelsUsageLimit` for the exporter. Do not add adapter-local environment parsing.
- Preserve resolved options when adding adapter callbacks. Start with `agent.NewAgentServer`, not `MakeAgentServer`, which would resolve environment values again.
- Pass the exporter as `AgentServerOptions.MetricsHandler` with `NativeMetricsSchema: shibuya-v1`. Let the SDK own the listener and bearer authentication; do not add a loopback metrics server or configure `MetricsURL` alongside the handler.
- Keep CSV parsing, metric normalization, URL admission and run reset in the exporter. SDK HTTP serving does not change the attribution boundaries above.

## Preserve and verify the contract

- Retain both `url` and `status` in k6 `systemTags`, including when a plan supplies an explicit tag list; preserve the other requested tags.
- Parse CSV with `encoding/csv`, locate required columns by name, and reject missing URL columns or empty URLs on HTTP duration samples. Non-request samples may have empty URL fields.
- Regression coverage must distinguish multiple URLs, repeated requests to one URL, query-string variants, and URL versus name columns. Cover overflow, zero budget, reset, independent status counts, and aggregate-only bytes.
- For metrics behavior changes, run `make test-unit` and container-only `make test-conformance`, plus a live smoke when adding behavior not exercised by conformance. Never run the integration binary directly on the host; it owns fixed ports and `/test-data`.
- Keep [the metrics documentation](k6/README.md#url-grouped-request-metrics), exporter tests, and live conformance assertions aligned with these semantics.
