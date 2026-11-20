---
name: golang-idioms
description: "Golang idioms for structs, consumer interfaces, receivers, slices, maps, generics, pointers, %w and errors.Is/As errors, and typed-nil, aliasing, or overflow safety. Use when designing Go types, collections, error chains, or safe zero values."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "🧱"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
    install: []
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Agent AskUserQuestion
paths:
  - "**/*.go"
---

**Persona:** You are a senior Go engineer focused on type design, data structure internals, reliable error handling, and defensive safety. You write clear, zero-value-friendly Go that prevents panics, aliasing bugs, and silent failures by construction.

# Go Core Idioms & Defensive Safety

Core Go idioms across type design, collections, error handling, and panic prevention.

## 1. Structs, Consumer Interfaces & Receivers

### Interface Design Principles

- **Keep interfaces small (1–3 methods)** and compose larger contracts (`type ReadWriter interface { io.Reader; io.Writer }`).
- **Define interfaces where consumed**, not where implemented — the consumer controls its contract and avoids importing the implementor package.
- **Accept interfaces, return concrete structs** — constructors give callers full access to fields/methods; never return interfaces from constructors.
- **Don't create interfaces prematurely** — "Don't design with interfaces, discover them." Start concrete; extract an interface when a second implementation, second consumer, or deliberate test mock demands it.
- **Honor canonical stdlib signatures** (`io.Reader`, `io.Writer`, `io.Closer`, `fmt.Stringer`, `error`, `http.Handler`, `json.Marshaler`/`Unmarshaler`) — implement `String() string`, never `ToString()`.
- **Verify satisfaction at compile time** near the type definition: `var _ io.ReadWriter = (*MyBuffer)(nil)`.

```go
// package notification — consumer defines only what it needs; constructor returns concrete type
type Sender interface {
    Send(to, body string) error
}

type Service struct {
    sender Sender
}

func NewService(sender Sender) *Service { return &Service{sender: sender} }
```

| Stdlib Interface | Package | Canonical Method |
| --- | --- | --- |
| `Reader` / `Writer` / `Closer` | `io` | `Read([]byte) (int, error)` / `Write([]byte) (int, error)` / `Close() error` |
| `Stringer` | `fmt` | `String() string` |
| `Handler` | `net/http` | `ServeHTTP(ResponseWriter, *Request)` |
| `Marshaler` / `Unmarshaler` | `encoding/json` | `MarshalJSON() ([]byte, error)` / `UnmarshalJSON([]byte) error` |

### Embedding, Receivers, Tags & Copy Safety

- **Embed vs named field:** Embed only when promoting the full inner API ("is a", e.g., embedding `http.Handler`). Use a named field for internal dependencies ("has a", e.g., `store *DataStore`). Promoted methods receive the _inner_ type, not the outer.
- **Receiver consistency:** Receiver types MUST be consistent across all methods of a type — if any method uses a pointer receiver, all should (method sets differ between `T` and `*T` for interface satisfaction). Use pointer `(s *Server)` when mutating, holding a `sync.Mutex`, or on large structs; use value `(s Server)` only for small, immutable, or basic types.
- **Struct field tags:** Exported fields in serialized structs MUST carry explicit tags (`json:"id" db:"id"`). Prefer `omitzero` (Go 1.24+) over `omitempty` when distinguishing explicit zero values from absent fields; run `go vet ./...` (`structtag`).
- **Prevent copies of lock-bearing structs:** Embed a `noCopy` sentinel implementing `Lock()`/`Unlock()` so `go vet` (`copylocks`) flags value copies, and pass by pointer.
- **Type assertions & switches:** MUST use comma-ok (`v, ok := x.(T)`), never bare `v := x.(T)`. Use optional-behavior assertions (`if f, ok := w.(Flusher); ok`) to exploit richer capabilities without widening parameter interfaces. For reflection in Go 1.25+, prefer `reflect.TypeAssert[T](v)` over `v.Interface().(T)`.

**References:** [Struct Fields: Tags & `noCopy`](references/struct-fields.md) | [Type Assertions & Type Switches](references/type-assertions.md)

## 2. Slices, Maps, Generics & Pointer Semantics

### Slices & Maps

