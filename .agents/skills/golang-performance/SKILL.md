---
name: golang-performance
description: "Golang debugging (dlv, -race, GODEBUG), pprof and trace profiling, testing.B and benchstat measurement, and CPU, allocation, memory, and GC optimization. Use when diagnosing Go bugs, measuring benchmarks, or optimizing hot paths."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness. Requires go compiler, benchstat, and dlv.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "⚡"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
        - benchstat
        - dlv
    install:
      - kind: go
        package: golang.org/x/perf/cmd/benchstat@latest
        bins: [benchstat]
      - kind: go
        package: github.com/go-delve/delve/cmd/dlv@latest
        bins: [dlv]
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Bash(benchstat:*) Bash(dlv:*) Agent WebFetch WebSearch AskUserQuestion EnterWorktree ExitWorktree
paths:
  - "**/*.go"
---

**Persona:** You are a Go systems debugger and performance engineer. You never fix symptoms or optimize on intuition — reproduce first, measure with rigor, change one thing at a time, and verify with evidence.

**Thinking mode:** Reason as thoroughly as possible for root-cause analysis, profile interpretation, and optimization trade-offs — shallow reasoning treats symptoms and misidentifies bottlenecks. On Claude Code, use `ultrathink` to trigger extended thinking explicitly.

**Orchestration mode:** Single-issue debugging, benchmark measurement, and iterative optimization stay sequential (one hypothesis at a time; benchmarks must run serially). Fan out parallel sub-agents only for broad codebase bug hunts (up to 5 bug-category agents) or architectural performance reviews (up to 3 concern agents: memory/layout, I/O/concurrency, algorithms/caching). On Claude Code, use `ultracode` to opt into multi-agent orchestration explicitly.

**Modes:**

- **Debug** — single-issue root-cause investigation (sequential Golden Rules) or codebase-wide bug hunt (up to 5 parallel category agents: nil/interface, resources, errors, races, context/slice/map).
- **Benchmark & Profile** — write `testing.B` benchmarks, capture `pprof`/execution `trace`/compiler diagnostics, compare before/after with `benchstat`, and wire CI regression gates.
- **Optimize & Review** — architectural package review (up to 3 parallel concern agents), focused hot-path review (sequential), or iterative bottleneck optimization (one verified change at a time).

**Dependencies:**

- benchstat: `go install golang.org/x/perf/cmd/benchstat@latest`
- dlv: `go install github.com/go-delve/delve/cmd/dlv@latest`

# Go Debugging, Benchmarking, Profiling & Performance

Follow one unified loop: **Diagnose → Measure → Profile → Optimize**. Intuition about Go bugs and bottlenecks is wrong ~80% of the time; never apply a fix or optimization you cannot verify with a failing test, profile, or statistically significant `benchstat` delta.

## Quick Symptom Triage

Rule out external bottlenecks first (`fgprof` for off-CPU/I/O wait, goroutine profile for `net.(*conn).Read`/`database/sql` waits, OpenTelemetry spans for slow upstreams) — if 90% of latency is a DB query or remote API, Go micro-optimizations will not help.

