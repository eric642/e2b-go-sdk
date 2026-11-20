---
name: golang-architecture
description: "Golang architecture — cmd/internal/pkg, go.work, flat/clean/hexagonal design, functional options, DI (uber-go/fx, dig, samber/do), Cobra/Viper CLIs, and graceful shutdown. Use when scaffolding services, wiring constructors, or building CLIs."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "🏗"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
    install: []
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Agent WebFetch mcp__context7__resolve-library-id mcp__context7__query-docs AskUserQuestion
paths:
  - "**/*.go"
  - "**/go.mod"
  - "**/go.work"
---

**Persona:** You are a Go software architect. You right-size project structure, patterns, and dependency injection to actual scope — a small CLI stays flat, while larger services earn clean boundaries and explicit wiring.

**Orchestration mode:** Fan out three parallel sub-agents (global/`init()` discovery, concrete-dependency mapping, and service-locator detection) when refactoring a large coupled codebase toward clean boundaries and dependency injection, then consolidate findings into one migration plan. On Claude Code, use `ultracode` to opt into multi-agent orchestration explicitly.

**Modes:**

- **Design / Scaffold** — starting a new project, service, or CLI: ask architecture preference first, then DI preference (one at a time), then scaffold the right-sized layout, patterns, and wiring below.
- **Extend** — adding modules, services, or CLI subcommands to an existing repo: inspect the current directory structure, DI setup, and command tree first, then match them.
- **Review / Refactor** — auditing existing code for `init()` globals, service locators, unbounded resources, missing timeouts, or CLI mistakes.

**Questions:** Ask the user through the environment's question tool — never as plain-text prose. Ask architecture preference and DI approach one at a time, in that order, waiting for each answer before proceeding — getting either wrong early cascades into every file created afterward.

# Go Architecture, Layout, DI & CLI Guide

Right-size architecture to project scope — never impose hexagonal layers or a DI container on a 100-line tool.

## 1. Project & Workspace Layout

### Match Structure to Project Type

| Project Type | Use When | Key Directories |
| --- | --- | --- |
| **CLI Tool** | Command-line application | `cmd/{name}/`, `internal/`, optional `pkg/` |
| **Library** | Reusable packages for external consumers | Root public packages, `internal/` for private code |
| **Service** | HTTP/gRPC API, microservice, or worker | `cmd/{service}/`, `internal/`, `api/`, `configs/` |
| **Monorepo / Workspace** | Multiple related Go modules developed together | `go.work`, separate `go.mod` per module |

### Layout & Naming Rules

- **`go.mod` module path:** match the repository URL, lowercase only, hyphens for multi-word names (`github.com/org/payment-processor`, never `MyProject` or `utils`).
- **`cmd/{name}/main.go`:** keep all `main` packages under `cmd/` with minimal wiring only (parse flags, build dependency graph, call `Run()`). Put business logic in `internal/` (compiler-enforced private) and reserve `pkg/` strictly for code intended for external importers.
- **12-Factor services:** configure via environment variables, write logs to stdout/stderr, keep processes stateless, handle graceful shutdown, and put admin jobs in one-off entry points (`cmd/migrate/`).
- **Tests & root files:** co-locate `*_test.go`, `*_bench_test.go`, and `*_example_test.go` next to source files with `testdata/` fixtures; include root [Makefile](assets/Makefile) and [.gitignore](assets/.gitignore).
- **Workspaces (`go.work`):** use `go work init` / `go work use` for multi-module monorepos instead of scattering `replace` directives.

### New Project Checklist

- [ ] Confirm architecture preference (flat, layered, clean, hexagonal, DDD) and DI strategy (one question at a time)
- [ ] Run `go version` and `go mod init github.com/<org>/<project-name>` (lowercase, hyphens)
- [ ] Create `cmd/{name}/main.go` for entry points, `internal/` for private code, `pkg/` only if externally reusable
- [ ] Add root [Makefile](assets/Makefile), [.gitignore](assets/.gitignore), and `go.work` if multi-module

Deep dives: [Directory Layouts](references/directory-layouts.md) · [Go Workspaces](references/workspaces.md) · [Testing Layout](references/testing-layout.md)

## 2. Service Architecture & Idiomatic Patterns

### Right-Sizing Service Layers

| Codebase Size | Recommended Architecture |
| --- | --- |
| Script / small CLI (< 500 lines) | Flat `cmd/` + a few files, zero abstraction layers |
| Medium service (500–5K lines) | Simple layered: `handler/`, `service/`, `repository/` |
| Large service (5K+ lines) | Clean architecture, hexagonal (ports & adapters), or DDD |

