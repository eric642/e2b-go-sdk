# Changelog

Releases of `github.com/eric642/e2b-go-sdk` mirror upstream
[e2b-dev/E2B](https://github.com/e2b-dev/E2B) `e2b@X.Y.Z` tags 1:1 once the
first aligned release ships. Pre-alignment (v0.x) releases pin an exact
upstream commit; each entry records both the Go SDK version and the
upstream spec source.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.0] — 2026-04-29

Initial public preview. Pinned to upstream `main` at commit
`a6a1156a76753d5e9bbdd9eb7b26e84aa18b24d2`. Subsequent v0.x releases may
pin newer commits; the first v1.0 release will align to a real upstream
`e2b@X.Y.Z` tag.

### Added

- Initial Go port of the core Sandbox surface: `Create`, `Connect`, `Kill`,
  `Pause`, `CreateSnapshot`, `GetInfo`, `GetMetrics`, `SetTimeout`.
- `Commands` (`Run`, `Start`, `Connect`, `List`, `Kill`, `SendStdin`,
  `CloseStdin`) with channel-based streaming.
- `Pty` (`Create`, `Resize`, `SendInput`, `Kill`).
- `Filesystem` (`Read`, `Write`, `List`, `Stat`, `Move`, `Remove`,
  `MakeDir`, `Watch`) over Connect-RPC + plain HTTP `/files`.
- `Git` wrapping `Commands` (clone/add/commit/push/pull/status/branches).
- `volume/` package for persistent volumes
  (Create/Connect/List/Read/Write/Remove/Delete).
- `template/` package with fluent `Builder` and `ReadyCmd` helpers
  (`WaitForPort`, `WaitForURL`, `WaitForFile`, …). The remote
  `Build()` call is not yet wired — it returns `e2b.ErrNotImplemented`.
- `scripts/sync-spec.sh` to pin the upstream E2B tag and regenerate every
  client. Exposes `UpstreamTag`, `UpstreamSemver`, `UpstreamCommit`, and
  `SDKVersion` via `internal/version`.

## Release workflow

When cutting a release:

1. `make sync-spec E2B_TAG=e2b@X.Y.Z` (or omit `E2B_TAG` to track the
   newest `e2b@*` tag).
2. `make codegen` — regenerate clients from the freshly copied specs.
3. `go vet ./... && go test ./...` — verify nothing drifted.
4. `golangci-lint run` — lint must pass too.
5. Update this `CHANGELOG.md` with an entry listing the upstream
   changeset notes and any Go-specific fixes.
6. `git add spec/ internal/ VERSION CHANGELOG.md && git commit -m "release vX.Y.Z"`.
7. `git tag vX.Y.Z && git push origin main --tags`.

Once aligned with upstream, the Go module tag (`vX.Y.Z`) matches the
`e2b@X.Y.Z` tag it was generated from so consumers can pin
`github.com/eric642/e2b-go-sdk@vX.Y.Z` and know exactly which upstream
spec they are running against.
