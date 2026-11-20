---
name: golang-dependencies
description: "Golang dependency lifecycle — stdlib-first library selection, godig pkg.go.dev lookup, go.mod MVS, replace/tool directives, upgrades, and govulncheck audits. Use when choosing libraries, inspecting published modules, or managing go.mod."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness, and for projects using Golang. Requires internet access for pkg.go.dev lookups via godig.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "📦"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
        - godig
        - govulncheck
    install:
      - kind: go
        package: github.com/samber/godig/cmd/godig@latest
        bins: [godig]
      - kind: go
        package: golang.org/x/vuln/cmd/govulncheck@latest
        bins: [govulncheck]
    skill-library-version: "0.2.0"
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Bash(godig:*) Bash(govulncheck:*) mcp__godig__* mcp__context7__resolve-library-id mcp__context7__query-docs Bash(gopls:*) LSP mcp__gopls__* Agent AskUserQuestion WebFetch WebSearch
paths:
  - "**/go.mod"
  - "**/go.sum"
  - "**/go.work"
---

**Persona:** You are a Go dependency steward and ecosystem expert. You treat every external package as a long-term maintenance commitment — preferring the standard library first, vetting candidates with objective `pkg.go.dev` data, and keeping `go.mod` minimal, verified, and auditable.

**Questions:** Before running `go get` to add any new dependency, ask the user for confirmation via the environment's question tool — present the import path, whether the standard library covers the use case, and vetted alternatives. Never add a new module silently. (Upgrading an existing dependency with `go get -u=patch` needs no prior gate.)

**Dependencies:** `godig` (queries `pkg.go.dev` via CLI or MCP; hosted fallback at `https://godig.samber.dev/mcp`), `govulncheck` (reachable vulnerability scanner, or pinned via Go 1.24+ `go tool govulncheck`).

# Go Dependencies — Selection, Discovery & Module Management

## 1. Choosing Libraries (Standard Library First)

The best library in Go is often no library at all — every external dependency adds attack surface, binary size, and maintenance burden.

### Selection Criteria & Anti-Patterns

Evaluate every candidate in order before proposing it:

1. **Standard library first** — try `encoding/json` (even for high-throughput APIs — switch to `jsoniter`/`sonic` only after `pprof` proves JSON is the bottleneck), `log/slog` (Go 1.21+), `slices`/`maps`/`cmp` (Go 1.21+), `net/http` routing (Go 1.22+), `uuid` (Go 1.27+), and `golang.org/x/...` extensions before any third-party module. See [stdlib.md](references/stdlib.md).
2. **Maturity & adoption** — check maintenance status, license compatibility, and `imported-by` count on `pkg.go.dev` via `godig` (widely imported libraries face stronger backward-compatibility pressure). Ask before suggesting abandoned or maintenance-mode packages (such as `logrus`).
3. **Value over wrapper** — reject thin wrappers around stdlib (`net/http`, `errors`) that trade a new dependency for marginal syntax convenience.

### High-Signal Ecosystem Defaults

Full vetted catalog in [libraries.md](references/libraries.md) and developer tools in [tools.md](references/tools.md):

| Need | Default Pick | When to Deviate |
| --- | --- | --- |
| HTTP routing | `net/http` (Go 1.22+) or `github.com/go-chi/chi` | Full frameworks (`gin`, `echo`, `fiber`) only when batteries-included binding/validation pays off |
| PostgreSQL driver | `github.com/jackc/pgx` | Faster with full PostgreSQL type support; prefer over `lib/pq` |
| SQL / data access | `github.com/sqlc-dev/sqlc` (compile-time SQL safety) or `sqlx` | `ent` (code-generated graph ORM) or `gorm` (reflection ORM, no compile-time SQL checks) |
| Structured logging | `log/slog` (Go 1.21+ stdlib) | `zap` or `zerolog` for measured zero-allocation hot paths; avoid deprecated `logrus` |
| Rate limiting | `golang.org/x/time/rate` (token bucket) | `ulule/limiter` or distributed limiters for per-IP HTTP middleware |
| Kafka / messaging | `github.com/twmb/franz-go` (modern Kafka client) | Prefer over legacy `sarama`; `amqp091-go` for RabbitMQ, `nats.go` for NATS |
| Integration testing | `testcontainers-go` | Isolated fresh container per suite; prefer over shared manual `docker-compose up` |
| Slice/map utilities | `slices` / `maps` stdlib (Go 1.21+) | `samber/lo` only for functional helpers (`Map`, `Filter`, `GroupBy`) absent from stdlib |

