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

## Releases

Adapter images are independently versioned at `ghcr.io/reqfleet/engine-adapters/k6`. See [releases](https://github.com/reqfleet/engine-adapters/releases). Deploy an approved immutable digest with a compatible Shibuya release.

## Licensing

Adapter code is Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). Container images also include k6, licensed under AGPLv3, with its license and corresponding source.
