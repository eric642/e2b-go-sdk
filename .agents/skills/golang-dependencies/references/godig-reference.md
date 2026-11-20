# `godig` Reference — CLI, MCP Setup & `--filter` Syntax

`godig` queries the [pkg.go.dev](https://pkg.go.dev) API and the Go vulnerability database (`vuln.go.dev`, OSV). All operations are **read-only** and require no authentication.

## Table of Contents

- [Setup & MCP Registration](#setup--mcp-registration)
- [Global Flags & Exit Codes](#global-flags--exit-codes)
- [Complete Command Reference](#complete-command-reference)
- [Filter Syntax (`--filter`)](#filter-syntax---filter)
- [Command Examples](#command-examples)

## Setup & MCP Registration

Install the CLI:

```bash
go install github.com/samber/godig/cmd/godig@latest
```

`godig mcp` runs over **stdio** by default, or **streamable HTTP** with `--transport http`. The command is harness-agnostic — any MCP-capable host can point at it. Claude Code registration examples:

```bash
# stdio (client launches godig on demand)
claude mcp add pkg-go-dev -- godig mcp

# streamable HTTP (shared server at /mcp, default :8080)
godig mcp --transport http --addr :8080
claude mcp add --transport http pkg-go-dev http://localhost:8080/mcp

# Hosted public instance (no local install needed)
claude mcp add --transport http pkg-go-dev https://godig.samber.dev/mcp
```

Other MCP-capable harnesses (Cursor, Windsurf, Codex, Gemini CLI) register the same `godig mcp` command or hosted URL in their respective settings files. Prefer the local CLI when `godig` is installed; use the hosted instance as a fallback.

## Global Flags & Exit Codes

**Global flags (all commands):**

- `-o`, `--output table|json|raw|md` — default `table`; pass `-o md` for clean Markdown in chat
- `--base-url` — pkg.go.dev API base URL
- `--vuln-base-url` — Go vulnerability database URL (consulted by `vulns` and `overview`)
- `--timeout` — HTTP request timeout
- `--log-level debug|info|warn|error|off`

All global flags can also be set via `GODIG_*` environment variables.

**Exit codes:** `0` success, `1` runtime error (network error, package not found), `2` usage error (missing/invalid argument or flag, e.g. non-positive `--limit`, or invoking a command group with no subcommand such as `godig package`). Check for `2` to distinguish a malformed invocation from a failed lookup.

## Complete Command Reference

When `godig` runs as an MCP server, each data command below is exposed under the matching operation name. Full `-o md` sample outputs live in [godig-sample-output.md](godig-sample-output.md).

| Command | Args | Specific flags | Purpose |
| --- | --- | --- | --- |
| `overview` | `<path>` | `--version` | Compact summary (metadata, versions, licenses, vulns) — start here |
| `search` | `<query>` | `--symbol --limit --filter` | Find packages (optionally exporting a symbol) |
| `package info` | `<path>` | `--module --version` | Package metadata |
| `package imports` | `<path>` | `--module --version` | Packages this package imports (plain list) |
| `package doc` | `<path>` | `--module --version --goos --goarch --format md\|text\|html\|markdown` | Full package doc (LARGE) |
| `package examples` | `<path>` | `--module --version --goos --goarch --symbol` | Runnable examples (LARGE; scope with `--symbol`) |
| `package licenses` | `<path>` | `--module --version` | License files, full text (LARGE) |
| `symbol doc` | `<path> <symbol>` | `--module --version --goos --goarch` | One symbol's signature + doc (token-efficient) |
| `symbol examples` | `<path> <symbol>` | `--module --version --goos --goarch` | One symbol's runnable examples |
| `symbols` | `<path>` | `--module --version --goos --goarch --limit --filter` | List exported symbols |
| `module info` | `<path>` | `--version` | Module metadata |
| `module licenses` | `<path>` | `--version` | Module license files (LARGE) |
| `module readme` | `<path>` | `--version` | Module README, full Markdown (LARGE) |
| `dependencies` | `<path>` | `--version` | `go.mod` deps: requires / replaces / excludes / `go` directive |
| `packages` | `<path>` | `--version --limit --filter` | Packages contained in a module |
| `versions` | `<path>` | `--limit --filter` | All versions, newest first |
| `major-versions` | `<path>` | `--limit --filter --exclude-pseudo` | Major versions (`v1`, `v2`, …) living as separate modules |
| `imported-by` | `<path>` | `--module --version --limit --filter` | Packages that import this one |
| `vulns` | `<path>` | `--version --limit` | Known vulnerabilities (from the Go vuln DB) |
| `mcp` | — | `--transport stdio\|http --addr --cache-ttl --cache-size` | Run as an MCP server |
| `version` | — | — | Print `godig` version / commit / build date |

## Filter Syntax (`--filter`)

`--filter` (on `search`, `versions`, `major-versions`, `packages`, `imported-by`, `symbols`) takes a **Go boolean expression evaluated server-side, once per result item** — not a regex. Wrap the entire expression in single quotes for the shell.

- **Identifiers** are the item's lowercase JSON keys and differ per command (unknown fields return HTTP 400 `undefined identifier: <name>`). Enum-like values such as `kind` are capitalized (`Function`, not `func`).
- **Operators:** `==` `!=` `<` `<=` `>` `>=`, `&&` `||` `!`, parentheses `(...)`.
- **String functions:** `contains(s, sub)`, `hasPrefix(s, pre)`, `hasSuffix(s, suf)`.
- **Literals:** double-quoted strings (`"Function"`), `true`/`false`, numbers.

| Command | Filterable fields (string unless noted) |
| --- | --- |
| `search` | `modulePath`, `packagePath`, `synopsis`, `version` |
| `versions` | `version`, `modulePath`, `deprecated` (bool), `retracted` (bool), `hasGoMod` (bool), `commitTime` |
| `packages` | `path`, `name`, `synopsis`, `isRedistributable` (bool) |
| `imported-by` | `path` (importing package path) |
| `symbols` | `name`, `kind` (`Function`/`Method`/`Type`/`Variable`/`Constant`), `synopsis`, `parent` |
| `major-versions` | `modulePath`, `major`, `version`, `isLatest` (bool) |

## Command Examples

Always pass `-o md` for Markdown rendering:

```bash
# Compact overview — start here
godig overview github.com/samber/ro -o md

# Search & filter
godig search "result option monad" --limit 5 -o md
godig search "result option" --filter 'hasPrefix(packagePath,"github.com/samber/")' -o md

# Single symbol (token-efficient vs package-wide doc/examples)
godig symbol doc github.com/samber/lo Map -o md
godig symbol examples github.com/samber/oops OopsError.Error -o md

# Filtered lists (listing commands auto-paginate; cap with --limit)
godig symbols github.com/samber/lo --filter 'kind=="Function" && hasPrefix(name,"Map")' -o md
godig versions github.com/samber/lo --filter 'deprecated==false && retracted==false' -o md
godig major-versions github.com/samber/lo -o md
godig imported-by github.com/samber/ro --limit 20 -o md

# Pin version / build context / vulnerabilities
godig package doc github.com/samber/lo --version v1.50.0 -o md
godig symbols github.com/samber/ro --goos linux --goarch amd64 -o md
godig dependencies github.com/samber/ro -o md
godig vulns github.com/samber/ro -o md
```
