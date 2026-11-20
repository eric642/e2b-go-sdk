# AGENTS.md

Unofficial Go `1.26+` SDK (`github.com/eric642/e2b-go-sdk`), wire-compatible with E2B JS/Python SDKs on Cloud & self-hosted.

## Baselines & Package Map

- **Baselines**: `e2b@2.64.0` (`spec/E2B_VERSION`, `VERSION`), `runtime <= 2026.30`, `envd-v0.6.13`.
- **`.` (`e2b`)**: `Client` (control-plane retries vs. stream data plane), `Sandbox`, `Config`, `Error`, `versions.go`.
- **`template/`**: `Builder`, `ReadyCmd`, `/v3` & `/v2` builds, listing, visibility, tags.
- **`volume/`**: Volume control plane, BYOC routing, JWT `belt` content API.
- **`internal/transport/`**: REST/Connect-RPC wiring, retries, header isolation, error decoding.
- **`internal/{api,envd,envdapi,volumeapi,version}/`**: Generated code (**never hand-edit**).
- **`spec/` & `scripts/`**: Filtered specs, codegen configs, `sync-spec.sh`, `filter_spec.go`.
- **`examples/`**: Runnable Cloud & self-hosted examples.
- **`E2B/` & `runtime/`**: Read-only upstream submodules.

## Submodules & Codegen

- **Read-only submodules**: Never modify, format, or checkout `E2B/` or `runtime/` unless asked.
  - `E2B/`: JS/Python SDK reference & spec source (`E2B/spec/` -> `./spec/`).
  - `runtime/`: Go server/`envd` source & specs (`packages/{api,envd,client-proxy,orchestrator,shared}`).
- **Codegen** (never hand-edit `internal/{api,envd,envdapi,volumeapi,version}/`; edit `./spec/` or `scripts/filter_spec.go`):
  ```sh
  make sync-spec E2B_TAG=e2b@2.64.0  # sync & filter ./spec/
  make codegen                       # regenerate internal/
  make regen                         # sync-spec + codegen
  ```

## Wire, Auth & Versioning Invariants

- **Auth & headers**:
  - Control plane: `X-API-Key` (wins over `Bearer`; never both) or `Bearer`, plus `Config.Headers`.
  - `envd`: `X-Access-Token`, `E2b-Sandbox-Id`, `E2b-Sandbox-Port: 49983`, `Keepalive-Ping-Interval: 50` (streams), `ExtraSandboxHeaders` **only** (never `Config.Headers`), optional `Basic` auth (never inject `Authorization` when `User == ""`).
  - Volume content: `Bearer <volumeJWT>` only.
- **Connect-RPC**: Keep `connect.WithProtoJSON()` on `envd` clients.
- **Coexistence vs. adaptation**:
  - 1-to-1 control plane (`Create`/`CreateV1`, `Connect`/`ConnectV1`/`Resume`, `List`/`ListV1`, `GetLogs`/`GetLogsV1`, `Build*`/`Build*V2`): **zero hidden fallback** (see `README.md` matrix).
  - Composite & `envd` data plane: `IsRunning`, empty `state`, MCP, S3/Azure uploads, template `401`/`logs`, BYOC `domain`, and `EnvdVersion` SemVer gates (`versions.go` is source of truth; `99.99.99` in `Debug`) adapt internally.
- **GoDoc rules**:
  - Unversioned `E2B Cloud` (no `e2b@...` tags in `Compatibility:`), `runtime <= 2026.30` / `<calver>`, `v0.x.y` (`envd`).
  - Omit `Endpoint:`/`Compatibility:`/`Envd:` on local constructors/builders/getters (no `N/A`); omit `Envd:` when unused.
  - Distinguish soft-deprecated active OpenAPI routes (e.g. `Resume`, `CreateV1` — explain *why*, where active, *when* to choose; no bare `(deprecated)`) from retired `410 Gone` routes (`Build*V2` on Cloud, active on `runtime <= 2026.30`).
- **Errors & `Config`**:
  - Public errors in `.`, `template/`, `volume/` implement `e2b.Error` (`*FileNotFoundError`, `*SandboxNotFoundError`).
  - Clone maps and pointer options in `Config`.

## Verification Gates

```sh
go fmt -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -shuffle=on -count=1 ./...
golangci-lint run --timeout=5m
```

- **Never run `make fmt`** — `gofmt .` dirties `E2B/` & `runtime/`; use `go fmt -mod=readonly ./...`.
- Run `gopls check -severity=hint <files.go>` when available.
- `.golangci.yml` excludes `internal/` — check `errcheck`/`govet`/`staticcheck`/`unused` in `internal/transport/`.

## Testing, Git & PR Review

- **Tests & `.env`**:
  - Unit tests stay offline (`httptest.NewServer`).
  - Integration tests (`//go:build integration`) may load `.env` with `t.Cleanup` & **zero secret logging**.
  - **`.env.prod` is strictly off-limits**: Never read, source, modify, or target it unless asked in this turn.
- **Git & releases**:
  - No unrequested `git commit`, `git push`, `git tag`, or destructive git ops.
  - Any `v*` tag triggers `.github/workflows/release.yml`.
  - Commits (when asked): <72-char subject, *why* body, separate `internal/` codegen commits, clean `git status`.
- **PR review**:
  - Check races, goroutine/stream/`Body` leaks, `ctx` cancellation, header isolation, `ProtoJSON`/pagination/zero-fallback.
  - Verify `versions.go` gates, `e2b.Error` parity, and `internal/transport/` lint issues; skip style nits.