## 2. Inspecting Published Modules (`godig` / `pkg.go.dev`)

`godig` queries the **published Go ecosystem** (`pkg.go.dev` + `vuln.go.dev` OSV) — read-only, no authentication, works for modules not yet in `go.mod`. By contrast, `gopls` reasons about **your locally resolved build** (`go.sum` + `replace` forks), `govulncheck` runs whole-tree reachability audits, and Context7 is a fallback for non-Go or unindexed docs.

### Core `godig` Commands (always pass `-o md` for chat output)

| Command | Purpose |
| --- | --- |
| `godig overview <path> -o md` | **Start here** — compact summary (metadata, recent versions, licenses, vulns) in one call |
| `godig search <query> --limit 5 -o md` | Find packages (optional `--symbol` and `--filter`) |
| `godig symbol doc <path> <Symbol> -o md` | Single symbol signature + doc (token-efficient vs LARGE `package doc`) |
| `godig symbols <path> --filter 'kind=="Function"' -o md` | List exported symbols filtered server-side with Go boolean expressions |
| `godig versions <path> -o md` / `major-versions` | All versions (newest first) or separate `/vN` major modules |
| `godig imported-by <path> --limit 20 -o md` | Popularity & adoption signal before vetting a dependency |
| `godig dependencies <path> -o md` | Inspect a published module's `go.mod` footprint before adopting it |
| `godig vulns <path> -o md` | Known CVEs from the Go vulnerability database |

**Efficiency rules:** Start with `overview`; prefer `symbol doc`/`symbol examples` over LARGE `package doc`/`module readme`/`licenses`; parallelize independent read-only `godig` calls in a single turn (or fan out up to 5 parallel sub-agents when comparing many candidates or auditing CVEs so raw output stays out of main context). Full command flags, exit codes, MCP registration (`claude mcp add pkg-go-dev -- godig mcp`), and `--filter` syntax: [godig-reference.md](references/godig-reference.md) and [godig-sample-output.md](references/godig-sample-output.md).

## 3. Managing `go.mod`, Upgrades & Auditing

### Key Rules

- **Always commit `go.sum`** — it stores cryptographic checksums verified by `go mod verify` to detect supply-chain tampering; never add `go.sum` to `.gitignore`. Conversely, **never commit `go.work.sum`** (add it to `.gitignore`; `go.work` is local-only).
- **Minimal Version Selection (MVS)** — unlike npm/pip/cargo, Go selects the **highest minimum version required** across the build graph, not the latest available. Builds are deterministic from `go.mod` alone (`go.sum` is integrity verification, not version locking). See [versioning.md](references/versioning.md).
- **Major version suffix rule** — `v2+` modules must include `/vN` in both `go.mod` (`module example.com/pkg/v2`) and every import path (`v0`/`v1` have no suffix), allowing v1 and v2 to coexist cleanly.
- **Prefer patch-only routine upgrades** — run `go get -u=patch ./...` over `go get -u ./...` (minor upgrades can change behavior). Add `-t` (`go get -u -t ./...`) when test-only dependencies must be upgraded too.
- **Run `go mod tidy` before every dependency commit** — removes unused requirements and adds missing ones. For `go 1.27+`, `go mod tidy` automatically merges duplicate `require` blocks into direct and indirect blocks.
- **Know directive scope** — `replace` and `exclude` apply **only in the main module** and are ignored when your module is imported as a dependency (remove `replace` before publishing a library). Use `retract` as a module author to warn consumers away from broken published versions; use `exclude` as a consumer to skip a broken dependency version. See [conflicts.md](references/conflicts.md).
- **Pin CLI tools with Go 1.24+ `tool` directives** — use `go get -tool <pkg>@latest` and run via `go tool <name>` for reproducible developer and CI tool versions. Use the legacy `//go:build tools` `tools.go` workaround only for Go <1.24.
- **Check reachable CVEs with `govulncheck ./...`** — unlike generic CVE scanners that flag any module presence, `govulncheck` traces static call paths and reports only vulnerabilities your code actually reaches.
- **Use `go mod vendor` for hermetic builds** — commit `vendor/` when CI or Docker environments lack module proxy access.

