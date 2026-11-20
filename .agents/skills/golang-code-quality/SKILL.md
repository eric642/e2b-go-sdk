---
name: golang-code-quality
description: "Golang code quality: control-flow readability, MixedCaps/package/interface naming, golangci-lint (.golangci.yml, nolint), and godoc/README/Example docs. Use when reviewing Go style, naming identifiers, configuring golangci-lint, or writing godoc."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness. Requires go compiler and golangci-lint.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "✨"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
        - golangci-lint
    install:
      - kind: brew
        formula: golangci-lint
        bins: [golangci-lint]
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Agent AskUserQuestion EnterWorktree ExitWorktree
paths:
  - "**/*.go"
  - "**/.golangci.yml"
  - "**/.golangci.yaml"
---

# Go Code Quality

Style, naming, linting, and documentation standards that require human judgment — linters enforce mechanical rules, this skill governs clarity. When intentionally breaking a rule, add a brief comment explaining why.

> "Clear is better than clever." — Go Proverbs

## 1. Control Flow & Readability

### Line Breaking & Function Signatures

- Break lines past ~120 characters at semantic boundaries. Function calls with 4+ arguments MUST put one argument per line with the closing parenthesis on its own line (Go trailing-comma style).
- Keep functions focused with **<=4 parameters**; group larger parameter lists into an options struct.
- **Parameter order:** `context.Context` first, then inputs, then output destinations.
- Avoid naked returns outside trivial 1-3 line functions.

```go
handleUsers(
    w,
    r,
    serviceName,
    cfg,
    logger,
)
```

### Declarations, Collections & Literals

- Use `var` for zero-value intent (`var count int`, `var buf bytes.Buffer`) and `:=` for non-zero values.
- Slices and maps MUST be initialized explicitly (`[]T{}`, `map[K]V{}`, or `make([]T, 0, len(in))` when an upper bound is known) — never return nil collections on success (nil slices marshal to JSON `null`; nil maps panic on write) and never preallocate with speculative constants.
- Composite literals MUST use named fields — never positional syntax, even for 2-field structs.

### Flat Control Flow

- Handle errors and edge cases first via early `return` or `continue` in loops so the happy path stays at indentation level 1.
- MUST drop `else` after `return`, `break`, or `continue`. For simple conditional assignment, assign a default first and override with `if` or a tagless `switch`.
- Prefer `switch` over multi-branch `if`/`else if` chains comparing the same value or tiered conditions.
- When a condition has 3+ operands, MUST extract domain-named booleans (`isEligibleUser`, `isArticleReady`); keep expensive checks inline last for short-circuiting. Scope check-only variables inside `if err := ...; err != nil`.

```go
if len(data) == 0 {
    return nil, errors.New("empty data")
}
parsed, err := parse(data)
if err != nil {
    return nil, fmt.Errorf("parsing: %w", err)
}
return transform(parsed), nil
```

### Parameters, Visibility & Strings

- Pass small fixed-size types (`string`, `int`, `float64`, `bool`, `time.Time`) and read-only small structs (<~128 bytes) by value. Use pointers only for mutation, large structs (~128+ bytes), or meaningful `nil` (optional parameters).
- Unexport internal helpers aggressively; every exported symbol is an API commitment. When renaming or unexporting across packages, use `gopls` rename rather than text search-and-replace so interface satisfaction is verified atomically.
- Use `strconv` over `fmt.Sprintf` for simple conversions, `strings.Join` for existing slices, `strings.Builder` for loop accumulation (never `+=` in loops), `range` (including `range n`) for iteration, and stdlib `slices`/`maps` over hand-rolled loops or `reflect`.
- Restrict blank imports (`_ "pkg"`) to `main` and tests; never use dot imports in library code.

Deep dive: [Style Details](./references/style-details.md) (complex condition short-circuiting, value vs pointer memory trade-offs).

## 2. Naming Conventions

All Go identifiers MUST use `MixedCaps` / `mixedCaps` — never `ALL_CAPS` constants or `snake_case` (only exceptions: test subcases like `TestFoo_InvalidInput`, generated code, cgo/OS interop). Acronyms MUST be all-caps or all-lower (`URL`, `HTTPServer`, `userID`, `xmlParser`), never mixed (`Url`, `userId`).

### High-Signal Rules

