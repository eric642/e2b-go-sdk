# Contributing

Thanks for your interest in e2b-go-sdk! This is a community Go port of the
official [E2B](https://e2b.dev) SDKs. We aim to stay wire-compatible with
upstream, so most contributions fall into one of three buckets:

1. **Bug fixes** in existing SDK surface.
2. **Go-ergonomics polish** on top of the generated clients.
3. **Spec upgrades** — bumping the upstream `e2b-dev/E2B` reference.

## Before you open a PR

- Run `go vet ./...` — CI will reject any vet warning.
- Run `go test ./...` — unit tests must pass. Integration tests are gated
  behind `//go:build integration`; see below.
- Keep each PR focused. If you're touching `internal/` generated code
  alongside hand-written code, land the regen separately where possible.
- Don't commit secrets. `.env` is gitignored; double-check `git status`
  before you push.

## Running integration tests locally

Integration tests hit the real E2B API and need an API key:

```sh
export E2B_API_KEY=sk_...
go test -tags=integration -timeout 15m ./...
```

CI runs these only on `push` to `main` / `master` or via
`workflow_dispatch`, because GitHub does not expose secrets to fork PRs.

## Regenerating clients from upstream spec

```sh
make tools                                   # one-time install of buf, oapi-codegen, protoc-gen-*
make regen                                   # sync newest upstream spec + regen internal/
make regen E2B_TAG=python-sdk@2.20.0         # pin to a specific upstream release
make version                                 # confirm what you're building against
```

Hand-edits under `internal/` get overwritten by `make regen` — adjust
generator config or spec files instead.

## Release & version policy

This module tracks `e2b-dev/E2B` `python-sdk@X.Y.Z` tags 1:1. A release
`vX.Y.Z` is always cut from upstream `python-sdk@X.Y.Z`, so
`github.com/eric642/e2b-go-sdk@vX.Y.Z` and `e2b==X.Y.Z` on PyPI describe
the same wire protocol. When you regen for a new upstream tag, update
`CHANGELOG.md` and bump `VERSION`.

## Commit style

- Imperative subject under 72 chars.
- Body explains *why* more than *what* — the diff already shows the what.
- Group related changes; avoid "fix typo" drive-bys in unrelated PRs.

## Code of conduct

Be kind. Assume good faith. Disagree with ideas, not people. If something
about an interaction feels off, reach out privately to the maintainer via
the address in [SECURITY.md](./SECURITY.md).
