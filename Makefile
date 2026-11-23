GOBIN := $(strip $(GOBIN))
ifeq ($(GOBIN),)
GOBIN := $(strip $(shell go env GOBIN))
endif
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
PATH  := $(GOBIN):$(PATH)

# Pinned codegen tool versions (override on CLI when upgrading).
BUF_VERSION                   ?= v1.75.0
OAPI_CODEGEN_VERSION          ?= v2.8.2
PROTOC_GEN_GO_VERSION         ?= v1.36.12
PROTOC_GEN_CONNECT_GO_VERSION ?= v1.21.1

# Upstream E2B tag to track. Override on the CLI, e.g.:
#   make sync-spec E2B_TAG=e2b@2.19.0
# Default: the script picks the newest e2b@* tag.
E2B_TAG ?=

.PHONY: tools sync-spec codegen codegen-proto codegen-api regen \
        vet test test-integration fmt clean version

tools:
	GOBIN="$(GOBIN)" go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOBIN="$(GOBIN)" go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	GOBIN="$(GOBIN)" go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN="$(GOBIN)" go install connectrpc.com/connect/cmd/protoc-gen-connect-go@$(PROTOC_GEN_CONNECT_GO_VERSION)

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
	go vet -mod=readonly ./...

test:
	go test -mod=readonly -race -shuffle=on -count=1 ./...

# Integration tests talk to a real E2B backend. Tests skip themselves
# automatically when E2B_API_KEY is unset, but this target fails fast so
# callers notice the missing key instead of silently running a pure-unit
# suite.
test-integration:
	@test -n "$$E2B_API_KEY" || (echo "E2B_API_KEY must be set to run integration tests" && exit 1)
	go test -mod=readonly -tags=integration -count=1 -timeout 15m ./...

fmt:
	go fmt -mod=readonly ./...

clean:
	rm -rf internal/api internal/envd internal/envdapi internal/volumeapi
