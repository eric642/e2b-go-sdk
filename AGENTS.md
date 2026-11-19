# AGENTS.md

Instructions for AI coding agents working in `github.com/eric642/e2b-go-sdk`.

## Repository Overview

`e2b-go-sdk` is an unofficial, community-maintained Go client SDK (Go `1.26+`) for the E2B sandbox platform, staying wire-compatible with the official JavaScript and Python SDKs and the E2B backend.

### Package & Directory Map

- **`.` (`package e2b`)**: Core public SDK — unified `Client` (`NewClient`, sharing a single `*http.Client` and control-plane REST client; preferred over deprecated package-level `Create`/`Connect`/`Kill` wrappers), `Sandbox` lifecycle, `Commands`, `Pty`, `Filesystem`, `Git`, `Snapshots`, `List` / `ListAll` cursor pagination, `Config`, `Error` hierarchy, and `EnvdVersion` feature gating (`versions.go`).
- **`template/` (`package template`)**: Fluent template `Builder`, `ReadyCmd` helpers, and build orchestration (`/v3/templates` via `Build`/`BuildStream`/`BuildInBackground`, plus legacy `BuildV2` counterparts for self-hosted deployments `<= 2.1.x`).
- **`volume/` (`package volume`)**: Persistent volume control-plane operations and JWT-scoped volume content client.
- **`internal/transport/` (hand-written)**: Wires REST (`oapi-codegen`) and Connect-RPC (`connect.WithProtoJSON()`) clients, control-plane vs. `envd` auth headers, and transport error decoding.
- **`internal/{api,envd,envdapi,volumeapi}/` & `internal/version/upstream.go` (generated)**: Generated REST/Connect-RPC clients and pinned upstream version metadata. **Never edit by hand.**
- **`spec/`**: Vendored OpenAPI (`openapi.yml`, `openapi-volumecontent.yml`, `envd/envd.yaml`), Protobuf (`envd/process/process.proto`, `envd/filesystem/filesystem.proto`), `oapi-codegen-*.yaml` configs, and `E2B_VERSION`.
- **`examples/`**: Runnable end-to-end examples (`basic`, `terminal`, `template`, `lifecycle`, `lifecycle_v2`, `selfhosted`, `desktop`).
- **`E2B/` & `runtime/` (git submodules)**: Read-only upstream reference repositories (see below).

## Upstream Submodules & Spec Synchronization

Both `E2B/` and `runtime/` are **strictly read-only reference submodules**:

- **Never modify, format, or create files inside `E2B/` or `runtime/`.**
- **Never advance or detach submodule commits** unless running an explicit spec-sync task or instructed by the user.

### Roles of Each Submodule

1. **`E2B/` (`e2b-dev/E2B`)**
   - Reference implementation for SDK ergonomics, defaults, error semantics, and version gates (`E2B/packages/js-sdk`, `E2B/packages/python-sdk`).
   - Default spec source for `scripts/sync-spec.sh` (`E2B/spec/` -> `./spec/`).
2. **`runtime/` (`e2b-dev/runtime`)**
   - Authoritative Go backend and in-sandbox daemon source (`runtime/packages/api`, `runtime/packages/envd`, `runtime/packages/client-proxy`, `runtime/packages/orchestrator`, `runtime/packages/shared`). Consult it to verify real server/`envd` handlers, status codes, headers, streaming behavior, and self-hosted compatibility.
   - Secondary/upstream spec source (`runtime/spec/openapi*.yml` for control-plane/edge OpenAPI and `runtime/packages/envd/spec/` for `envd` OpenAPI and Connect-RPC `.proto` definitions) when comparing against or syncing newer backend specs not yet reflected in `E2B/spec/`.

### Regenerating Clients

```sh
make tools                           # one-time install: buf, oapi-codegen, protoc-gen-go, protoc-gen-connect-go
make sync-spec E2B_TAG=e2b@X.Y.Z     # sync ./spec/, VERSION, spec/E2B_VERSION, internal/version/upstream.go
make codegen                         # buf generate + oapi-codegen into internal/
make regen                           # sync-spec + codegen in one step
make version                         # print current SDK version and pinned upstream spec ref
```

If generated code needs to change, update `./spec/`, `buf.gen.yaml`, `spec/oapi-codegen-*.yaml`, or `scripts/sync-spec.sh` and re-run `make codegen` — never patch generated files under `internal/` directly.

## Wire Protocol, Auth & Versioning Invariants

Preserve these invariants whenever modifying SDK or `internal/transport` code:

- **Control-plane vs. `envd` auth isolation**:
  - Control plane (`https://api.<domain>`): authenticates with `X-API-Key` (`APIKey` wins if both are set) or `Authorization: Bearer <AccessToken>`.
  - In-sandbox `envd` (`https://49983-<sandboxID>.<domain>`): authenticates with `X-Access-Token` plus optional `Authorization: Basic <base64(user+":")>`. `ExtraSandboxHeaders` must **never** inject or override `Authorization` when `User` is empty (doing so breaks server-side default-user inference on `envd >= 0.4.0` and risks leaking control-plane tokens to `envd`).
