IMAGE ?= k6-adapter:local
CONFORMANCE_IMAGE ?= k6-adapter:conformance

.PHONY: test test-unit test-conformance build image

test: test-unit test-conformance

test-unit:
	GOWORK=off go test -race -count=1 ./... github.com/reqfleet/engine-sdk/conformance

build:
	GOWORK=off go build ./cmd/agent

image:
	docker build -f Dockerfile --target runtime -t $(IMAGE) ..

test-conformance:
	docker build -f Dockerfile --target conformance -t $(CONFORMANCE_IMAGE) ..
	docker run --rm --network none $(CONFORMANCE_IMAGE)