- **Preallocate** with `make([]T, 0, n)`, `slices.Grow(s, n)`, or `make(map[K]V, n)` when size is known or estimable. NEVER rely on a hardcoded slice capacity growth factor — growth heuristics are a runtime detail.
- **Arrays** (`[32]byte`, `[2]int`) are fixed-size value types; prefer them for compile-time-known sizes and zero-allocation comparable map keys (along with all-comparable structs like `struct{ Method, Path string }`).
- **Maps never shrink** on `delete()` — the bucket array stays allocated even after `runtime.GC()`. Reclaim memory only by rebuilding into a fresh `make` map and copying surviving entries.
- **Map value sizing:** Use value maps (`map[K]V`) for small structs (`< 128` bytes) to reduce GC pressure, and pointer maps (`map[K]*V`) for large structs to avoid heavy per-access copying.
- **Stdlib helpers (Go 1.21+):** Use `slices` (`Sort`, `BinarySearch`, `Clone`, `Equal`, `DeleteFunc`, `Clip`, `Grow`, `Compact`) and `maps` (`Clone`, `Equal`, `Keys`, `Values`, `Collect`, `Insert`). After `slices.DeleteFunc`, chain `slices.Clip` to release dead references in excess capacity.

### Containers, Buffers, Generics & Pointers

- **Containers & buffers:** Prefer slices over `container/list` unless O(1) arbitrary-position insert/delete via `*list.Element` (e.g., LRU, ordered map) is needed. Use `container/heap` (`heap.Init`, `heap.Fix`, `heap.Pop`) for priority queues, `container/ring` for fixed-size circular buffers/round-robin, `strings.Builder` (with `Grow`) for string assembly, `bytes.Buffer` for bidirectional I/O, and `bufio.Scanner` with `scanner.Buffer()` for lines exceeding the 64 KB default limit.
- **Prefer generics over `any`** for type-safe containers and algorithms (Go 1.18+). Use the tightest constraint: `comparable` for map keys/`==`, `cmp.Ordered` for sorting/binary search, custom `~T` unions for domain math. Do NOT add a type parameter when `[T any]` adds no type safety (e.g., wrapping `json.Marshal`, which already accepts `any`).
- **Pointer types:** Prefer `&T{}` over `new(T)`. Restrict `unsafe.Pointer` strictly to the 6 valid Go spec patterns — NEVER store a `uintptr` across statements; prefer modern `unsafe.Add`, `unsafe.Slice`, and `unsafe.String`. Use `weak.Pointer[T]` with `runtime.AddCleanup` (Go 1.24+, preferred over `runtime.SetFinalizer`) for GC-safe caches and interning tables.

### Copy Semantics Quick Reference

| Type | Assignment Behavior | Independence |
| --- | --- | --- |
| `int`, `float`, `bool`, `string`, `array`, `struct` | Value (full copy) | Fully independent |
| `slice` | 3-word header copied; backing array shared | `slices.Clone` / `s[:len(s):len(s)]` |
| `map`, `channel` | Reference copied | `maps.Clone` (maps) / same channel |
| `*T`, `interface` | Address or `(type, value)` pair copied | Depends on pointed/held type |

**References:** [Slices: Internals & Safety](references/slices.md) | [Maps: Internals & Safety](references/maps.md) | [Containers, `bufio` & Builders](references/containers.md) | [Generic Collections](references/generics.md) | [Pointer Types (`*T`, `unsafe`, `weak`)](references/pointers.md)

## 3. Idiomatic Error Handling

### Core Rules