| Symptom | First Action / Tool | Reference |
| --- | --- | --- |
| Build / module / CGO error | `go build ./... 2>&1`, `go vet ./...` | [Methodology](references/methodology.md#compilation-and-module-issues) |
| Wrong output / logic bug | Write failing test; inspect nil/errors/shadowing | [Common Go Bugs](references/common-go-bugs.md), [Testing Debug](references/testing-debug.md) |
| Random crashes / panics / races | `GOTRACEBACK=all ./app`, `go test -race ./...` | [Common Go Bugs](references/common-go-bugs.md), [Diagnostic Tools](references/diagnostic-tools.md) |
| Hang / deadlock / goroutine leak | `/debug/pprof/goroutine?debug=2`, `goroutineleak` (Go 1.27+) | [Concurrency Debug](references/concurrency-debug.md), [pprof Reference](references/pprof.md) |
| High CPU / slow hot loop | `go test -cpuprofile` or 30s `pprof/profile` | [pprof Reference](references/pprof.md), [CPU Optimization](references/cpu.md) |
| High alloc rate / GC pressure | `pprof -alloc_objects`, `GODEBUG=gctrace=1` | [Memory Optimization](references/memory.md), [Runtime Tuning](references/runtime.md) |
| Memory growing over time (leak) | `pprof -base heap1.prof heap2.prof` (`inuse_space`) | [pprof Reference](references/pprof.md#comparing-profiles), [Observability](references/observability.md) |
| High latency, low CPU (p99 spikes) | `pprof/mutex`, `pprof/block`, `fgprof`, `go tool trace` | [Trace Reference](references/trace.md), [I/O & Networking](references/io-networking.md) |
| Live production incident | Capture profiles BEFORE restarting; check Prometheus | [Production Debug](references/production-debug.md), [Observability](references/observability.md) |

## 1. Root-Cause Troubleshooting (Diagnose)

**NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST.** Escalate tools incrementally: failing test and targeted logs (`fmt.Println` locally, `slog` in production) → `go test -race` / `GOTRACEBACK=all` → `pprof` / `GODEBUG` → `dlv` debugger.

1. **Read the full error** — file/line, concrete vs interface mismatch, undefined symbols, build tags.
2. **Reproduce deterministically** — write a minimal failing test first; use `git bisect` to isolate regressions and `go test -count=10 -shuffle=on` for flaky tests.
3. **Research callers, not just the diff** — trace data flow backward to where invalid state originates; check upstream validation and constructors before flagging or patching a downstream consumer.
4. **One hypothesis at a time** — change one thing, observe, and revert failed experiments instead of stacking patches.
5. **Fix at the source** — never mask a defect with a defensive band-aid when the invariant was violated upstream.

```bash
go test -race -count=1 -v -run TestSuspect ./pkg/...   # reproduce & detect data races
GOTRACEBACK=all ./app                                  # full stacks of all goroutines on panic
GODEBUG=gctrace=1,schedtrace=1000 ./app                # live GC & scheduler diagnostics
dlv test ./pkg/suspect                                 # interactive breakpoints & goroutine inspection
```

**Red Flags (stop and return to Step 1):** "quick fix for now", multiple simultaneous edits, proposing a fix you cannot explain, each fix exposing a new failure, >=3 failed attempts on the same issue (re-evaluate the mental model), or blaming stdlib/compiler before proving it.

## 2. Benchmarking & Statistical Measurement (Measure)

Keep benchmarks in `<source>_bench_test.go` (e.g., `parser.go` → `parser_bench_test.go`), ordering `Benchmark*` functions to mirror the source file so `Test*` noise and correctness fixtures stay separate.

For **Go 1.24+**, prefer `b.Loop()` — it excludes setup automatically and prevents dead-code elimination without manual sinks or `b.ResetTimer()` (Go 1.26 also lifts earlier `b.Loop()` inlining limits). Note: **Go 1.27's** size-specialized allocator changes sub-80-byte baselines, so never compare `benchstat` runs across the Go 1.26→1.27 boundary; rerun the baseline on the same toolchain.

```go
func BenchmarkEncode(b *testing.B) {
    b.ReportAllocs() // or pass -benchmem
    for _, size := range []int{64, 256, 4096} {
        b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
            data := make([]byte, size) // setup excluded from timing
            var totalBytes int64
            for b.Loop() {
                Encode(data)
                totalBytes += int64(len(data))
            }
            b.ReportMetric(float64(totalBytes)/b.Elapsed().Seconds(), "bytes/s")
        })
    }
}
```

Legacy `for i := 0; i < b.N; i++` loops remain valid for Go <1.24 (use `b.ResetTimer()` after setup and assign results to a sink to prevent dead-code elimination). Run with statistical rigor (`-count=10`, `-benchmem`, `-benchtime=3s`, `-cpu=1,2,4`) and compare with `benchstat`:

```bash
go test -bench=BenchmarkEncode -benchmem -count=10 ./pkg/parser | tee /tmp/report-1.txt
# Output: BenchmarkEncode/size=64-8  5000000  230.5 ns/op  128 B/op  2 allocs/op
# (-8 = GOMAXPROCS, ns/op = latency, B/op = heap bytes/op, allocs/op = heap allocs/op)

# Apply ONE optimization, rerun to /tmp/report-2.txt, then verify statistical significance:
benchstat /tmp/report-1.txt /tmp/report-2.txt
```

- **Parallel variants, serial measurement:** When competing optimization hypotheses exist, implement each variant in an isolated git worktree via a separate sub-agent, then **run benchmarks serially, one at a time** — concurrent runs share CPU and contaminate `ns/op`. Compare every variant against the same baseline, keep the winner, and clean up losing worktrees.
- **Document in commits:** Use `perf(scope): summary`, include the `goos/goarch/cpu` line and affected `benchstat` rows only, and never claim an improvement marked `~` (not statistically significant):

```text
perf(parser): reduce Parse allocations 50% with sync.Pool

goos: linux / goarch: amd64 / cpu: AMD Ryzen 9 5950X
          │    old     │              new               │
Parse-32    4.592µ ± 2%  3.041µ ± 1%  -33.78% (p=0.000 n=10)
Parse-32   1.024Ki ± 0%  0.512Ki ± 0% -50.00% (p=0.000 n=10)
Parse-32    12.00 ± 0%    6.000 ± 0%  -50.00% (p=0.000 n=10)
```

- **CI regression gating:** Automate PR checks with `benchdiff`, `cob`, or `gobenchdata` (see [CI Regression Detection](references/ci-regression.md)).

## 3. Profiling & Compiler Analysis (Profile)

Capture profiles from benchmarks (`-cpuprofile=cpu.prof`, `-memprofile=mem.prof`, `-trace=trace.out`) or from authenticated `/debug/pprof/*` endpoints (toggled via `PPROF_ENABLED=true`, protected with Basic Auth — never expose publicly).

```bash
# Capture from benchmarks (no HTTP server required)
go test -bench=BenchmarkEncode -cpuprofile=cpu.prof -memprofile=mem.prof ./pkg/parser
go test -bench=BenchmarkEncode -trace=trace.out ./pkg/parser && go tool trace trace.out

# Capture from a live service (ensure server write timeout > 30s)
curl -u "$PPROF_USER:$PPROF_PASS" "http://localhost:6060/debug/pprof/profile?seconds=30" > cpu.prof
curl -u "$PPROF_USER:$PPROF_PASS" "http://localhost:6060/debug/pprof/goroutineleak?debug=2" > leaks.txt

# Analyze profiles & compiler decisions
go tool pprof -top cpu.prof                       # on-CPU hot spots (flat vs cum)
go tool pprof -alloc_objects mem.prof             # GC churn (count of allocations)
go tool pprof -base heap1.prof heap2.prof         # live heap growth (memory leaks)
go tool pprof -http=:8080 cpu.prof                # interactive flamegraph UI
go build -gcflags="-m -m" ./pkg/parser            # escape analysis & inlining decisions
```

- **Profile selection:** Use `alloc_objects` for GC churn, `alloc_space` for peak memory volume, `inuse_space` (`-base`) for leaks, `goroutine` / `goroutineleak` (Go 1.27+) for leaks and hangs, `mutex`/`block` (after enabling `runtime.SetMutexProfileFraction` / `runtime.SetBlockProfileRate`) for lock contention, and `go tool trace` or `runtime/trace.FlightRecorder` when latency is high despite low CPU.
- **Compiler diagnostics:** Check `-gcflags="-m"` for unexpected heap escapes and inlining misses, `-gcflags="-d=ssa/check_bce/debug=1"` for bounds-check elimination, and `fieldalignment ./...` (without `-fix`) for struct padding waste.

### Production Capture Hygiene

- Capture goroutine, heap, CPU, and mutex profiles **before** restarting an unhealthy instance.
- Gate costly diagnostics (`PPROF_ENABLED`, `PYROSCOPE_ENABLED`, `LOG_LEVEL=debug`) behind environment variables on a single target instance.
- Keep execution traces short (5–10s) and prefer `prometheus/client_golang`'s `runtime/metrics`-based `collectors.NewGoCollector()` over `ReadMemStats` to avoid stop-the-world pauses.
- Correlate Go runtime metrics with host metrics (`node_exporter`) and non-Go memory (`process_resident_memory_bytes - go_memstats_sys_bytes`) before assuming a Go heap leak.

## 4. Optimization Patterns (Optimize)

Execute sequentially: **Define metric → Baseline (`/tmp/report-1.txt`) → Diagnose → Apply ONE optimization → Compare (`benchstat`) → Commit → Repeat**. Keep `/tmp/report-*.txt` as an audit trail, add inline comments explaining *why* non-obvious optimizations are faster (with benchmark numbers so future readers do not revert them), and prioritize allocation reduction — it usually delivers the highest ROI in Go.

| Bottleneck Signal | Optimization Pattern | Reference |
| --- | --- | --- |
| High `alloc_objects` / `runtime.mallocgc` | Preallocate slices/maps, `strings.Builder`, stack values, `sync.Pool` (<=32KB, reset before `Put`), zero-copy `[]byte` | [Memory Optimization](references/memory.md) |
| High `runtime.scanobject` / struct bloat | Value slices over pointer slices, flatten structs, reorder fields largest-to-smallest | [Memory Optimization](references/memory.md#memory-layout) |
| High CPU in tight loop / `reflect` | Enable inlining, direct loops over iterator chains, cache-friendly AoS/SoA, cache-line padding against false sharing, avoid reflection | [CPU Optimization](references/cpu.md) |
| High GC CPU% / OOM in containers | Reduce allocation rate first; set `GOMEMLIMIT` to 80–90% of container limit, tune `GOGC`, validate `GOMAXPROCS`, enable PGO | [Runtime Tuning](references/runtime.md) |
| High I/O / HTTP / JSON / DB latency | Custom `http.Transport` (`MaxIdleConnsPerHost`), `bufio` streaming, faster JSON/buffers, batch DB/HTTP ops, minimize cgo crossings | [I/O & Networking](references/io-networking.md) |
| Repeated work / `O(n²)` loops | Hoist compiled regex/templates, `singleflight` (or `go-singleflightx`), contiguous LRU (`go-freelru`), replace nested scans with maps | [Caching Patterns](references/caching.md) |
| Hot `mutex`/`block` profile | Shrink critical sections, shard locks, `sync.RWMutex` for read-heavy paths, `sync/atomic` counters | [Concurrency Debug](references/concurrency-debug.md), [pprof Reference](references/pprof.md) |

### Common Mistakes to Avoid

| Mistake | Fix |
| --- | --- |
| Optimizing without profiling | Profile first — intuition misses the real hot spot ~80% of the time |
| Default `http.Client` / `http.Transport` | Set explicit timeouts and raise `MaxIdleConnsPerHost` (default is 2) |
| Logging in hot loops | Prevents inlining and allocates even when disabled; use `slog.LogAttrs` outside tight loops |
| `panic`/`recover` as control flow | Allocates a stack trace and unwinds frames; return `error` values |
| Unproven `unsafe` tricks | Require benchmark proof (>10% gain in a verified hot path) and document invariants |
| Unconfigured GC in containers | Set `GOMEMLIMIT` to 80–90% of container RAM to prevent OOM kills |
| `reflect.DeepEqual` in hot paths | 50–200x slower than typed comparisons (`slices.Equal`, `maps.Equal`, `bytes.Equal`) |

## Reference Guides

**Diagnose (Root-Cause Debugging):**

- [General Debugging Methodology](references/methodology.md) — 10-step process, escalation protocol, compilation/module/CGO fixes, and code-review red flags
- [Common Go Bugs](references/common-go-bugs.md) — nil/interface gotchas, shadowing, slice/map/defer/context/JSON pitfalls, and fixes
- [Concurrency Debugging](references/concurrency-debug.md) — `-race`, deadlocks, goroutine leaks, `goleak`, and Go 1.27 `goroutineleak`
- [Test-Driven Debugging](references/testing-debug.md) — reproducing bugs in tests, table-driven edge cases, test flags, and flaky test diagnosis
- [Production Debugging](references/production-debug.md) — incident capture checklist, structured `slog` tracing, and HTTP/network debugging

**Measure & Profile:**

- [benchstat Reference](references/benchstat.md) — statistical comparison, p-values, confidence intervals, filtering, and interleaving
- [CI Regression Detection](references/ci-regression.md) — `benchdiff`, `cob`, `gobenchdata`, noisy-neighbor mitigation, and runner tuning
- [pprof Reference](references/pprof.md) — secure HTTP setup, profile types/capture, interactive CLI, web UI, `-base` diffs, and pattern recognition
- [Trace Reference](references/trace.md) — `go tool trace`, scheduler/GC timelines, custom regions, and `FlightRecorder`
- [Compiler Analysis](references/compiler-analysis.md) — escape analysis, inlining budgets, bounds-check elimination, SSA dumps, and assembly
- [Diagnostic Tools](references/diagnostic-tools.md) — `GODEBUG`, `GOTRACEBACK`, `go doc`, `dlv` debugger, runtime APIs, `fieldalignment`, `fgprof`, and `perf`

**Optimize & Observe:**

- [Memory Optimization](references/memory.md) — allocation reduction, backing-array leaks, `sync.Pool`, and struct field alignment
- [CPU Optimization](references/cpu.md) — inlining, cache locality, false sharing, ILP, and reflection avoidance
- [I/O & Networking](references/io-networking.md) — HTTP transport pooling, buffered streaming, JSON optimization, cgo overhead, and batching
- [Runtime Tuning](references/runtime.md) — `GOGC`, `GOMEMLIMIT`, GC pacer diagnostics, container `GOMAXPROCS`, and PGO
- [Caching Patterns](references/caching.md) — `singleflight`, contiguous LRU caches, precompiled patterns, and algorithmic complexity
- [Production Observability](references/observability.md) — investigation sessions, PromQL diagnostics, continuous profiling, cost warnings, and [Prometheus Alerting Rules](assets/prometheus-alerts.yml)
- [Prometheus Go Metrics Reference](references/prometheus-go-metrics.md) — exhaustive default/optional `client_golang` and `runtime/metrics` catalog
