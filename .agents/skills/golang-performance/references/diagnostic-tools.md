# Diagnostic Tools

Use these tools to validate the root cause of a bug or slowdown **before** applying any fix or optimization. Do NOT use auto-fix flags (e.g., `-fix`) — interpret the results and apply changes manually with explanatory comments.

For dedicated deep dives, see [pprof Reference](./pprof.md), [benchstat Reference](./benchstat.md), [Trace Reference](./trace.md), and [Compiler Analysis](./compiler-analysis.md).

## Table of Contents

- [Runtime Diagnostics (GODEBUG and GOTRACEBACK)](#runtime-diagnostics-godebug-and-gotraceback)
  - [Go Documentation Command](#go-documentation-command)
  - [GODEBUG Quick Reference](#godebug-quick-reference)
  - [GC Tracing](#gc-tracing)
  - [Scheduler Tracing](#scheduler-tracing)
  - [GOTRACEBACK](#gotraceback)
- [Programmatic Runtime APIs](#programmatic-runtime-apis)
- [Delve Debugger (dlv)](#delve-debugger-dlv)
  - [Installation](#installation)
  - [Basic Usage](#basic-usage)
  - [Common Commands](#common-commands)
  - [IDE Integration](#ide-integration)
- [Static Analysis Tools](#static-analysis-tools)
- [Third-Party Profilers](#third-party-profilers)

## Runtime Diagnostics (GODEBUG and GOTRACEBACK)

Configure via environment variables — no recompile needed.

### Go Documentation Command

Use `go doc`, not `go tool doc`. Go 1.26 removed the old `cmd/doc` / `go tool doc` path. Go 1.27 added `package@version` lookups (`go doc golang.org/x/tools/cmd/stringer@v0.30.0`) and an `-ex` flag that lists executable examples.

### GODEBUG Quick Reference

| Command | Use for |
| --- | --- |
| `GODEBUG=gctrace=1 ./app` | GC frequency, pause times, heap sizes, CPU% — one line per GC cycle |
| `GODEBUG=gcpacertrace=1 ./app` | Why GC triggers when it does — pacer decisions (trigger ratio, heap goal) |
| `GODEBUG=schedtrace=1000 ./app` | Load balancing, goroutine distribution across Ps — prints every 1000ms |
| `GODEBUG=schedtrace=1000,scheddetail=1 ./app` | Per-goroutine state detail on top of schedtrace |
| Heap/alloc profiles (`go tool pprof -alloc_objects`) | Allocation sites and object churn; use instead of removed/stale allocation trace flags |

### GC Tracing

```bash
GODEBUG=gctrace=1 ./app
```

**Output:**

```
gc 123 @45.67s 4%: 0.8+10+0.3 ms clock, 6+5/10/0 ms cpu, 512->300->150 MB
```

| Field | Meaning |
| --- | --- |
| `4%` | GC CPU overhead (if >10%, over-allocating) |
| `512->300->150 MB` | Heap at GC start -> heap at GC end -> live heap |
| Large pause | Allocation storm |

### Scheduler Tracing

```bash
GODEBUG=schedtrace=1000,scheddetail=1 ./app
```

| Signal | Meaning |
| --- | --- |
| `runqueue` high | CPU saturation, goroutines waiting |
| `idleprocs=0` | Fully busy, at capacity |
| `spinningthreads` | Lock contention |
| `threads > gomaxprocs` | Blocking syscalls |

### GOTRACEBACK

Get full stack traces on panic:

```bash
GOTRACEBACK=all ./app
```

| Level | Shows |
| --- | --- |
| `none` | No stack traces |
| `single` | Current goroutine only (default) |
| `all` | All goroutines (useful for deadlocks) |
| `system` | All goroutines + runtime frames |

## Programmatic Runtime APIs

- **`runtime.ReadMemStats`** — heap size, `NumGC`, pause durations (`PauseNs` circular buffer), `TotalAlloc` (cumulative). Triggers a short stop-the-world pause; prefer `runtime/metrics` on hot services.
- **`debug.ReadGCStats`** — GC-specific statistics: pause percentiles, pause timeline, total pause duration. More focused than `ReadMemStats`.
- **`runtime/metrics` (Go 1.16+)** — stable API, safe for concurrent reads, lower overhead than `ReadMemStats`. Key series: `/gc/cycles/total:gc-cycles`, `/gc/heap/allocs:bytes`, `/gc/pauses:seconds`, `/sched/latencies:seconds`, `/memory/classes/heap/released:bytes`.
- **`debug.FreeOSMemory()`** — forces GC and returns memory to OS. One-off use after large temporary allocations (not for regular use — let the runtime manage this).
- **`expvar`** — stdlib metrics at `/debug/vars` as JSON. `import _ "expvar"` auto-registers. Lightweight, no dependencies.

## Delve Debugger (dlv)

### Installation

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

### Basic Usage

```bash
dlv debug ./cmd/myapp          # debug a program
dlv test ./mypackage           # debug a test
dlv attach 12345               # attach to running process
dlv exec ./myapp -- --flag=v   # execute binary with args
```

### Common Commands

```
break main.main      # set breakpoint
break file.go:42     # break at line
continue             # continue execution
next                 # step over (n)
step                 # step into (s)
stepout              # step out
print variable       # print variable
locals               # print all locals
args                 # print function arguments
goroutines           # list all goroutines
goroutine 5          # switch to goroutine 5
stack                # show stack trace
```

### IDE Integration

**VS Code:**

```json
// .vscode/launch.json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Launch Package",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}",
      "env": { "GOTRACEBACK": "all" }
    }
  ]
}
```

**GoLand:** Run -> Run/Debug Configurations -> Go Build. Click the gutter to set breakpoints and use the Debugger tab.

## Static Analysis Tools

| Command | Use for |
| --- | --- |
| `fieldalignment ./...` | Detect suboptimal struct field ordering (padding waste). Do NOT use `-fix` — reorder manually with explanatory comments. |
| `unsafe.Sizeof` / `Alignof` / `Offsetof` | Inspect struct memory layout at compile time — compare before/after reordering to quantify savings. |
| `go vet ./...` | Suspicious constructs: printf format mismatches, unreachable code, unused results, suspicious shifts. |
| `staticcheck ./...` | Advanced linter: performance pitfalls (`SA9003` empty branch, `SA4006` unused value, `SA1019` deprecated API). |
| `go test -race ./...` | Data race detection at runtime — also useful for confirming false sharing. |

## Third-Party Profilers

| Tool | What it adds | When to use |
| --- | --- | --- |
| **fgprof** (`github.com/felixge/fgprof`) | Full goroutine profiler — captures both on-CPU and off-CPU (I/O wait) time in a single profile. Standard pprof CPU profiles only show on-CPU time. | pprof CPU profile shows low CPU% but latency is high. |
| **Pyroscope / Parca** | Continuous profiling platforms — aggregate pprof profiles over time, compare across deployments, detect regressions. | Production performance monitoring and historical trend analysis (see [observability.md](./observability.md)). |
| **Linux perf** (`perf record -g ./app && perf report`) | Hardware performance counters: cache misses, branch mispredictions, TLB misses. Requires `perf_data_converter` for pprof format. | CPU microarchitecture-level analysis when pprof isn't granular enough. |
