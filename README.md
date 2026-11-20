# E2B Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/eric642/e2b-go-sdk.svg)](https://pkg.go.dev/github.com/eric642/e2b-go-sdk)
[![test](https://github.com/eric642/e2b-go-sdk/actions/workflows/test.yml/badge.svg)](https://github.com/eric642/e2b-go-sdk/actions/workflows/test.yml)
[![integration](https://github.com/eric642/e2b-go-sdk/actions/workflows/integration.yml/badge.svg)](https://github.com/eric642/e2b-go-sdk/actions/workflows/integration.yml)
[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](./LICENSE)

A Go client for the [E2B](https://e2b.dev) sandbox platform, staying wire-compatible
with the official [Python](https://github.com/e2b-dev/E2B/tree/main/packages/python-sdk)
and [JavaScript](https://github.com/e2b-dev/E2B/tree/main/packages/js-sdk) SDKs across
both E2B Cloud and self-hosted E2B deployments.

> **Unofficial, community-maintained port.** This module is not published
> by the E2B team — it tracks the upstream spec but is independent of
> them. Report SDK issues here; report E2B platform issues upstream.

### Pinned upstream baselines

| Component | Baseline | Scope |
| --- | --- | --- |
| [`e2b-dev/E2B`](https://github.com/e2b-dev/E2B) SDK spec | `e2b@2.64.0` | Vendored OpenAPI & Protobuf specs in `./spec/` (`spec/E2B_VERSION`) |
| [`e2b-dev/runtime`](https://github.com/e2b-dev/runtime) | `2026.30` (`runtime <= 2026.30`, tagged `2025.31..2026.30` + pre-`2025.31`) | Self-hosted control-plane, orchestrator, proxy, and `belt` volume content |
| `envd` (in-sandbox daemon) | `envd-v0.6.13` (`v0.1.0..v0.6.13`) | Guest capabilities gated automatically via `Sandbox.EnvdVersion` |

## Install

```sh
go get github.com/eric642/e2b-go-sdk
```

## Quick start (E2B Cloud)

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/eric642/e2b-go-sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := e2b.NewClient(e2b.Config{}) // reads E2B_API_KEY from env
	if err != nil {
		log.Fatal(err)
	}

	sbx, err := client.Create(ctx, e2b.CreateOptions{
		Template: "base",
		Timeout:  5 * time.Minute,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sbx.Kill(ctx)

	handle, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args: []string{"-c", "echo hello"},
	})
	if err != nil {
		log.Fatal(err)
	}
	result, err := handle.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Stdout) // hello
}
```

> **Self-hosted E2B note:** Self-hosted releases (`runtime <= 2026.30`) expose `POST /sandboxes` rather than `POST /v2/sandboxes`. Call `client.CreateV1(ctx, ...)` (and `ConnectV1` or `Resume` / `sbx.Resume`) on self-hosted clusters — see the [Server generation selection table](#server-generation-selection-table) and [`examples/selfhosted`](./examples/selfhosted).

## Unified clients

Hold one `e2b.Client` (and `template.Client` / `volume.Client` when building templates or managing volumes) for the lifetime of your process. Each resolves `e2b.Config` once and shares pooled HTTP transports — applying control-plane retries and timeouts on REST calls while keeping long-lived `envd` streams and large file transfers free of client-wide timeout cutoffs:

```go
c, err := e2b.NewClient(e2b.Config{})
if err != nil {
	log.Fatal(err)
}

sbx, err := c.Create(ctx, e2b.CreateOptions{Template: "base"})
if err != nil {
	log.Fatal(err)
}
defer sbx.Kill(ctx)

// List all running sandboxes (cursor-paginated via x-next-token).
running, err := c.ListAll(ctx, e2b.SandboxListOptions{
	State: []e2b.SandboxState{e2b.SandboxStateRunning},
})
if err != nil {
	log.Fatal(err)
}
for _, s := range running {
	fmt.Println(s.SandboxID, s.State)
}
```

Package-level `e2b.Create` / `CreateV1` / `Connect` / `ConnectV1` / `Resume` / `Kill` / `List` / `ListAll` (and `volume.Create` / `Connect` / `ConnectByID` / `List`) remain supported for one-shot scripts; prefer `NewClient` when issuing multiple calls.

## Configuration & authentication

`e2b.Config{}` resolves missing fields from environment variables:

| Variable | Purpose |
| --- | --- |
| `E2B_API_KEY` | Team API key (`X-API-Key` header; takes precedence if `E2B_ACCESS_TOKEN` is also set) |
| `E2B_ACCESS_TOKEN` | User access token (`Authorization: Bearer`) |
| `E2B_DOMAIN` | Base domain (defaults to `e2b.app`) |
| `E2B_API_URL` | Full override of the control-plane URL (defaults to `https://api.<domain>`) |
| `E2B_SANDBOX_URL` | Override the `envd` base URL (e.g. `http://localhost:3002` for E2B Embed or tunnels) |
| `E2B_VOLUME_API_URL` | Override the volume-content URL (defaults to `https://api.<volumeDomain>`) |
| `E2B_HTTP_VERSION` | Force HTTP protocol version: `"1.1"` (`HTTPVersion1`) or `"2"` (`HTTPVersion2`) |
| `E2B_DEBUG` | `"true"` targets local debug servers (`localhost:3000` API, `localhost:8080` volumes, `localhost:49983` `envd`) |

`e2b.Config` also supports explicit `Proxy` (`http://`, `https://`, `socks5://`, `socks5h://`, or `host:port`), `Retries` (default `3`, honoring `Should-Retry` and `Retry-After`), `Headers` (control-plane only), and `ExtraSandboxHeaders` (`envd` only).

## Packages

| Path | Description |
| --- | --- |
| `github.com/eric642/e2b-go-sdk` | Core `Client`, `Sandbox`, `Commands`, `Filesystem`, `Pty`, `Git`, Snapshots, Fork, Logs, Network, Secrets |
| `github.com/eric642/e2b-go-sdk/template` | Fluent `template.Builder`, `ReadyCmd` helpers, v3 & legacy v2 build pipelines, template & tag management |
| `github.com/eric642/e2b-go-sdk/volume` | `volume.Client` persistent volume lifecycle & streaming volume-content API |

## Server generation selection table

E2B Cloud is a single hosted service, whereas self-hosted E2B clusters span `runtime` releases (`2025.31..2026.30`) as well as older pre-`2025.31` deployments. Use this table to pick the right method for your target environment:

| Area & operation | E2B Cloud | Self-Hosted Modern (`runtime 2025.44..2026.30`) | Self-Hosted Legacy (`runtime < 2025.44`) |
| --- | --- | --- | --- |
| **Sandbox create** | `Client.Create` (`POST /v2/sandboxes`) | `Client.CreateV1` (`POST /sandboxes`, `secure: true` default) | `Client.CreateV1` (`POST /sandboxes`, `secure: true` default) |
| **Sandbox connect / wake** | `Client.Connect` / `Sandbox.Connect` (`POST /v2/sandboxes/{id}/connect`) | `Client.ConnectV1` / `Sandbox.ConnectV1` (`POST /sandboxes/{id}/connect`), or `Client.Resume` / `Sandbox.Resume` | `Client.Resume` / `Sandbox.Resume` / `ResumeV1` (`POST /sandboxes/{id}/resume`) |
| **Sandbox pause / kill** | `Pause` (`POST /sandboxes/{id}/pause`), `Kill` (`DELETE /sandboxes/{id}`) | `Pause`, `Kill` | `Pause`, `Kill` |
| **Sandbox list** | `Client.List` / `ListAll` (`GET /v2/sandboxes`) | `Client.List` / `ListAll` (`GET /v2/sandboxes`; `Order`/`StartedAfter`/`Template` in `runtime 2026.30`) | `Client.List` (`runtime >= 2025.31`) or `Client.ListV1` (`GET /sandboxes` on `runtime < 2025.31`) |
| **Sandbox timeout / keepalive** | `SetTimeout` (`POST /sandboxes/{id}/timeout`) | `SetTimeout` or monotonic `RefreshTimeout` (`POST /sandboxes/{id}/refreshes`) | `SetTimeout` or `RefreshTimeout` (`RefreshTimeoutV1`) |
| **Sandbox logs** | `GetLogs` (`GET /v2/sandboxes/{id}/logs`) | `GetLogs` (`runtime >= 2026.07`) or `GetLogsV1` (`runtime < 2026.07`) | `GetLogsV1` (`GET /sandboxes/{id}/logs`) |
| **Sandbox fork** | `Fork` (`POST /sandboxes/{id}/fork`, `envd >= v0.5.0`) | `Fork` (`runtime >= 2026.29`, `envd >= v0.5.0`) | Not available |
| **Sandbox network update** | `UpdateNetwork` (`PUT /sandboxes/{id}/network`) | `UpdateNetwork` (`runtime >= 2026.12`) | Not available |
| **Snapshots** | `CreateSnapshot`, `ListSnapshots`, `DeleteSnapshot` (`envd >= v0.5.0`) | Supported on `runtime >= 2026.08` (`envd >= v0.5.0`) | Not available |
| **Template build** | `template.Client.Build` / `BuildStream` / `BuildInBackground` (`POST /v3/templates`) | `Build` / `BuildStream` / `BuildInBackground` (`POST /v3/templates`) | `BuildV2` / `BuildStreamV2` / `BuildInBackgroundV2` (`POST /v2/templates`; returns `410 Gone` on E2B Cloud) |
| **Template list** | `ListV2` / `ListAllV2` (`GET /v2/templates`) & `ListDefault` (`GET /default-templates`) | `ListV2` / `ListAllV2` (`runtime >= 2026.26`) or `List` / `ListV1` (`GET /templates`) | `List` / `ListV1` (`GET /templates`) |
| **Template visibility** | `SetPublic` (`PATCH /v2/templates/{id}`) | `SetPublic` (`runtime >= 2026.05`) or `SetPublicV1` (`runtime < 2026.05`) | `SetPublicV1` (`PATCH /templates/{id}`) |
| **Template tags** | `AssignTags`, `RemoveTags`, `GetTags`, `CountTags`, `TagExists`, `ListTagGroups`, `GetTagAssignments` | `AssignTags`, `RemoveTags`, `GetTags` (`runtime >= 2026.01..2026.07`) | Not available |
| **Persistent volumes** | `volume.NewClient` (`Create`, `Connect`, `ConnectWithToken`, `Stat`, `List`, `ReadFileStream`, `WriteFileStream`, …) | Supported on `runtime >= 2026.07` (BYOC `domain` when returned, else `Config.Domain`) | Not available |
| **Secrets** | `CreateSecret`, `UpdateSecret`, `GetSecret`, `ListSecrets`, `DeleteSecret`, `SecretPlaceholder` | Supported on `runtime 2026.30` | Not available |

### Soft-deprecated OpenAPI routes vs. retired (`410 Gone`) routes

Upstream OpenAPI marks older routes `deprecated: true` when a newer endpoint is introduced, which does **not** mean those routes are removed on self-hosted clusters (or even on E2B Cloud):

- **Soft-deprecated in OpenAPI, still actively served (`CreateV1`, `ConnectV1`, `Resume`, `RefreshTimeout`, `ListV1`, `GetLogsV1`, `template.List`/`ListV1`, `template.SetPublicV1`):**
  All of these remain **actively served** on `E2B Cloud` and `self-hosted runtime <= 2026.30` (from each route's introduction release), and several are the **primary or required** endpoints on self-hosted deployments:
  - `CreateV1` (`POST /sandboxes`): primary create endpoint for all `self-hosted runtime <= 2026.30` (`POST /v2/sandboxes` is E2B Cloud-only).
  - `ConnectV1` (`POST /sandboxes/{id}/connect`): primary idempotent connect/wake endpoint for `self-hosted runtime 2025.44..2026.30`.
  - `Resume` / `ResumeV1` (`POST /sandboxes/{id}/resume`): soft-deprecated since `runtime 2025.44` introduced idempotent `/connect` (`ConnectV1` attaches if running or wakes if paused, whereas `/resume` returns `409 Conflict` if already running). Still served on both `E2B Cloud` and all `self-hosted runtime <= 2026.30`; **required** on `runtime < 2025.44` to wake paused sandboxes, and useful anywhere when overriding `CPUCount`/`AutoPause` on wake or strictly asserting the sandbox was paused.
  - `RefreshTimeout` / `RefreshTimeoutV1` (`POST /sandboxes/{id}/refreshes`): monotonic keepalive (`allowShorter=false`), still served on `E2B Cloud` and `self-hosted runtime <= 2026.30`.
  - `ListV1` (`GET /sandboxes`), `GetLogsV1` (`GET /sandboxes/{id}/logs`), `template.List`/`ListV1` (`GET /templates`), `template.SetPublicV1` (`PATCH /templates/{id}`): required on self-hosted runtimes predating their v2 counterparts (`< 2025.31`, `< 2026.07`, `< 2026.26`, `< 2026.05` respectively) and still served on `runtime <= 2026.30` and `E2B Cloud`.
- **Retired (`410 Gone`) on E2B Cloud (`template.BuildV2` / `BuildStreamV2` / `BuildInBackgroundV2` — `POST /v2/templates`):**
  The **only** SDK-exposed route retired on `E2B Cloud` (where it returns `410 Gone`). It remains **actively served on `self-hosted runtime <= 2026.30`** and is **required on `self-hosted runtime < 2025.44`** (where `POST /v3/templates` does not exist).

### Explicit control-plane coexistence vs. adaptive high-level features

The SDK follows a strict two-layer design so multi-generation support stays predictable and debuggable:

1. **1-to-1 Control-Plane Methods — Zero Hidden Fallback:**
   Versioned control-plane endpoints (`Create` vs `CreateV1`, `Connect` vs `ConnectV1` vs `Resume` / `ResumeV1`, `List` vs `ListV1`, `GetLogs` vs `GetLogsV1`, `template.Build` vs `BuildV2`, `template.ListV2` vs `ListV1`, `template.SetPublic` vs `SetPublicV1`) coexist on the same client and **never probe or silently fall back across route versions**. If your server returns `404` or `410`, the SDK surfaces the exact `e2b.Error` immediately.
2. **High-Level Composite & Data-Plane Features — Automatic Adaptation:**
   Composite workflows and guest operations adapt transparently across server and `envd` versions:
   - **`(*Sandbox).IsRunning` & `GetInfo`:** Avoids unintentionally waking paused `autoResume` sandboxes, probes `envd` `GET /health` (`200/204` -> `true`, `502` -> `false`), falls back to control-plane `GetInfo` when older self-hosted proxies return `404` on `/health`, and normalizes legacy pre-`2025.31` empty `state == ""` responses to `SandboxStateRunning`.
   - **MCP Gateway (`Create` & `CreateV1`):** Defaults `Template` to `"mcp-gateway"` when `Mcp` is configured, generates a random UUID access token, launches `mcp-gateway` as `root` (killing the sandbox on failure), and falls back to `/etc/mcp-gateway/.token` on reconnected sandboxes.
   - **Template Layer Uploads & Build Logs:** Buffers `COPY` tarballs with an exact `Content-Length` (required by AWS S3 presigned `PUT`) while forwarding `TemplateBuildFileUpload.Headers` (required by Azure Blob SAS), and synthesizes `LogEntry` items from legacy `logs: []string` when `logEntries` is empty.
   - **Volume BYOC Routing:** Routes `/volumecontent/{id}/*` to `Config.VolumeAPIURL` / `$E2B_VOLUME_API_URL` (or `http://localhost:8080` in `Debug` mode), otherwise `https://api.<VolumeAndToken.domain>` when returned by BYOC clusters, falling back to `https://api.<Config.Domain>` on non-BYOC E2B Cloud and self-hosted `runtime 2026.07..2026.30`.
   - **`envd` SemVer Gates (`versions.go`):** Gates guest features against `Sandbox.EnvdVersion` (`99.99.99` in `Debug`):
     - `>= v0.1.0`: minimum `envd` floor on sandbox creation
     - `>= v0.1.4`: recursive `Filesystem.Watch` / `WatchWithOptions`
     - `>= v0.1.5`: `Sandbox.GetMetrics`
     - `>= v0.4.0`: omits default username and `Authorization` header unless `User` is explicitly provided (`< v0.4.0` falls back to `"user"`)
     - `>= v0.5.2`: `Commands.CloseStdin` / `CommandHandle.CloseStdin`
     - `>= v0.5.5`: `Filesystem.Compose` (`POST /files/compose`)
     - `>= v0.5.7`: `Filesystem` uploads use `application/octet-stream` (`< v0.5.7` falls back to `multipart/form-data`)
     - `>= v0.6.2`: `Filesystem` extended-attribute `Metadata` (`X-Metadata-*`)
     - `>= v0.6.3`: `WatchOptions.IncludeEntry`
     - `>= v0.6.4`: `WatchOptions.AllowNetworkMounts`

## Architecture

- **Control-plane REST** at `https://api.<domain>` — generated from `spec/openapi.yml` using [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen).
- **`envd` Connect-RPC** at `https://49983-<sandboxID>.<domain>` — generated from `spec/envd/**/*.proto` using [`connectrpc.com/connect`](https://connectrpc.com) (`WithProtoJSON()`), carrying `E2b-Sandbox-Id`, `E2b-Sandbox-Port: 49983`, and `Keepalive-Ping-Interval: 50` on streams.
- **`envd` plain HTTP** (`/files`, `/files/compose`, `/health`, `/metrics`, `/envs`) — generated from `spec/envd/envd.yaml`.
- **Volume content REST** (`belt` `/volumecontent/{volumeID}/*`) — generated from `spec/openapi-volumecontent.yml`.

All generated code lives under `internal/` and is not part of the public API.

## Versioning & regenerating clients

`spec/E2B_VERSION` and `VERSION` track the upstream [`e2b-dev/E2B`](https://github.com/e2b-dev/E2B) SDK spec tag (currently `e2b@2.64.0`):

```sh
make tools                           # one-time: install buf, oapi-codegen, protoc-gen-*
make sync-spec E2B_TAG=e2b@2.64.0    # sync & filter ./spec/, VERSION, internal/version/upstream.go
make codegen                         # regenerate internal/ clients
make regen                           # sync-spec + codegen in one step
make version                         # print current SDK version and pinned upstream spec ref
```

`scripts/sync-spec.sh` reads spec files directly from git objects in `E2B/` without mutating the submodule checkout and runs `scripts/filter_spec.go` to strip internal/admin operations before code generation. Pass `--skip-fetch` to work offline.

## Examples

| Path | What it shows |
| --- | --- |
| [`examples/basic`](./examples/basic) | Minimal `Create` / `Commands.Run` loop |
| [`examples/terminal`](./examples/terminal) | Interactive PTY session |
| [`examples/template`](./examples/template) | Programmatic `template.Builder` (v3 build API) |
| [`examples/lifecycle`](./examples/lifecycle) | Full template + sandbox lifecycle on E2B Cloud (`/v3/templates` + `/v2/sandboxes`) |
| [`examples/lifecycle_v2`](./examples/lifecycle_v2) | Legacy lifecycle on self-hosted E2B (`runtime <= 2026.30`: `ListV1`, `BuildStreamV2`, `SetPublicV1`, `CreateV1`) |
| [`examples/selfhosted`](./examples/selfhosted) | Connecting to a self-hosted E2B deployment (`CreateV1`, session info, exec, metrics) |
| [`examples/desktop`](./examples/desktop) | Split dual-display noVNC desktop (`:0` tmux terminal + `:1` on-demand Chromium) |

Run any example with `source ./.env && go run ./examples/<name>`.

### Desktop example

`examples/desktop` builds the `e2b.Dockerfile` template and exposes **two** separate noVNC URLs — one per X display — so the terminal and browser don't share a desktop:

- `:0` -> `6080` — an xterm attached to the shared `tmux -L main` session. Any `Pty.Create` opened through the SDK appears here in real time.
- `:1` -> `6081` — launched on demand by `launchBrowser`, which starts Chromium with `--no-sandbox --disable-dev-shm-usage` on the browser display and blocks until the window is mapped.

Set `E2B_TEMPLATE_ID` to reuse an already-built template (skips build + teardown). Set `DESKTOP_BROWSER_URL` to override the page Chromium opens.

## Testing

```sh
go test -mod=readonly -race ./...   # unit tests (no external network)
go test -tags=integration ./...     # live integration tests (requires E2B_API_KEY)
```

## License

Apache 2.0, matching upstream E2B.