- **Connect-RPC codec**: `NewEnvdClients` must keep `connect.WithProtoJSON()` enabled for `Process` and `Filesystem` clients to match upstream JS/Python wire behavior.
- **Control-plane API versions**:
  - Sandbox listing uses `GET /v2/sandboxes` with cursor pagination via the `x-next-token` response header.
  - Template builds use v3 (`/v3/templates`) by default; preserve v2 (`*V2`) methods for legacy self-hosted E2B deployments (`<= 2.1.x`).
  - `Connect` resumes paused sandboxes (there is no separate `Resume` endpoint).
- **`envd` semver gating (`versions.go` against `Sandbox.EnvdVersion`, or `99.99.99` when `Config.Debug` is true)**:
  - `>= 0.1.4`: recursive `Filesystem.Watch` (`WatchDir` streaming RPC; polling watcher RPCs `CreateWatcher`/`GetWatcherEvents`/`RemoveWatcher` are intentionally unexposed).
  - `>= 0.4.0`: omit default username and `Authorization` header unless caller explicitly provides a user; `< 0.4.0` falls back to `DefaultUser`.
  - `>= 0.5.2`: `Commands.CloseStdin` supported (fail fast with a clear error on older builds).
  - `>= 0.5.7`: `Filesystem` uploads use `application/octet-stream`; `< 0.5.7` falls back to streaming `multipart/form-data`.
- **Error hierarchy & config safety**:
  - Public errors must implement `e2b.Error` (`errors.go`, `error_mapping.go`). Map missing files to `*FileNotFoundError` across both Connect-RPC and plain HTTP `/files`, and missing sandboxes to `*SandboxNotFoundError`.
  - Deep-copy caller-provided maps (`Headers`, `ExtraSandboxHeaders`) when storing or returning `Config` to prevent aliasing races.

## Build, Lint, Formatting & Testing

### Standard Verification Gates

Run these after any Go code change:

```sh
go vet ./...
go test -race ./...
golangci-lint run --timeout=5m
```

- **Linter toolchain note**: `go.mod` targets Go `1.26.x` and `.golangci.yml` uses the v2 schema (`version: "2"`). If `golangci-lint` fails with a Go version mismatch, build it with the matching Go toolchain (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).
- **`internal/transport/` lint blind spot**: `.golangci.yml` excludes `internal` as a whole (along with `E2B` and `runtime`). When editing hand-written code in `internal/transport/`, manually verify `errcheck`, `govet`, `ineffassign`, `staticcheck`, and `unused` cleanliness alongside `go test -race ./internal/transport/...`.
- **Formatting pitfall (`gofmt`)**: Do **not** run `make fmt` (`gofmt -s -w .`) when `E2B/` or `runtime/` submodules are populated on disk — `gofmt .` will recurse into submodule Go files and dirty them. Format only SDK paths instead:
  ```sh
  gofmt -s -w *.go template volume internal/transport examples
  ```

### Integration Tests & Environment Files (`.env` vs. `.env.prod`)

- **Unit tests** (`go test -race ./...`) mock HTTP/Connect-RPC locally via `httptest.NewServer` and must never depend on external network access or `E2B_API_KEY`.
- **Integration tests (`//go:build integration`)**: When modifying wire protocol, transport, auth, or API endpoint behavior, you **may** load `.env` (dev/test credentials) to run relevant integration tests:
  ```sh
  set -a && source .env && set +a && go test -tags=integration -timeout 15m -run TestName ./...
  ```
  Always ensure live sandboxes, snapshots, templates, and volumes created in tests register cleanup (`t.Cleanup` / `defer Kill`), and **never** print or log secret values (`E2B_API_KEY`, `E2B_ACCESS_TOKEN`).
- **Production environment off-limits**: **Never** read, source, modify, or execute commands/tests against `.env.prod` unless the user explicitly instructs you to in the current turn.

## Git & Release Rules

- **No unrequested Git mutations**: Do **not** run `git commit`, `git push`, `git tag`, or destructive git operations (`git reset --hard`, `git checkout --`, force-push) unless explicitly asked by the user.
- **Release tag hazard**: Pushing any `v*` tag immediately triggers `.github/workflows/release.yml` to publish a GitHub Release.
- **Commit style (when asked to commit)**:
  - Imperative English subject line under 72 characters; body explains *why* rather than *what*.
  - Keep `internal/` codegen updates in a separate commit from hand-written SDK changes whenever practical.
  - Verify `git status` before committing so `.env`, `.env.prod`, compiled example binaries, and accidental submodule modifications are never staged.

## Code & PR Review Guidelines

When asked to review code or pull requests in this repository:

- Focus on concrete bugs, regressions, and behavioral risks:
  - Concurrency & resource lifecycles: data races, goroutine leaks, unclosed `http.Response.Body` or streaming connections (`CommandHandle`, `WatchHandle`, PTY streams), and missing `context.Context` cancellation handling.
  - Protocol & auth correctness: control-plane vs. `envd` header isolation, `ProtoJSON` compatibility, cursor pagination (`x-next-token`), and v2/v3 endpoint behavior.
  - Backwards compatibility: `EnvdVersion` feature gates in `versions.go`, error-type mapping (`*FileNotFoundError` vs. `*SandboxNotFoundError` vs. `*BuildError`), and parity with `E2B/` SDKs and `runtime/` server handlers.
- Skip pure style, formatting, or naming nits already enforced by `gofmt` and `golangci-lint` (except real lint/error-handling issues inside `internal/transport/`, which `.golangci.yml` excludes).