Regardless of architecture: **keep domain pure** (no DB/HTTP/framework imports), **fail fast at boundaries** (validate once in handlers/CLI, trust inside domain), **make illegal states unrepresentable** (validated value types, enums starting at `1` with `Unknown = iota` at `0`), and prefer **explicit over implicit** defaults and dependencies.

### Constructors, Initialization & Resilience

- **Functional options (preferred constructor pattern):** scale APIs without breaking callers; return an `error` from `Option` (`func(*Server) error`) whenever option validation can fail. Use builders only for multi-step cross-field validation.

```go
type Option func(*Server) error

func WithReadTimeout(d time.Duration) Option {
    return func(s *Server) error {
        if d <= 0 {
            return errors.New("read timeout must be positive")
        }
        s.readTimeout = d
        return nil
    }
}

func NewServer(addr string, opts ...Option) (*Server, error) {
    s := &Server{addr: addr, readTimeout: 5 * time.Second, maxConns: 100}
    for _, opt := range opts {
        if err := opt(s); err != nil {
            return nil, err
        }
    }
    return s, nil
}
```

- **Avoid `init()` and mutable globals:** `init()` hides dependencies, runs in alphabetical filename order, and cannot return errors — inject dependencies explicitly via constructors.
- **Idiomatic invariants:** compile `regexp.MustCompile` once at package level, embed static assets with `//go:embed`, verify interface satisfaction at compile time (`var _ Interface = (*Type)(nil)`), return errors for expected failures and reserve `panic` for impossible invariants, and use `[]byte` for I/O/mutation vs `string` for display/keys.
- **Resources, streaming & graceful shutdown:** `defer Close()` immediately after opening (report write/flush close errors when durability matters), prefer `runtime.AddCleanup` (Go 1.24+) over `runtime.SetFinalizer`, stream large datasets with Go 1.23+ iterators (`iter.Seq2`), timeout every external call, check `ctx.Err()` in retry backoff loops, bound every pool/queue/buffer, and shut down via `signal.NotifyContext` with a bounded drain timeout.

| Deep Dive | Scope |
| --- | --- |
| [Architecture Patterns](references/architecture.md) | Layer sizing, pure domain, boundary validation, illegal states, explicitness |
| [Clean Architecture](references/clean-architecture.md) | Dependency rule, entities, use cases, consumer-side ports, adapters |
| [Hexagonal Architecture](references/hexagonal-architecture.md) | Primary/secondary ports & adapters, multiple `cmd/` entry points |
| [Domain-Driven Design](references/ddd.md) | Aggregates, value objects, vertical bounded contexts, anti-corruption layer |
| [Resource Management](references/resource-management.md) | Immediate `defer Close()`, `runtime.AddCleanup`, bounded pools, graceful shutdown |
| [Data Handling](references/data-handling.md) | Go 1.23+ `iter.Seq2` lazy row iteration and constant-memory HTTP streaming |

## 3. Dependency Injection

### Core DI Rules

1. **Inject dependencies via constructors** — never use package globals or `init()` for wiring.
2. **Define interfaces where consumed** — accept interfaces, return concrete structs.
3. **Keep the container at the composition root (`main()`)** — never pass the container into services (the service-locator anti-pattern).
4. **Match scope to state** — singletons for stateful infrastructure (DB pools, caches), transients for stateless objects, lazy creation where helpful, and shallow dependency graphs.

### DI Strategy Decision Table

| Criteria | Manual DI | `uber-go/dig` | `uber-go/fx` | `samber/do` |
| --- | --- | --- | --- | --- |
| **Best for** | Small (< 10–15 services) | Medium–large graphs | Large apps wanting a framework | Any size (Go 1.18+) |
| **Type safety** | Compile-time | Runtime (reflection) | Runtime (reflection) | Compile-time (generics) |
| **Lifecycle / shutdown** | Manual (`defer`) | Manual | Built-in (`OnStart`/`OnStop`) | Built-in (`Shutdowner`) |
| **Health checks & scopes** | Manual | `container.Scope` | `fx.Module` | Built-in (`Healthchecker`, scopes) |
| **Test overrides** | Pass mocks directly | Rebuild container | `fxtest.New` + `fx.Populate` | `Clone` + `do.OverrideValue` |
| **Learning curve** | None | Medium | High | Low |

Start with manual constructor injection when a project has fewer than ~10–15 services; adopt a container once wiring order, lifecycle hooks, or scoped overrides become repetitive. Consult official library docs for current API signatures when wiring `dig`, `fx`, or `samber/do/v2`.

```go
// Manual — explicit order in main(); ideal for < 10–15 services
cfg := NewConfig()
db := NewDatabase(cfg)
store := NewUserStore(db)
svc := NewUserService(store)
api := NewAPI(svc)
api.Run()
```

