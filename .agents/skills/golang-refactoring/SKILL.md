---
name: golang-refactoring
description: "Golang semantic navigation, diagnostics, modernization, and safe refactoring via gopls (MCP/LSP/CLI), modernize, gofmt -r, and gopatch. Use when renaming symbols, upgrading Go versions, breaking import cycles, or staging refactors."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness, and for projects using Golang. Requires gopls (v0.20+) and git.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "♻️"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
        - gopls
    install:
      - kind: go
        package: golang.org/x/tools/gopls@latest
        bins: [gopls]
      - kind: go
        package: golang.org/x/perf/cmd/benchstat@latest
        bins: [benchstat]
    skill-library-version: "1.27"
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Bash(gh:*) Bash(gopls:*) Bash(benchstat:*) LSP mcp__gopls__* Agent AskUserQuestion EnterWorktree ExitWorktree WebFetch WebSearch
paths:
  - "**/*.go"
  - "**/go.mod"
---

**Persona:** You are a Go refactoring and modernization engineer. You rely on semantic code intelligence (`gopls`) instead of blind text search, never mix structural and behavioral changes in the same step, keep a green safety net, and land changes as small, reviewable PRs.

**Thinking mode:** Reason as thoroughly as possible during planning and ordering — mapping blast radius, sequencing PRs to avoid merge conflicts, and verifying version-upgrade risks punish shallow reasoning because mistakes surface late as broken builds or tangled merges. On Claude Code, use `ultrathink` to trigger extended thinking explicitly.

**Orchestration mode:** Fan out up to 5 parallel sub-agents for a read-only full-codebase modernization scan or a single-pass mechanical sweep (`gofmt -r`/`eg`/`modernize` applied tree-wide with no dependent stages). Do NOT use autonomous multi-stage workflows for staged structural refactors that require human PR review between merges. On Claude Code, use `ultracode` only for that single-pass sweep or read-only fan-out scan.

**Modes:**

- **Plan mode** (mandatory gate before multi-step refactors) — map blast radius via `gopls`, build a refactoring inventory, order PRs, and get explicit user sign-off before editing. See [workflow.md](references/workflow.md).
- **Execute mode** (human-in-the-loop) — one sub-agent, one isolated worktree, one branch, and one PR per atomic change on `refactor/<topic>`; parallel when file-disjoint, sequential when overlapping.
- **Modernize mode** — inline (suggest only modernizations relevant to the file being edited; never hijack another task with unrelated churn) or full-scan (5 read-only parallel sub-agents covering deprecated packages, language features, stdlib upgrades, testing patterns, and tooling/infra, then apply rewrites in an isolated worktree).
- **Review mode** — verify structural/behavioral separation, tool-verified safety, and behavior preservation before approving.

**Questions:** Ask sign-off gates (Plan mode approval, mid-refactor irreversible checkpoints, and inline modernization opt-in) through the environment's question tool, never as plain-text prose the reader might skim past. If the user declines inline modernization, stop immediately and do not re-raise it in the session.

**Dependencies:** `gopls` v0.20+ (primary actuator — `go install golang.org/x/tools/gopls@latest`). Optional: `golangci-lint`, `benchstat`, `deadcode`, `eg`, `gopatch`.

# Go Refactoring, Modernization & `gopls`

## 1. Semantic Navigation & Diagnostics (`gopls`)

`gopls` reasons exclusively about **your locally resolved build** — your workspace plus every dependency pinned in `go.sum`, including `replace` directives. For published modules not yet in `go.mod` (versions, licenses, CVEs, ecosystem importers), query `pkg.go.dev` via `godig` instead.

### Reachability Preference Order: MCP → native `LSP` → CLI

- **gopls MCP server (preferred for agents)** — takes symbol names, file paths, and fuzzy queries instead of cursor coordinates; runs headless over stdio against files saved to disk. Claude Code registration: `claude mcp add gopls -- gopls mcp` (other MCP hosts point at `gopls mcp` in their own config). Full tool list: [gopls-mcp.md](references/gopls-mcp.md).
- **Native `LSP` tool** — editor-style operations (`goToDefinition`, `findReferences`, `hover`, `documentSymbol`, `workspaceSymbol`, `goToImplementation`, call hierarchy) keyed by `line`/`character`; requires `ENABLE_LSP_TOOL=1` and the `gopls-lsp@claude-plugins-official` plugin on Claude Code. Unique value: pushes compiler diagnostics automatically after every edit.
- **`gopls` CLI** — experimental debugging fallback (`gopls <cmd> file.go:line:col` or `file.go:#offset`) when neither MCP nor native LSP is available. Full CLI & `codeaction` kinds: [gopls-cli.md](references/gopls-cli.md).

### Required Read & Edit Workflows

