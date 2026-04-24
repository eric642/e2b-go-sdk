# E2B Go SDK

A Go client for the [E2B](https://e2b.dev) sandbox platform, ported from the
official [Python](https://github.com/e2b-dev/E2B/tree/main/packages/python-sdk)
and [JavaScript](https://github.com/e2b-dev/E2B/tree/main/packages/js-sdk)
SDKs.

## Install

```sh
go get github.com/eric642/e2b-go-sdk
```

## Quick start

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

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
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

## Authentication

The SDK reads credentials from the environment:

| Variable            | Purpose                                          |
| ------------------- | ------------------------------------------------ |
| `E2B_API_KEY`       | Team API key (`X-API-Key` header)                |
| `E2B_ACCESS_TOKEN`  | User access token (`Authorization: Bearer`)      |
| `E2B_DOMAIN`        | Override the default `e2b.app` domain            |
| `E2B_API_URL`       | Full override of the control-plane URL           |
| `E2B_SANDBOX_URL`   | Override the envd URL (used for tunneling/tests) |
| `E2B_DEBUG`         | `true` targets `http://localhost:3000`           |

Pass an explicit `e2b.Config` to any `*Options` struct to override.

## Packages

| Path                                             | Description                               |
| ------------------------------------------------ | ----------------------------------------- |
| `github.com/eric642/e2b-go-sdk`                  | Core `Sandbox`, `Commands`, `Filesystem`, `Pty`, `Git` |
| `github.com/eric642/e2b-go-sdk/template`         | Template builder + ReadyCmd helpers       |
| `github.com/eric642/e2b-go-sdk/volume`           | Persistent volume client                  |

## Architecture

- **Control-plane REST** at `https://api.<domain>` — generated from
  `spec/openapi.yml` using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen).
- **envd Connect-RPC** at `https://49983-<sandboxID>.<domain>` — generated from
  `spec/envd/**/*.proto` using [connectrpc.com/connect](https://connectrpc.com).
- **envd plain HTTP** (`/files`, `/metrics`, `/envs`) — generated from
  `spec/envd/envd.yaml`.
- **Volume content REST** — generated from `spec/openapi-volumecontent.yml`.

All generated code lives under `internal/` and is not part of the public API.

## Versioning & regenerating clients

This SDK tracks upstream [`e2b-dev/E2B`](https://github.com/e2b-dev/E2B)
`python-sdk@X.Y.Z` tags **1:1**. A release `vX.Y.Z` of this module is
always generated from upstream `python-sdk@X.Y.Z` — so
`github.com/eric642/e2b-go-sdk@vX.Y.Z` and the official
`e2b==X.Y.Z` PyPI release describe the exact same wire protocol.

```sh
make tools                                   # one-time: install buf, oapi-codegen, protoc-gen-*
make sync-spec E2B_TAG=python-sdk@2.20.0     # pin upstream spec (fetches tags)
make codegen                                 # regenerate internal/ clients

# Convenience: sync + regen in one step. Defaults to the newest python-sdk tag.
make regen
make regen E2B_TAG=python-sdk@2.20.0

# Inspect what you're currently building against:
make version
```

`sync-spec.sh` copies the relevant spec files from the `E2B/` submodule
into `./spec/` (so the submodule can stay detached at any ref without
affecting builds), writes `spec/E2B_VERSION` and
`internal/version/upstream.go`, and updates the top-level `VERSION` file.

Pass `--skip-fetch` to work offline (reuses the submodule's existing
local refs). See [`CHANGELOG.md`](./CHANGELOG.md) for the full release
workflow.

## Testing

```sh
go test ./...                    # unit tests (no network)
go test -tags=integration ./...  # integration tests (requires E2B_API_KEY)
```

## Scope & status

v1 implements the core sandbox surface:

- [x] `Create`, `Connect`, `Kill`, `Pause`, `CreateSnapshot`, `GetInfo`, `GetMetrics`, `SetTimeout`
- [x] `Commands.Run` / `Start` / `Connect` / `List` / `Kill` / `SendStdin` / `CloseStdin`
- [x] `Pty.Create` / `Resize` / `SendInput` / `Kill`
- [x] `Filesystem` `Read` / `Write` / `List` / `Stat` / `Move` / `Remove` / `MakeDir` / `Watch`
- [x] `Git` Clone / Add / Commit / Push / Pull / Status / Branches / …
- [x] `Volume` Create / Connect / List / ReadFile / WriteFile / Remove / MakeDir / Delete
- [x] `template.Builder` serialization + `ReadyCmd` helpers
- [ ] `template.Builder.Build` (server-side build orchestration) — planned for
      v1.1; use the upstream `e2b` CLI in the meantime.

## License

Apache 2.0, matching upstream E2B.