```go
// Container (samber/do) — order derived from providers, built-in Shutdowner
inj := do.New()
do.Provide(inj, NewConfig)
do.Provide(inj, NewDatabase)
do.Provide(inj, NewUserStore)
do.Provide(inj, NewUserService)
api := do.MustInvoke[*API](inj)
defer inj.Shutdown()
api.Run()
```

Deep dives: [Manual DI](references/manual-di.md) · [uber-go/dig & fx](references/uber-dig-fx.md) · [samber/do](references/samber-do.md)

## 4. CLI Application Architecture (Cobra + Viper)

Use `github.com/spf13/cobra` + `github.com/spf13/viper` as the default CLI stack (use stdlib `flag` only for trivial single-command tools).

### Structural & Unix Hygiene Rules

- **Minimal `main.go`:** keep `cmd/myapp/main.go` to calling `Execute()` and mapping errors to Unix exit codes (`0` ok, `1` runtime error, `2` usage, `128+N` signal). Never call `os.Exit()` inside `RunE` — it bypasses `defer` and Cobra cleanup.
- **Root command discipline:** set `SilenceUsage: true` and `SilenceErrors: true`; initialize Viper in `PersistentPreRunE`; place each subcommand in its own file under `cmd/myapp/`.
- **Config layering (highest → lowest):** **CLI flags > Env vars > Config file > Defaults**. Bind every configurable flag via `viper.BindPFlag`, namespace env vars with `viper.SetEnvPrefix`, ignore `viper.ConfigFileNotFoundError`, unmarshal into a `mapstructure`-tagged struct, and load secrets strictly from env vars or secret managers.
- **Testable I/O & signals:** write data to `cmd.OutOrStdout()` (supporting `--output` `table`/`json`/`plain` and auto-disabling colors when piped) and logs/errors to `cmd.ErrOrStderr()`; use `signal.NotifyContext` for graceful interruption; inject build version via `-ldflags`; expose Cobra shell completions and test commands programmatically.

### Exit Codes

| Exit Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Runtime error |
| `2` | Usage / invalid flag or arg |
| `64–78` | BSD sysexits categories |
| `128+N` | Terminated by signal `N` (`130` = `SIGINT`) |

### Built-in Cobra Argument Validators

| Validator | Behavior |
| --- | --- |
| `cobra.NoArgs` | Rejects any positional args |
| `cobra.ExactArgs(n)` | Requires exactly `n` args |
| `cobra.MinimumNArgs(n)` / `cobra.MaximumNArgs(n)` | Enforces lower / upper arg bounds |
| `cobra.RangeArgs(min, max)` | Requires between `min` and `max` args |
| `cobra.MatchAll(cobra.ExactArgs(n), cobra.OnlyValidArgs)` | Requires `n` args matching `ValidArgs` |

Deep dive: [CLI & Cobra/Viper Reference](references/cli-cobra-viper.md)

Runnable CLI examples (`assets/examples/`): [main.go](assets/examples/main.go) · [root.go](assets/examples/root.go) · [serve.go](assets/examples/serve.go) · [flags.go](assets/examples/flags.go) · [args.go](assets/examples/args.go) · [config.go](assets/examples/config.go) · [version.go](assets/examples/version.go) · [exit_codes.go](assets/examples/exit_codes.go) · [output.go](assets/examples/output.go) · [signal.go](assets/examples/signal.go) · [completion.go](assets/examples/completion.go) · [cli_test.go](assets/examples/cli_test.go) · [go.mod](assets/examples/go.mod)

## Common Mistakes

| Area | Mistake | Fix |
| --- | --- | --- |
| Layout | Root `main.go`, `src/`, or `utils/`/`common/` packages | Place mains in `cmd/{name}/`, private code in `internal/`, domain-named packages |
| Patterns | `init()` connecting to DB or setting globals | Explicit constructors returning `(*T, error)` wired in `main()` |
| Patterns | Enum starting at `0` for valid state; unvalidated options | Start valid enums at `1` (`Unknown = iota`); return `error` from validating `Option` |
| Patterns | Missing `defer Close()`, unbounded pools, no external timeouts | Defer immediately, cap pool channels, attach `context.WithTimeout` + `signal.NotifyContext` |
| DI | Passing DI container into services (service locator) | Resolve at composition root only; inject narrow consumer interfaces |
| CLI | `os.Exit` in `RunE`, writing to `os.Stdout`, logging to stdout | Return errors to `main()`, use `cmd.OutOrStdout()`, route logs to `cmd.ErrOrStderr()` |
| CLI | Unbound flags, unprefixed env vars, crashing on missing config | Call `viper.BindPFlag`, `viper.SetEnvPrefix`, ignore `ConfigFileNotFoundError`, set `SilenceUsage: true` |
