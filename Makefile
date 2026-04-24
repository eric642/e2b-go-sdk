GOBIN ?= $(shell go env GOPATH)/bin
PATH  := $(GOBIN):$(PATH)

# Upstream E2B tag to track. Override on the CLI, e.g.:
#   make sync-spec E2B_TAG=python-sdk@2.20.0
# Default: the script picks the newest python-sdk@* tag.
E2B_TAG ?=

.PHONY: tools sync-spec codegen codegen-proto codegen-api regen \
        vet test test-integration fmt clean version

tools:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

# Pull specs from the pinned upstream tag into ./spec/ and refresh
# internal/version/upstream.go + VERSION.
sync-spec:
	bash scripts/sync-spec.sh $(E2B_TAG)

# codegen assumes spec/ is already up to date. Use `make regen` to sync
# specs and regenerate in one step.
codegen: codegen-proto codegen-api

codegen-proto:
	$(GOBIN)/buf generate

codegen-api:
	$(GOBIN)/oapi-codegen -config spec/oapi-codegen-api.yaml    spec/openapi.yml
	$(GOBIN)/oapi-codegen -config spec/oapi-codegen-envd.yaml   spec/envd/envd.yaml
	$(GOBIN)/oapi-codegen -config spec/oapi-codegen-volume.yaml spec/openapi-volumecontent.yml

# One-shot: sync upstream spec to the target tag, then regenerate everything.
regen: sync-spec codegen

version:
	@printf 'sdk:     %s\n' "$$(cat VERSION 2>/dev/null || echo unknown)"
	@printf 'spec:    %s\n' "$$(awk -F= '/^tag=/    {print $$2}' spec/E2B_VERSION 2>/dev/null || echo unknown)"
	@printf 'commit:  %s\n' "$$(awk -F= '/^commit=/ {print $$2}' spec/E2B_VERSION 2>/dev/null || echo unknown)"

vet:
	go vet ./...

test:
	go test -race ./...

# Integration tests talk to a real E2B backend. Tests skip themselves
# automatically when E2B_API_KEY is unset, but this target fails fast so
# callers notice the missing key instead of silently running a pure-unit
# suite.
test-integration:
	@test -n "$$E2B_API_KEY" || (echo "E2B_API_KEY must be set to run integration tests" && exit 1)
	go test -run Integration -timeout 10m ./...

fmt:
	gofmt -s -w .

clean:
	rm -rf internal/api internal/envd internal/envdapi internal/volumeapi