1. **Always check returned errors** — NEVER discard with `_`.
2. **Error strings MUST be lowercase** with no trailing punctuation and no redundant layer prefixes.
3. **Wrap with `%w` internally, `%v` at system/API boundaries** — `fmt.Errorf("fetching user: %w", err)` preserves the unwrap chain across internal layers; `%v` breaks the chain at public boundaries so callers cannot couple to internal backend error types.
4. **Inspect chains with `errors.Is` and `errors.As`/`errors.AsType`**, NEVER `==` or bare type assertions. On Go 1.26+, prefer `errors.AsType[T](err)` when `T` implements `error`; use `errors.As(err, &target)` on Go <1.26 or for non-error interface targets.
5. **Sentinel vs custom error types:** Use package-level `var ErrNotFound = errors.New("not found")` for expected conditions; use custom structs implementing `Error()` (and `Unwrap() error` when wrapping) to carry structured fields.
6. **Combine independent errors with `errors.Join`** (Go 1.20+) for multi-field validation, batch processing, and graceful multi-resource shutdown.
7. **Single handling rule:** An error MUST be either logged OR returned, NEVER both — log-and-return produces duplicate noise at every call layer.
8. **Low-cardinality logging & `samber/oops`:** Keep stable grouping messages static at logging/APM boundaries and attach IDs, paths, or row numbers as structured attributes via `slog` (Go 1.21+, not `log.Printf`) or `samber/oops` (`oops.With(...)` for stack traces, codes, and public/private separation). Never expose raw technical errors to end users.
9. **Panic & recover:** Reserve `panic` strictly for unrecoverable programmer errors/impossible invariants. Recover panics with `defer`/`recover` at goroutine and HTTP handler boundaries.

```go
// Internal layer — wrap with %w
if err := s.repo.Find(ctx, id); err != nil {
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrNotFound
    }
    return nil, fmt.Errorf("finding order: %w", err)
}

// Public API boundary — inspect internally, break chain with %v
var te *BackendTimeoutError
if errors.As(err, &te) {
    return nil, fmt.Errorf("request timed out: %v", ErrTimeout)
}
```

### Modes & Parallelizing Error Audits

- **Coding mode:** Follow the rules above sequentially when writing new code.
- **Review mode:** Audit diffs for swallowed errors, bare assertions, aliasing, typed-nil returns, and log-and-return pairs.
- **Audit mode:** For large codebases, spawn up to 5 parallel sub-agents targeting independent error categories: (1) error creation & low-cardinality messages, (2) `%w` vs `%v` wrapping & `errors.Is`/`As`, (3) single-handling rule & swallowed errors, (4) panic/recover boundaries, and (5) `slog`/structured logging & PII checks.

**References:** [Error Creation & Custom Types](references/error-creation.md) | [Error Wrapping, Inspection & `errors.Join`](references/error-wrapping.md) | [Single Handling, Panic/Recover & `samber/oops`](references/error-handling.md)

## 4. Defensive Coding & Zero-Value Safety

### Nil Safety & Useful Zero Values

- **Typed-nil interface trap:** An interface is `nil` only when both its type descriptor and value are `nil`. Returning a typed nil pointer (`var err *MyError; return err`) or unconditionally assigning `err = parseErr` yields a **non-nil** interface. Always `return nil` explicitly on the success path.
- **Make the zero value useful:** Like `sync.Mutex` and `bytes.Buffer`, guard internal maps with lazy initialization (`if r.items == nil { r.items = make(...) }`) or use `sync.Once` for concurrent lazy init.
- **Nil pointers & callbacks:** Check `func` callbacks for `!= nil` before calling (or set a no-op default). Guard nil pointer receivers when nil is an intentional state; otherwise validate pointers before calling methods.
- **Channels & loop variables:** Always use `v, ok := <-ch` when reading channels that may close (a closed channel returns zero values immediately and spins `select` loops unless set to `nil`). Note that `for range` per-iteration variable scoping requires `go 1.22`+ in `go.mod`; taking `&items[i]` avoids copies and works across all Go versions.

| Nil Value | Read / Index | Write / Send | `len`/`cap` | `range` |
| --- | --- | --- | --- | --- |
| `map` | Zero value | **panic** | 0 | 0 iterations |
| `slice` | **panic** | **panic** | 0 | 0 iterations |
| `chan` | Blocks forever | Blocks forever | 0 | Blocks forever |
| `func` | — | Call **panics** | — | — |

```go
// ✗ Bad — returns interface{type: *ValidationError, value: nil} != nil
func validate(s string) error {
    var err *ValidationError
    if s == "" { err = &ValidationError{Field: "name"} }
    return err
}

// ✓ Good — returns untyped nil explicitly
func validate(s string) error {
    if s == "" { return &ValidationError{Field: "name"} }
    return nil
}
```

### Aliasing, Defensive Copies, Numerics & Loop Defers