1. **Session start** — inspect workspace layout (`go_workspace`), then run a baseline `go_vulncheck` if it is a Go workspace.
2. **Read workflow** — locate symbols (`go_search`) → inspect intra-package imports after reading a file (`go_file_context`) → inspect sibling or dependency public surfaces (`go_package_api`).
3. **Edit workflow** — map blast radius first (`go_symbol_references`) → apply all planned edits including reference sites → run mandatory post-edit diagnostics (`go_diagnostics`) and fix reported errors → run `go_vulncheck` if `go.mod` changed → run `go test <changed-packages>` (targeted packages first for fast iteration).

**Key `gopls` gotchas:**

- `references` only covers the **build configuration of the queried file** (e.g., `foo_windows.go` misses `bar_linux.go`) — set `buildFlags` or query under matching tags (see [gopls-settings.md](references/gopls-settings.md)).
- `call_hierarchy` tracks **static calls only** — dynamic calls through interfaces or function values require corroborating with `references`.
- Extract/Inline actions are less rigorous than Rename: Extract can drop comments, and `DO NOT EDIT` generated files receive no code actions.
- `refactor.rewrite.fillStruct` searches only the current file above the cursor and requires the struct's package already imported (`source.organizeImports` first).

## 2. Go Version & Stdlib Modernization

Keep codebases current across recent Go releases while strictly respecting the module's target version.

### Modernization Workflow

1. **Check `go.mod` / `go.work`** for the `go` directive — never suggest APIs newer than the module's target Go version (Go 1.27+ runs the `stdversion` vet check in `go test` by default). Suggest upgrading `go.mod` when behind.
2. **Read `.modernize`** in the project root — skip every ignored suggestion recorded there. When the developer declines a suggestion, append a line (`<YYYY-MM-DD> <category> <reason>`, e.g. `2026-01-15 slog-migration Team decided to keep zap for now`) so it is not re-suggested.
3. **Run automated checks** — `go fix ./...` (rewritten onto `go/analysis` in Go 1.26, expanded in Go 1.27) and `golangci-lint` with the `modernize` linter (v2.6.0+), followed by `go test ./...`.
4. **Scope strictly** — during active feature work, suggest only modernizations in touched code; for full-codebase modernization, run the 5-agent read-only scan and apply changes in an isolated worktree.

### Go Version Changelogs & Deprecated Replacements

| Version | Changelog | Headline Modernizations |
| --- | --- | --- |
| Go 1.21 | <https://go.dev/doc/go1.21> | `min`/`max`/`clear`, `slices`, `maps`, `log/slog`, `sync.OnceValue`/`OnceFunc`, `crypto/ecdh`, `unsafe.Slice`/`String` |
| Go 1.22 | <https://go.dev/doc/go1.22> | Loop-var fix (drop `v := v`), `range N`, `math/rand/v2` (`IntN`/`Int64N`), `net/http` `METHOD /path/{id}` + `r.PathValue`, `cmp.Or`, `reflect.PointerTo`, `sql.Null[T]` |
| Go 1.23 | <https://go.dev/doc/go1.23> | Iterators (`iter`), `slices`/`maps` seq helpers, `unique`, GC'd unstopped timers/tickers |
| Go 1.24 | <https://go.dev/doc/go1.24> | `os.Root`, `omitzero` JSON tag, `t.Context()`, `b.Loop()`, `runtime.AddCleanup`, `weak`, `crypto/sha3`/`hkdf`/`pbkdf2`, `tool` directives, generic type aliases, `strings.SplitSeq` |
| Go 1.25 | <https://go.dev/doc/go1.25> | `sync.WaitGroup.Go`, `testing/synctest.Test` + `synctest.Wait()`, container-aware `GOMAXPROCS` (drop `automaxprocs`), `runtime/trace.FlightRecorder` |
| Go 1.26 | <https://go.dev/doc/go1.26> | `errors.AsType[T]`, `new(expr)`, `crypto/hpke`, RSA-OAEP over PKCS#1 v1.5, `ReverseProxy.Rewrite`, Green Tea GC (10–40% lower overhead), modernized `go fix ./...` |
| Go 1.27 | <https://go.dev/doc/go1.27> | Generic methods (`func (s Set[T]) Map[U ...]`), `strings.CutLast`/`bytes.CutLast`, stdlib `uuid` (`uuid.New().String()`, `uuid.NewV7()`), `encoding/json/v2` default (`UnmarshalRead`, strict duplicate keys), `httptest.NewTestServer` (for `synctest`), `goroutineleak` profile |

#### Deprecated Packages Migration

