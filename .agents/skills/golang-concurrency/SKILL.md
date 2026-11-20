---
name: golang-concurrency
description: "Golang concurrency and context.Context design — goroutines, channels vs mutexes, errgroup, singleflight, worker pools, cancellation, timeouts, and WithoutCancel. Use when bounding goroutine lifecycles, propagating ctx, or avoiding leaks."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "⚡"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
    install: []
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Agent AskUserQuestion
paths:
  - "**/*.go"
---

**Persona:** You are a Go concurrency engineer. You assume every goroutine is a liability until proven necessary — correctness, cancellation propagation, and leak-freedom come before performance.

**Orchestration mode:** Fan out the five sub-agents described in the "Parallelizing Concurrency Audits" section for auditing concurrent code and context propagation across a large codebase, and consolidate their findings into one report. On Claude Code, use `ultracode` to opt into multi-agent orchestration explicitly.

**Modes:**

- **Write mode** — implement concurrent code (goroutines, channels, sync primitives, worker pools, pipelines) and `context.Context` propagation. Follow the sequential instructions below.
- **Review mode** — review a PR's concurrent code and context changes. Focus on the diff: check for goroutine leaks, broken context chains, leaked cancel functions, ownership violations, and unprotected shared state. Sequential.
- **Audit mode** — audit existing concurrent code and context usage across a codebase. Use up to 5 parallel sub-agents as described in "Parallelizing Concurrency Audits".

# Go Concurrency and Context Best Practices

Goroutines are cheap, not free — every goroutine you spawn is a resource you must own and shut down. `context.Context` is the backbone of structured concurrency in Go: it carries cancellation signals, deadlines, and request-scoped metadata across goroutines and API boundaries so every unit of work has a predictable exit and clean error propagation.

## Core Concurrency Principles

1. **Every goroutine must have a clear exit** — without a shutdown mechanism (`context.Context`, done channel, or `WaitGroup`), goroutines leak until the process runs out of memory.
2. **Recover panics at goroutine boundaries** — an unrecovered panic in a spawned goroutine crashes the entire process; recover and log at the boundary in production code.
3. **Share memory by communicating** — channels make ownership transfer explicit; mutexes protect shared state but leave ownership implicit.
4. **Send copies, not pointers, on channels** — sending pointers creates invisible shared memory and reintroduces data races.
5. **Only the sender closes a channel** — closing from the receiver panics if the sender writes after close; use a separate stop signal (`ctx` or done channel) to tell producers to stop.
6. **Specify channel direction in signatures** (`chan<-`, `<-chan`) — let the compiler enforce send/receive roles at build time.
7. **Default to unbuffered channels** — arbitrary large buffers mask backpressure and hide slow consumers; size buffers only with measured justification.
8. **Always include `<-ctx.Done()` in `select`** — both sends and receives on channels must abort promptly when the caller cancels.
9. **Avoid repeated `time.After` in hot loops** — reuse `time.NewTimer` with `Stop()` + `Reset()` to avoid per-iteration timer allocations.
10. **Prefer Go 1.23+ iterators (`iter.Seq`) for sequential CPU-bound transforms** — reserve goroutine pipelines for stages involving I/O or genuine parallelism.

## Channel vs Mutex vs Atomic

| Scenario | Use | Why |
| --- | --- | --- |
| Passing data between goroutines | Channel | Communicates explicit ownership transfer |
| Coordinating goroutine lifecycle | Channel + `context.Context` | Clean shutdown with `select` |
| Protecting shared struct fields | `sync.Mutex` / `sync.RWMutex` | Simple critical sections; never hold across I/O |
| Simple counters, flags | `sync/atomic` | Lock-free typed atomics (`atomic.Int64`, `atomic.Bool`, `atomic.Pointer[T]`) |
| Write-once/read-many or disjoint-key map | `sync.Map` | Optimized for read-heavy access; **unprotected concurrent map read/write crashes hard** |
| Write-heavy overlapping-key map | `sync.RWMutex` + `map` | Faster than `sync.Map` under frequent overlapping writes |
| Caching / deduplicating in-flight work | `sync.Once` / `singleflight` | Execute once or collapse thundering herds |

## WaitGroup vs errgroup

| Need | Use | Why |
| --- | --- | --- |
| Wait for goroutines, no errors | `sync.WaitGroup` | Go 1.25+: prefer `wg.Go(fn)` for non-panicking fire-and-wait; Go <1.25: call `wg.Add(n)` before `go` |
| Wait + collect first error | `errgroup.Group` | Structured error propagation |
| Wait + cancel siblings on first error | `errgroup.WithContext` | Cancels derived `ctx` on first error |
| Wait + bound concurrency | `errgroup.SetLimit(n)` | Replaces hand-rolled semaphore/worker pools |