- **Prevent `append` aliasing:** `append(a, x)` mutates `a`'s backing array if spare capacity exists. Force isolation with a full-slice expression `a[:len(a):len(a)]` or `slices.Clone(a)`.
- **Release large backing arrays & defensive copying:** Never return a raw subslice of a large buffer (`data[:32]`) — copy with `slices.Clone` or `copy` on all return branches. Keep collection struct fields unexported, clone incoming slices/maps in constructors (ingress), and return `slices.Clone`/`maps.Clone` from getters (egress).
- **Deterministic maps & safe deletion:** Map iteration is randomized — sort keys with `slices.Sorted(maps.Keys(m))` when order matters. Deleting from a map during `range` is safe; for slices, use `slices.DeleteFunc` or iterate backwards. Never access a map concurrently without synchronization.
- **Numeric safety:** Narrowing conversions (`int64` → `int32`/`uint32`, `int` → `byte`/`uint16`) silently wrap — check bounds (`< 0`, `math.MinInt32`/`math.MaxUint32`) before converting. Compare floats with `math.Abs(a-b) < epsilon`, never `==` or `reflect.DeepEqual`. Guard zero divisors for both integers (panics) and floats (`+Inf`/`-Inf`/`NaN` silently propagates), and convert to `float64` before dividing when fractional precision is required.
- **`defer` in loops:** `defer` runs at _function_ exit, not per loop iteration. Extract the loop body to a helper function so files, DB connections, and HTTP response bodies close every iteration (and check `Close()` errors on writable files).

```go
// ✓ Numeric bounds check + defensive ingress/egress + helper-scoped defer
if contentLen < 0 || contentLen > math.MaxUint32 {
    return fmt.Errorf("content length %d overflows uint32", contentLen)
}

func NewConfig(hosts []string) *Config { return &Config{hosts: slices.Clone(hosts)} }
func (c *Config) Hosts() []string      { return slices.Clone(c.hosts) }

func processFile(path string) (err error) {
    f, err := os.Open(path)
    if err != nil {
        return fmt.Errorf("opening file: %w", err)
    }
    defer func() { err = errors.Join(err, f.Close()) }()
    return parse(f)
}
```

- **Enforce with linters:** Catch safety bugs automatically in CI with `go vet`, `errcheck`, `forcetypeassert`, `nilerr`, and `staticcheck`.

**Reference:** [Nil Safety & Zero-Value API Patterns](references/nil-safety.md)

## Common Mistakes

| Mistake | Fix |
| --- | --- |
| Defining interfaces in implementor package or returning interfaces | Define 1–3 method interfaces at consumer; return concrete structs |
| Bare type assertion `v := x.(T)` or mixing pointer/value receivers | Use `v, ok := x.(T)`; keep all receivers on a type consistent |
| Returning typed nil pointer via interface (`var e *MyErr; return e`) | Return untyped `nil` explicitly (`if e != nil { return e }; return nil`) |
| Writing to nil map or exposing internal slice/map fields | Initialize/lazy-init maps; unexport fields and clone on ingress & egress |
| Appending into caller's slice with spare capacity or returning raw subslice | Use `s[:len(s):len(s)]` or `slices.Clone` to isolate backing arrays |
| Comparing errors with `==`/type assertion, or logging AND returning | Use `errors.Is`/`errors.As`/`errors.AsType`; handle each error once |
| Wrapping internal errors with `%w` across public API boundaries | Inspect with `errors.As`, translate to domain error, break chain with `%v` |
| Interpolating IDs/rows into log/APM grouping messages | Keep messages static lowercase; pass variable data via `slog`/`oops.With` |
| Narrowing ints without bounds check, float `==`, or zero-divisor omission | Bounds-check against `math.Max*`, use epsilon, guard `divisor == 0` |
| Bare `defer f.Close()` inside a `for` loop | Extract loop body to a helper function so `defer` runs each iteration |

## External References

- [Effective Go](https://go.dev/doc/effective_go) · [Go Data Structures (Russ Cox)](https://research.swtch.com/godata) · [The Go Memory Model](https://go.dev/ref/mem)
- [`log/slog`](https://pkg.go.dev/log/slog) · [`samber/oops`](https://github.com/samber/oops)