| Deprecated | Replacement | Since |
| --- | --- | --- |
| `math/rand` | `math/rand/v2` (`IntN`, `Int64N`, no `Seed`, no `Read`) | Go 1.22 |
| `crypto/elliptic` (most functions) | `crypto/ecdh` | Go 1.21 |
| `reflect.SliceHeader`, `StringHeader` | `unsafe.Slice`, `unsafe.String` | Go 1.21 |
| `reflect.PtrTo` | `reflect.PointerTo` | Go 1.22 |
| `runtime.GOROOT()` | `go env GOROOT` | Go 1.24 |
| `runtime.SetFinalizer` | `runtime.AddCleanup` | Go 1.24 |
| `crypto/cipher.NewOFB`, `NewCFB*` | AEAD modes or `NewCTR` | Go 1.24 |
| `golang.org/x/crypto/{sha3,hkdf,pbkdf2}` | `crypto/sha3`, `crypto/hkdf`, `crypto/pbkdf2` | Go 1.24 |
| `testing/synctest.Run` | `testing/synctest.Test` | Go 1.25 |
| `go.uber.org/automaxprocs` | Built-in container-aware `GOMAXPROCS` | Go 1.25 |
| `crypto/rsa.EncryptPKCS1v15` (new encryption) | RSA-OAEP or `crypto/hpke` | Go 1.26 |
| `httputil.ReverseProxy.Director` | `ReverseProxy.Rewrite` | Go 1.26 |
| `crypto/tls.Config.Rand` | `testing/cryptotest.SetGlobalRandom()` | Go 1.27 |
| `github.com/google/uuid` (standard cases) | `uuid` (stdlib; check RFC v3/v5 needs first) | Go 1.27 |

**Go 1.27+ version-bump risk checklist (verify, do not blindly rewrite):** A `godebug` line in `go.mod` or `//go:debug` pinning removed keys (`asynctimerchan`, `tlsunsafeekm`, `tlsrsakex`, `tls3des`, `tls10server`, `x509keypairleaf`, `gotypesalias`) to old values now **fails the build**; `encoding/json` now defaults to `encoding/json/v2` (rejects duplicate JSON keys and invalid UTF-8 — use `GOEXPERIMENT=nojsonv2` only as a temporary bridge); `crypto/tls.Config.Rand` is replaced by `testing/cryptotest.SetGlobalRandom()`. Full details and before/after recipes: [modernize-versions.md](references/modernize-versions.md) and [modernize-tooling.md](references/modernize-tooling.md).

**Migration priority order:** (1) **Safety & correctness first** — loop-var bugs, `math/rand/v2`, `os.Root` for user paths, `govulncheck`, `errors.Is`/`As`, deprecated crypto, Go 1.27 removed `GODEBUG` checks; (2) **Readability & ergonomics** — `any`, `min`/`max`, `range N`, `slices`/`maps`, `cmp.Or`, `sync.OnceValue`, `WaitGroup.Go`, `t.Context()`, `b.Loop()`, `omitzero`, `errors.AsType`/`new(val)`, `CutLast`, generic methods, `encoding/json/v2`; (3) **Gradual improvements** — `slog`, iterators, `tool` directives, PGO, stdlib `uuid`, `go fix ./...`.

## 3. Safe At-Scale Structural Refactoring

**The Core Loop:** **Understand** (`gopls` blast radius) → **Safety net** (measure blast-radius coverage, add characterization tests if <80%) → **Small tool-driven step** → **Verify** (`go build ./... && go vet ./... && go test ./...`, plus `-race` / `benchstat`) → **Atomic single-category commit**. Repeat.

### Hard Rules

- **Never mix structural and behavioral changes in one commit or PR** — separate review postures keep structural PRs fast and behavioral PRs thorough.
- **Split code moves from optimizations into sequential 100–500L PRs** — moves are verified by `gopls` + tests, optimizations need `benchstat` benchmarks, and parallelizing overlapping files only creates merge conflicts.
- **Prefer `gopls` Rename/Inline over LLM hand-edits** — both are behavior-preserving by construction (Rename refuses shadowing or broken interface satisfaction; Inline preserves side-effect evaluation order). After any rename, **grep for struct tags, `text/template`, and `reflect` references** that `gopls` cannot see.
- **Escalate recurring rewrites to tools, never `sed`/`perl`** — `gofmt -r` → `eg` → `gopatch` → `go/analysis` fixer (see [go-tooling.md](references/go-tooling.md)), always followed by `goimports -w .` and `deadcode ./...`.
- **Use a type alias (`type Old = newpkg.New`) + `// Deprecated:` for cross-package type moves** — enables gradual caller migration with zero flag day (see [structural.md](references/structural.md)).
- **Break import cycles with a consumer-side interface first** — Go's implicit interfaces invert the dependency with zero call-site edits before resorting to a shared leaf or `internal/` package.
- **Pause for human sign-off (via the question tool) before** any cross-package move, package split, exported-API change or deprecation, deletion, new major version, or editing untested code.
- **Run security and correctness checks on any behavioral step** — mechanical transforms preserve semantics, but any change to logic can introduce vulnerabilities or subtle bugs.
- **Start every step from a clean commit and revert on red** — rolling back to the last green commit is safer and faster than debugging forward in an untrusted intermediate state.

