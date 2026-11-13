# Changelog

## [0.2.1](https://github.com/reqfleet/engine-adapters/compare/k6/v0.2.0...k6/v0.2.1) (2026-11-13)


### Bug Fixes

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
