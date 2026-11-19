# Changelog

## [0.3.0](https://github.com/reqfleet/engine-adapters/compare/k6/v0.2.0...k6/v0.3.0) (2026-11-19)


### ⚠ BREAKING CHANGES

* **k6:** Request labels now allow at most 255 UTF-8 bytes, down from 1023. Use bounded request/group names for longer URLs; oversized metric samples are dropped. Co-Authored-By: GPT-6 Astra
* **k6:** Remove metrics.New; initialize exporters with &metrics.Exporter{}.

### deps

* **k6:** Adopt SDK-owned run metrics ([#43](https://github.com/reqfleet/engine-adapters/issues/43)) ([4b7f8c5](https://github.com/reqfleet/engine-adapters/commit/4b7f8c55914ecd1bd2f4284845b86af0cae91800))
* **k6:** Upgrade engine SDK to v0.6.0 ([#47](https://github.com/reqfleet/engine-adapters/issues/47)) ([f570e3b](https://github.com/reqfleet/engine-adapters/commit/f570e3b7787c8a24b2c06000760f64b8da48a9b2))


### Features

* **k6:** Add opt-in LLM timing histograms ([#37](https://github.com/reqfleet/engine-adapters/issues/37)) ([1d4129c](https://github.com/reqfleet/engine-adapters/commit/1d4129c5be1d8d99e2c2c92c8a1607c4750aebb6)), closes [#29](https://github.com/reqfleet/engine-adapters/issues/29)
* **k6:** Add token-stream TPS and inter-token latency reporting ([#38](https://github.com/reqfleet/engine-adapters/issues/38)) ([79f573a](https://github.com/reqfleet/engine-adapters/commit/79f573a4f619a35c224adc04d38be976dfef34c7))
* **k6:** Support request-attributed byte counters ([#36](https://github.com/reqfleet/engine-adapters/issues/36)) ([b81e04e](https://github.com/reqfleet/engine-adapters/commit/b81e04e320f96a12bca6181f547646911bda3aa0))


### Bug Fixes

* **k6:** Bundle matching dashboard source and license ([#42](https://github.com/reqfleet/engine-adapters/issues/42)) ([9525688](https://github.com/reqfleet/engine-adapters/commit/952568813e26f48dccb1f57f9101b6048906651f))
* **k6:** Disable automatic binary provisioning ([#22](https://github.com/reqfleet/engine-adapters/issues/22)) ([a2e11a9](https://github.com/reqfleet/engine-adapters/commit/a2e11a93f8b8cdb2c46f66cb810a403da94e1660)), closes [#18](https://github.com/reqfleet/engine-adapters/issues/18)
* **k6:** move Makefile into adapter directory ([#10](https://github.com/reqfleet/engine-adapters/issues/10)) ([8b7fafa](https://github.com/reqfleet/engine-adapters/commit/8b7fafadb8ae852b5255b873bc846948bacd9d07))
* **k6:** propagate inspection diagnostics on startup ([#14](https://github.com/reqfleet/engine-adapters/issues/14)) ([7877961](https://github.com/reqfleet/engine-adapters/commit/7877961c0e771d108641107de931dbc34ff5ca39))
* **k6:** Reject oversized labels with SDK validation ([#24](https://github.com/reqfleet/engine-adapters/issues/24)) ([66f7331](https://github.com/reqfleet/engine-adapters/commit/66f73315dfaf9231ac90eca55156d84d9246f240))
* **k6:** report readable transport failure metrics ([#9](https://github.com/reqfleet/engine-adapters/issues/9)) ([22fb6d1](https://github.com/reqfleet/engine-adapters/commit/22fb6d1a97a90eb42ff511c6a49a3674be9dfa05))
* **k6:** Retain UTF-8 prefixes for oversized request labels ([#21](https://github.com/reqfleet/engine-adapters/issues/21)) ([ef20017](https://github.com/reqfleet/engine-adapters/commit/ef200178efcfffa2cc4210a8c2ecfcc68d0e82d6))
* **k6:** Use SDK request-name budget admission ([#27](https://github.com/reqfleet/engine-adapters/issues/27)) ([b21f062](https://github.com/reqfleet/engine-adapters/commit/b21f06254badb0cf1a59d372764deaad0cfd4293)), closes [#23](https://github.com/reqfleet/engine-adapters/issues/23)
* **k6:** use SDK v0.5.0 container resource stats ([c4e8434](https://github.com/reqfleet/engine-adapters/commit/c4e84341129f738b1074b289d0dd3d766756bf7f))

## [0.2.0](https://github.com/reqfleet/engine-adapters/compare/k6/v0.1.0...k6/v0.2.0) (2026-11-06)


### Features

* **k6:** Group metrics by URL and adopt SDK handlers ([9345a38](https://github.com/reqfleet/engine-adapters/commit/9345a387442889c06904bea64fb3c56875899933))
* **k6:** make engine version configurable at build time ([f978e74](https://github.com/reqfleet/engine-adapters/commit/f978e74cdb6c4d3d53cf31ca3e075d5f7d96d00b))
* **k6:** make SDK version configurable at build time ([47a98f8](https://github.com/reqfleet/engine-adapters/commit/47a98f8c743d3c703fccd5ceb4377eb3017fb369))
* **k6:** Update releases, URL metrics, and SDK integration ([9efe03f](https://github.com/reqfleet/engine-adapters/commit/9efe03f4b412bea37da99cb093f429c2daca2009))


### Bug Fixes

* **deps:** upgrade engine-sdk to v0.4.0 ([693d2a1](https://github.com/reqfleet/engine-adapters/commit/693d2a13e666c219c22d76c51ab52f16e903c397))