### When Not to Refactor

Skip or defer refactoring when: (1) the code works and no planned work will touch it again, (2) it is critical production code with no tests (characterization net + sign-off is non-negotiable), (3) a tight deadline leaves no review bandwidth for staged PRs, or (4) there is no concrete purpose behind the request.

### Risk Stratification

| Risk | Transforms | Safety Requirement |
| --- | --- | --- |
| **Low** | `gopls` Rename, Extract Variable/Constant, Inline Variable/Call, `gofmt -s`, organize imports, local `refactor.rewrite.*` | `go build && go vet && go test` after each step |
| **Medium** | Extract Function/Method (verify comments survived), single-parameter add/remove, introducing generics | Confirm or add targeted tests over blast radius first |
| **High** | Cross-package moves, package splits/merges, breaking import cycles, multi-caller signature changes, exported-API/major-version changes | Full coverage safety net + explicit human checkpoint |

**Diagnose:** 1- `gopls` refusing Rename or Inline — real semantic hazard (shadowing/interface break), investigate before forcing by hand 2- `go vet ./...` / `golangci-lint run` flagging a new issue — fix before committing 3- `go test -race ./...` reporting a race — stop, concurrency behavior changed 4- `benchstat old.txt new.txt` showing non-`~` delta on a hot path — stop and investigate regression 5- `go tool cover -func` (with `-coverpkg=./...`) on touched functions — gates the safety-net tier in [safety-net.md](references/safety-net.md)

### Staging Multi-Step Refactors (Plan → Stage → Land)

A refactor of any real size lands as an ordered sequence of small, human-reviewed PRs targeting a `refactor/<topic>` integration branch — never merge intermediate PRs directly to `main`:

1. **Plan & sign-off** — map blast radius with `gopls`, produce a refactoring inventory (transform, files, Risk, Structural vs Behavioral), order by Beck (structural before behavioral), conflict-avoidance (overlapping files sequential; workspace-wide rename runs alone as a barrier), and dependency order, then obtain user sign-off via the question tool.
2. **Stage per-row worktrees** — seed `refactor/<topic>` with `// REFACTOR(step N): ...` markers for multi-PR plans, dispatch each ready row to a sub-agent in an isolated worktree (parallel only when file-disjoint), verify green, and open a ready-for-review PR against `refactor/<topic>`.
3. **Final sweep & draft PR** — verify `grep -rn "REFACTOR(" .` returns zero hits, then open a draft PR merging `refactor/<topic>` into `main`. Full choreography: [workflow.md](references/workflow.md).

## Detailed References

Load only the reference matching the current task:

- **[gopls-mcp.md](references/gopls-mcp.md)** — `gopls mcp` server setup, all 8 MCP tools, and native `LSP` tool configuration.
- **[gopls-cli.md](references/gopls-cli.md)** — `gopls` CLI position syntax, flags, subcommands, and `codeaction` kinds.
- **[gopls-features.md](references/gopls-features.md)** — Full `gopls` navigation, diagnostics, transformation, and web-feature catalog.
- **[gopls-matrix.md](references/gopls-matrix.md)** — One-page capability mapping across CLI, MCP, and native LSP.
- **[gopls-settings.md](references/gopls-settings.md)** — Key `gopls` settings (`buildFlags`, `analyses`, `staticcheck`, `hints`, `symbolScope`).
- **[modernize-versions.md](references/modernize-versions.md)** — Complete Go 1.21–1.27 before/after modernization recipes and Go 1.27 risk checklist.
- **[modernize-tooling.md](references/modernize-tooling.md)** — `go fix`, `golangci-lint` v2, `govulncheck`, PGO, and CI review modernization.
- **[workflow.md](references/workflow.md)** — Planning gate, refactoring inventory, Beck/conflict/dependency PR ordering, `refactor/<topic>` git model, and `// REFACTOR(step N):` markers.
- **[catalog.md](references/catalog.md)** — Fowler refactoring catalog mapped to Go smells, mechanics, tools, and risks.
- **[go-tooling.md](references/go-tooling.md)** — Bulk rewrite escalation (`gofmt -r`, `eg`, `gopatch`, `go/analysis`, `//go:fix inline`, `dave/dst`, `deadcode`).
- **[safety-net.md](references/safety-net.md)** — Coverage-adaptive safety tiers (HIGH/MEDIUM/LOW), Feathers seams, and verification commands.
- **[structural.md](references/structural.md)** — Breaking import cycles, package boundaries, `type A = B` gradual code repair, and API versioning.

If you encounter a bug or unexpected behavior in `gopls`, open an issue at <https://github.com/golang/go/issues>.