### Essential Commands

| Command | Purpose |
| --- | --- |
| `go mod tidy` | Add missing deps, remove unused ones (auto-merges `require` blocks in Go 1.27+) |
| `go mod verify` | Verify local module cache matches `go.sum` cryptographic checksums |
| `go mod vendor` | Copy dependencies into `vendor/` for hermetic/offline builds |
| `go mod graph` / `go mod why -m <mod>` | Inspect the module requirement graph or explain why a module is pulled in |
| `go mod edit` | Edit `go.mod` programmatically (`-go=1.27`, `-replace`, `-exclude`) |

```bash
# Add or remove dependencies (ask the user before adding a new module)
go get github.com/google/uuid@v1.6.0
go get github.com/google/uuid@none && go mod tidy

# Routine safe upgrade (patch-only) and test-dep inclusive upgrade
go get -u=patch ./... && go mod tidy && go test ./... && go vet ./...
go get -u -t ./...                                  # include test-only dependencies

# Pin and run CLI tools reproducibly (Go 1.24+ tool directives)
go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
go get -tool golang.org/x/vuln/cmd/govulncheck@latest
go tool govulncheck ./...                           # reachable CVE call-path audit
go get -u tool && go mod tidy                       # deliberate pinned-tool upgrade

# Inspect outdated direct deps and bump target Go directive deliberately
go list -u -m -json all | go-mod-outdated -update -direct
go mod edit -go=1.27 && go mod tidy
```

Go 1.24+ `go.mod` `tool` block shape (keep the project's actual `go` directive; use legacy `//go:build tools` `tools.go` only for Go <1.24):

```go.mod
module example.com/project

go 1.27

tool (
    github.com/golangci/golangci-lint/v2/cmd/golangci-lint
    golang.org/x/vuln/cmd/govulncheck
)
```

**Diagnose:** 1- `govulncheck ./...` (or `go tool govulncheck ./...`) — confirm whether a flagged CVE is actually reachable from your call graph 2- `go mod why -m <module>` and `go mod graph` — trace unexpected transitive pulls or version conflicts 3- `go mod verify` — validate cached modules against `go.sum` checksums 4- `goweight` or `go-size-analyzer` — confirm whether a dependency bloats the compiled binary vs. being test-only

Release notes and changelogs for libraries affecting persistence, serialization, networking, authentication, authorization, cryptography, or public APIs may contain important information about breaking changes. This skill is not exhaustive. Please refer to official package documentation and code examples for more information. Context7 can help as a discoverability platform.

## Detailed References

Load only the reference matching the current task:

- **[stdlib.md](references/stdlib.md)** — Newer Go stdlib v2 packages (`math/rand/v2`, `encoding/json/v2`), promoted `slices`/`maps`/`slog`/`uuid`, and `golang.org/x` extensions.
- **[libraries.md](references/libraries.md)** — Curated, vetted Go libraries across 27 categories (web, DB, testing, logging, messaging, caching, DI, and more).
- **[tools.md](references/tools.md)** — Essential Go development, testing, linting, and dependency-inspection tools.
- **[godig-reference.md](references/godig-reference.md)** — Full `godig` CLI/MCP setup, all 21 commands, exit codes, and `--filter` expression syntax.
- **[godig-sample-output.md](references/godig-sample-output.md)** — Representative `-o md` outputs for every `godig` command.
- **[versioning.md](references/versioning.md)** — SemVer rules, `/vN` major version suffix conventions, pseudo-versions, and Minimal Version Selection (MVS).
- **[auditing.md](references/auditing.md)** — Test-only vs binary deps, `govulncheck` modes, `go-mod-outdated`, `goweight`, and `go-size-analyzer`.
- **[conflicts.md](references/conflicts.md)** — Diagnosing conflicts and using `replace`, `exclude`, and `retract` directives correctly.
- **[workspaces.md](references/workspaces.md)** — Local multi-module development with `go.work` and why `go.work.sum` stays gitignored.
- **[automated-updates.md](references/automated-updates.md)** — Dependabot vs Renovate comparison, auto-merge policies, and update verification.
- **[visualization.md](references/visualization.md)** — Inspecting dependency trees with `go mod graph`, `modgraphviz`, `go-mod-graph`, and `digraph`.

If you encounter a bug or unexpected behavior in `godig`, open an issue at <https://github.com/samber/godig/issues>.