## Sync Primitives Quick Reference

| Primitive | Key Rule |
| --- | --- |
| `sync.Mutex` | Embed unexported directly above guarded fields; keep critical sections short and I/O-free |
| `sync.RWMutex` | Never upgrade `RLock` to `Lock` (deadlocks); release `RLock` first |
| `sync.Pool` | Always `Reset()` before `Put()` via `defer`; never assume `Get()` returns a clean object or persists across GC |
| `sync.Once` | Prefer Go 1.21+ `sync.OnceFunc`, `sync.OnceValue`, `sync.OnceValues` for lazy initialization |
| `x/sync/singleflight` | Use `group.Do(key, fn)` to deduplicate concurrent expensive lookups (cache stampede protection) |
| `x/sync/errgroup` | Combine `WithContext` + `SetLimit(n)` for bounded concurrent I/O with cancellation |

## `context.Context` Rules

1. **Propagate the same context through the entire call chain** (HTTP handler → service → DB → external API) — creating `context.Background()` mid-request breaks cancellation and deadlines.
2. **Pass `ctx context.Context` as the first parameter** — fixed position makes context-aware APIs recognizable and verifiable by linters (`govet`, `staticcheck`).
3. **Never store `context.Context` in a struct** — structs outlive individual requests and cause stale or already-cancelled contexts to be reused.
4. **Never pass a `nil` context** — use `context.TODO()` as an explicit migration placeholder when a caller does not provide one yet.
5. **Call `cancel()` on all control-flow paths** for `WithCancel`, `WithTimeout`, and `WithDeadline` (typically `defer cancel()` immediately) unless ownership is explicitly transferred — uncalled cancels leak timers and parent attachments.
6. **Respect nested deadline semantics** — a child timeout never extends a shorter parent deadline; the shorter deadline always wins.
7. **Use `context.Background()` only at top-level entry points** (`main`, `init`, tests) and `r.Context()` inside HTTP handlers.
8. **Use context-aware I/O APIs everywhere** — `http.NewRequestWithContext`, `db.BeginTx(ctx, nil)`, `QueryContext`, `QueryRowContext`, and `ExecContext`.
9. **Key context values with unexported types** (`type contextKey string` or `type key struct{}`) and wrap with typed getter/setter helpers — plain string keys collide across packages.
10. **Carry only request-scoped metadata in context values** (trace ID, request ID, authenticated user/tenant) — pass DB handles, loggers (unless request-enriched), configs, and business parameters explicitly.
11. **Use `context.WithoutCancel(ctx)` (Go 1.21+)** for background work that must outlive the parent request (e.g., async audit logs) while preserving trace/request values.
12. **Use `context.AfterFunc(ctx, fn)` (Go 1.21+)** to register non-blocking cancellation cleanup callbacks, capturing the returned `stop` function alongside a `defer` safety net.

### Context Propagation Example

Propagating the caller's context ensures upstream cancellations and deadlines automatically abort downstream DB and HTTP work:

```go
// ✗ Bad — breaks the cancellation chain mid-request
func (s *OrderService) Create(ctx context.Context, order Order) error {
    return s.db.ExecContext(context.Background(), "INSERT INTO orders ...", order.ID)
}

// ✓ Good — propagates caller context end-to-end
func (s *OrderService) Create(ctx context.Context, order Order) error {
    return s.db.ExecContext(ctx, "INSERT INTO orders ...", order.ID)
}
```

### Creating and Deriving Contexts

| Situation | Use |
| --- | --- |
| Entry point (`main`, `init`, test) | `context.Background()` |
| Temporary migration placeholder | `context.TODO()` |
| Inside an HTTP handler | `r.Context()` |
| Manual cancellation control | `context.WithCancel(parentCtx)` |
| Relative timeout / absolute SLA | `context.WithTimeout` / `context.WithDeadline` |
| Background work outliving a request | `context.WithoutCancel(ctx)` (+ optional `WithTimeout`) |

## Concurrency & Context Checklist

Before spawning a goroutine or adding a context boundary, verify:

- [ ] **How will it exit?** — `ctx.Done()`, channel close, or explicit shutdown signal
- [ ] **Can callers wait for it?** — `sync.WaitGroup` or `errgroup.Group`
- [ ] **Are panics recovered?** — `defer recover()` at goroutine entry points in production code
- [ ] **Who owns and closes each channel?** — creator/sender only, typed with `chan<-` / `<-chan`
- [ ] **Does `ctx` flow uninterrupted?** — no mid-chain `context.Background()`, no `nil`, every `cancel()` deferred, all DB/HTTP calls use `*Context`/`BeginTx`
- [ ] **Is concurrency actually needed?** — prefer synchronous code or `iter.Seq` for sequential CPU work