| Element | Convention | Example |
| --- | --- | --- |
| Package | Lowercase, single-word, singular; never `util`/`helper`/`common`/`base`/`model` | `email`, `currency`, `httputil` |
| Anti-stutter | Never repeat package or type name at call site | `http.Client`, `dbpool.Status`, `email.Validate` |
| Constructor | `New()` for single primary type; `NewTypeName()` for multi-type packages | `apiclient.New()`, `http.NewRequest()` |
| Getters & bools | Omit `Get` on value getters (`Port()`, `Status()`); keep `is`/`has`/`can` on bool fields and predicates | `isConnected bool`, `IsHealthy() bool` |
| Receiver | Consistent 1-2 letter abbreviation; never `this`/`self` | `func (s *Server)` |
| Interfaces | Single-method: verb + `-er`; multi-method: noun; honor canonical names | `Reader`, `Store`, `String()`, `Close()` |
| Enums (`iota`) | Type-name prefix + protect zero value (`Unknown` at 0 or `iota + 1`) | `StatusUnknown`, `StatusReady` |
| Errors | Sentinel `Err` prefix + package-prefixed lowercase string; custom type `Error` suffix | `ErrNotFound = errors.New("dbpool: not found")` |
| Error & subtest strings | Fully lowercase **including acronyms**, no trailing punctuation | `"invalid message id"`, `t.Run("invalid url", ...)` |
| Variants & options | `WithContext` suffix, `In` in-place suffix, `Must` panic prefix, `f` format suffix, `With*` options | `FetchWithContext`, `SortIn`, `MustParseDSN`, `Wrapf` |
| Imports & scope | Alias imports only on collision (`mrand "math/rand"`); length proportional to scope; no type in name; one name per concept | `users` not `userSlice`, `ctx` not `contextCtx` |

```go
// Package dbpool — anti-stutter constructor, zero-value enum protection, package-prefixed error
type State int

const (
    StateUnknown State = iota
    StateReady
)

var ErrInvalidDSN = errors.New("dbpool: invalid dsn") // lowercase including acronym

func New(dsn string, opts ...Option) (*Pool, error)   // callers write dbpool.New(...)
```

Deep dives:

- [Packages, Files & Test Naming](./references/naming-packages-tests.md) — package/directory rules, file suffixes, collision-only import aliasing, table-driven test naming.
- [Variables, Booleans, Receivers & Acronyms](./references/naming-identifiers.md) — scope-length scaling, predictable domain names, parameter clarity, receiver and acronym rules.
- [Functions, Methods & Options](./references/naming-functions-methods.md) — noun/verb functions, value vs bool getters, constructors, named returns, functional options.
- [Types, Constants & Errors](./references/naming-types-errors.md) — single/multi-method interfaces, canonical methods, iota zero-value protection, sentinel vs typed errors.

## 3. Linting & `.golangci.yml`

Every Go project MUST include a `golangci-lint` v2 `.golangci.yml` (`version: "2"`) as the source of truth. Start from the [recommended `.golangci.yml`](./assets/.golangci.yml) (48 curated linters + `gofumpt`/`goimports` configured under `formatters:`, separate from `linters:`).

```bash
golangci-lint run ./...                     # Run all configured linters
golangci-lint run --fix ./...               # Auto-fix mechanical issues
golangci-lint fmt ./...                     # Run formatters (v2 separate section)
golangci-lint run --enable-only govet ./... # Isolate a single linter
golangci-lint linters                       # Inspect linters available in current version
golangci-lint migrate                       # Migrate a v1 config to v2
```

### `//nolint` Discipline & Legacy Adoption

1. `//nolint` directives MUST name specific linter(s) (comma-separated for multiple) AND include a justification — never bare `//nolint`:

   ```go
   //nolint:errcheck // fire-and-forget flush, error is not actionable
   _ = logger.Sync()
   ```

2. Enable `nolintlint` with `require-specific: true` and `require-explanation: true` under `linters.settings.nolintlint` to enforce hygiene automatically.
3. **Fix, don't suppress** correctness, security, and resource-leak linters (`govet`, `staticcheck`, `errcheck` on production paths, `bodyclose`, `sqlclosecheck`, `rowserrcheck`, `gosec`). Suppress complexity/duplication linters only with clear rationale (`funlen` on table tests, `gocyclo` on orchestrators, `dupl` on parallel code, `goconst` in tests).
4. **Prioritize bug-finding linters** (`govet`, `staticcheck`, `errcheck`, `nilerr`, `bodyclose`/`gosec`) over style rules; avoid noisy defaults (`exhaustruct`, `gochecknoglobals`, `wrapcheck`, `mnd`, `varnamelen`) and redundant complexity pairs (`gocyclo` vs `cyclop`/`gocognit`).
5. **Legacy repos:** Set `issues.new-from-rev` (e.g., `HEAD~1` or `main`) to lint only new changes, run `golangci-lint run --fix ./...` first, then batch remaining cleanup by linter category (security, errors, style, quality) — never bulk-add `//nolint`.

### Common Linter Issues

