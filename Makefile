IMAGE ?= k6-adapter:local
CONFORMANCE_IMAGE ?= k6-adapter:conformance

.PHONY: test test-unit test-conformance build image

test: test-unit test-conformance

test-unit:
	GOWORK=off go test -race -count=1 ./...

build:
	GOWORK=off go build ./k6/cmd/agent

image:
	docker build -f k6/Dockerfile --target runtime -t $(IMAGE) .

test-conformance:
	docker build -f k6/Dockerfile --target conformance -t $(CONFORMANCE_IMAGE) .
	docker run --rm --network none $(CONFORMANCE_IMAGE)