## Parallelizing Concurrency Audits

When auditing concurrency and context usage across a large codebase, fan out up to 5 parallel sub-agents:

1. Find all goroutine spawns (`go func`, `go method`) and verify exit mechanisms, panic recovery, and `WaitGroup`/`errgroup` synchronization.
2. Search for mutable globals and shared state lacking mutex, atomic, or channel protection.
3. Audit channel usage — ownership, directional signatures, closing side, and unjustified buffer sizes.
4. Audit `context.Context` chains — mid-request `context.Background()`, struct-stored contexts, uncalled `cancel()`, non-context DB/HTTP calls (`db.Begin`, `http.NewRequest`), `time.After` in loops, and missing `ctx.Done()` in `select`.
5. Check mutex discipline (`RLock` upgrades, locks held across I/O), `sync.Map` misuse, `sync.Pool` resets, and atomics.

## Common Mistakes

| Mistake | Fix |
| --- | --- |
| Fire-and-forget goroutine / unrecovered panic | Propagate `ctx`, wait via `errgroup`/`WaitGroup`, recover panics at boundary |
| Closing channel from receiver | Signal producer via `ctx` or done channel; sender closes |
| `time.After` in hot `select` loop | Reuse `time.NewTimer` + `Stop()` + `Reset()` |
| Missing `ctx.Done()` in `select` | Include `case <-ctx.Done():` on both sends and receives |
| Unbounded goroutine spawning | Bound with `errgroup.SetLimit(n)` or a semaphore |
| Sending pointers on channels | Send value copies or document immutability |
| `wg.Add` inside goroutine | Call `wg.Add(len(items))` before `go` (or `wg.Go` on Go 1.25+) |
| `context.Background()` inside handler/service | Propagate `r.Context()` / caller `ctx` (or `WithoutCancel` for detached work) |
| Discarding `cancel` (`ctx, _ := context.WithTimeout(...)`) | Always `defer cancel()` immediately (or cancel per retry iteration) |
| `db.Begin()` or `http.NewRequest()` with a `ctx` in scope | Use `db.BeginTx(ctx, nil)` and `http.NewRequestWithContext` |
| Plain string context keys or DB/config in `context.Value` | Use unexported key types for request-scoped metadata only |
| Mutex held across I/O or missing `-race` in CI | Keep critical sections short; always run `go test -race ./...` |

## Leak Detection

Combine automated test checks with runtime diagnostics:

- **Tests:** `goleak.VerifyTestMain(m)` from `go.uber.org/goleak` and `go test -race ./...`
- **Runtime counts & stacks:** `runtime.NumGoroutine()` and `/debug/pprof/goroutine?debug=2`
- **Goroutine leak profile** (experimental behind `GOEXPERIMENT=goroutineleakprofile` in Go 1.26, generally available in `runtime/pprof` since Go 1.27):

```bash
curl http://localhost:6060/debug/pprof/goroutineleak?debug=2
go tool pprof http://localhost:6060/debug/pprof/goroutineleak
```

## Deep Dives

- [Channels and Select Patterns](references/channels-and-select.md) — goroutine lifecycles, panic recovery, directional signatures, closing ownership, buffer sizing, non-blocking `select`, and timer reuse.
- [Pipelines and Worker Pools](references/pipelines.md) — multi-stage pipelines, fan-out/fan-in, `errgroup.SetLimit` worker pools, semaphores, Go 1.23+ iterators, `samber/ro`, and `goleak`.
- [Sync Primitives Deep Dive](references/sync-primitives.md) — `Mutex`, `RWMutex`, typed `sync/atomic`, `sync.Map`, `sync.Pool`, `sync.Once`/`OnceValue`, `WaitGroup.Go` (Go 1.25+), `singleflight`, and `errgroup`.
- [Cancellation, Timeouts & Deadlines](references/context-cancellation.md) — `WithCancel`, `WithTimeout`, `WithDeadline`, nested deadline precedence, loop/select cancellation checks, `AfterFunc`, and `WithoutCancel`.
- [Context in HTTP, Databases & Tracing](references/context-http-values.md) — `r.Context()`, context-aware HTTP/DB calls (`NewRequestWithContext`, `BeginTx`, `*Context`), unexported value keys, what belongs in context vs parameters, and cross-service trace propagation.