| Problem | Solution |
| --- | --- |
| `deadline exceeded` | Increase `run.timeout` in `.golangci.yml` (e.g., `10m` for large repos) |
| v1 config errors after upgrade | Run `golangci-lint migrate` to convert to `version: "2"` |
| `linter modernize not found` | Check installed version with `golangci-lint linters` (`modernize` needs v2.6.0+) |
| Too many issues on legacy code | Set `issues.new-from-rev: HEAD~1` (or `main`) for incremental adoption |

Deep dives:

- [Recommended `.golangci.yml`](./assets/.golangci.yml) — production v2 config with rationale for enabled and disabled linters.
- [Linter Reference](./references/linter-reference.md) — domain breakdown and description of all 48 linters and formatters.
- [Nolint Directives](./references/nolint-directives.md) — syntax, multi-linter suppression, and fix-vs-suppress judgment.

## 4. Godoc & Project Documentation

### Writing Principles

- **Intent over paraphrase:** Code shows *what*; comments explain *why*, *when* to use it, constraints (nil handling, thread safety, mutation), and error cases. Remove pure paraphrases, marketing fluff (`seamlessly`, `robust`), invented rationale, and hollow filler.
- **Preserve modality:** Keep `must`/`should`/`may` obligations intact when editing.

### Code & Package Comments

- Every exported symbol MUST have a doc comment starting with its exact name and a verb phrase. Document Parameters, Returns/Errors, concurrency safety, and a tab-indented `Example:` (plus `// Play: https://go.dev/play/p/...` for public libraries).
- Every package MUST have a `// Package <name> ...` comment: place it at the top of the main `.go` file for 1-2 file packages, or in a dedicated `doc.go` for 3+ files or >~10 lines.
- Use `Deprecated: Use NewFunc instead...` on its own paragraph for deprecations.
- **Skip doc comments on `TestXxx`/`BenchmarkXxx` functions** (their names are the docs). Add a file-level comment below imports (with ASCII architecture flow) only for complex algorithms/state machines (200+ lines) — not simple CRUD handlers.

```go
// ParseDurationStrict parses s into a non-negative duration.
// Returns ErrInvalidDuration if s is empty or uses unsupported units.
//
// Deprecated: Use Parse instead. Removed in v3.0.0.
//
// Play: https://go.dev/play/p/abc123XYZ
//
// Example:
//
//	d, err := ParseDurationStrict("150ms")
```

### Project & API Documentation

First detect project type: **Library** (importable packages), **Application/CLI** (`cmd/`/`main`), or **Both** (apply each strategy to its respective packages).

- **README exact order:** Title -> Badges -> Summary (1-2 sentences) -> Demo -> Getting Started -> Features (longest) -> Contributing -> License. Template: [README.md](./assets/templates/README.md).
- **CONTRIBUTING (10-minute rule):** New contributors must go from clone to passing tests in under 10 minutes; add `Makefile`, `docker-compose.yml` for external services, `.devcontainer/`, and separate fast unit tests from integration tests. Template: [CONTRIBUTING.md](./assets/templates/CONTRIBUTING.md).
- **CHANGELOG:** Use Keep a Changelog format with `### Security` as its own top-level section separate from `### Fixed`. Template: [CHANGELOG.md](./assets/templates/CHANGELOG.md).
- **AI-friendly docs:** Add repository-root `llms.txt` ([template](./assets/templates/llms.txt)) and register public libraries on Context7, DeepWiki, OpenDeep, and zRead.
- **Libraries:** Write runnable `ExampleXxx` tests in external `pkg_test` packages with `// Output:`, lowercase suffixes (`ExampleConvert_celsius`), and `ExampleType_Method`; verify locally with `go doc` / `pkgsite`.
- **Applications & APIs:** Provide comprehensive `--help`, document config precedence (defaults < file < env < flags), offer multiple install paths (binaries, `go install`, Docker, Homebrew), record ADRs in `docs/architecture/0001-*.md`, and match API docs to protocol (OpenAPI/`swaggo` for REST, commented `.proto` + `buf`/`grpc-gateway` for gRPC, AsyncAPI for events).

Deep dives:

- [Code Comments](./references/code-comments.md) — full comment template, error/interface/method docs, `Deprecated:`, `doc.go`, file descriptions.
- [Library Documentation](./references/library-docs.md) — public vs private matrix, `ExampleXxx`, Playground demos, `pkgsite`, discoverability.
- [Application Documentation](./references/application-docs.md) — CLI `--help`, configuration docs, ADRs, REST/gRPC/AsyncAPI docs.
- [Project Documentation](./references/project-docs.md) — README order, 10-minute CONTRIBUTING rule, Keep a Changelog, distribution & Dockerfiles.
