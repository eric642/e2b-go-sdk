# Pointer Types Deep Dive

## Table of Contents

- [Regular Pointers (`*T`)](#regular-pointers-t)
  - [Stack vs Heap (Escape Analysis)](#stack-vs-heap-escape-analysis)
  - [`new(T)` vs `&T{}`](#newt-vs-t)
- [`unsafe.Pointer`](#unsafepointer)
  - [The 6 Valid Patterns (from the Go spec)](#the-6-valid-patterns-from-the-go-spec)
  - [Critical Rule: NEVER Store `uintptr` Across Statements](#critical-rule-never-store-uintptr-across-statements)
  - [Modern Alternatives (prefer these)](#modern-alternatives-prefer-these)
- [`weak.Pointer[T]` (Go 1.24+)](#weakpointert-go-124)
  - [Use Cases](#use-cases)
  - [`runtime.AddCleanup` vs `runtime.SetFinalizer`](#runtimeaddcleanup-vs-runtimesetfinalizer)

## Regular Pointers (`*T`)

### Stack vs Heap (Escape Analysis)

Go's compiler decides whether to allocate on the stack or heap. A variable "escapes" to the heap when its lifetime extends beyond the function:

```go
func noEscape() int {
    x := 42
    return x // x stays on stack — copied on return
}

func escapes() *int {
    x := 42
    return &x // x escapes to heap — pointer outlives function
}
```

Use `go build -gcflags="-m"` to inspect escape analysis decisions. Heap allocations add GC pressure — avoid unnecessary escapes in hot paths.

### `new(T)` vs `&T{}`

Both allocate and return a pointer. `&T{}` is preferred for structs because it allows field initialization:

```go
p := new(Point)   // *Point with zero values
p := &Point{X: 1} // *Point with initialized fields — preferred
```

## `unsafe.Pointer`

`unsafe.Pointer` bypasses Go's type system for FFI and low-level memory layout access. Only the 6 patterns defined in the Go `unsafe` package documentation are valid; any other pattern is undefined behavior.

### The 6 Valid Patterns (from the Go spec)

**Pattern 1 — Convert `*T` to `*U` via `unsafe.Pointer`** (when `U` is no larger and alignment is compatible):

```go
f := 1.5
bits := *(*uint64)(unsafe.Pointer(&f))
```

**Pattern 2 — Convert `unsafe.Pointer` to `uintptr` (without converting back)** — e.g., printing an address.

**Pattern 3 — Convert `unsafe.Pointer` to `uintptr` and back in a single expression** (pointer arithmetic — superseded by `unsafe.Add`):

```go
p := unsafe.Pointer(uintptr(unsafe.Pointer(&s.field)) + offset)
```

**Pattern 4 — Convert `unsafe.Pointer` to `uintptr` directly inside a `syscall.Syscall` call:**

```go
syscall.Syscall(SYS_READ, fd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
```

**Pattern 5 — Convert `reflect.Value.Pointer()` or `UnsafeAddr()` directly to `unsafe.Pointer` in a single expression:**

```go
p := unsafe.Pointer(reflect.ValueOf(&x).Pointer())
```

**Pattern 6 — Convert a `reflect.SliceHeader` or `reflect.StringHeader` `Data` field** — deprecated in favor of `unsafe.Slice`, `unsafe.SliceData`, `unsafe.String`, and `unsafe.StringData`.

### Critical Rule: NEVER Store `uintptr` Across Statements

A `uintptr` is just an integer; the GC does not treat it as a pointer and may move or collect the referenced object between statements:

```go
// ✗ DANGEROUS — GC can move/reclaim x between statements
u := uintptr(unsafe.Pointer(&x))
p := unsafe.Pointer(u) // dangling pointer

// ✓ Safe (Go 1.17+) — use unsafe.Add with bounds checks first
if len(data) < 12 {
    return 0, 0, 0
}
base := unsafe.Pointer(unsafe.SliceData(data))
magic := *(*uint32)(base)
version := *(*uint16)(unsafe.Add(base, 4))
length := *(*uint32)(unsafe.Add(base, 8))
```

### Modern Alternatives (prefer these)

| Function | Since | Purpose |
| --- | --- | --- |
| `unsafe.Add(ptr, len)` | Go 1.17 | Pointer arithmetic without `uintptr` conversion |
| `unsafe.Slice(ptr, len)` | Go 1.17 | Create slice from pointer + length |
| `unsafe.String(ptr, len)` | Go 1.20 | Create string from pointer + length |
| `unsafe.SliceData(s)` | Go 1.20 | Get pointer to slice's backing array |
| `unsafe.StringData(s)` | Go 1.20 | Get pointer to string's backing array |

Always perform bounds checks before unsafe memory access.

## `weak.Pointer[T]` (Go 1.24+)

A weak pointer references an object without preventing garbage collection. Once collected, `Value()` returns `nil`:

```go
strong := new(MyType)
w := weak.Make(strong)

if p := w.Value(); p != nil {
    // object still alive
} else {
    // object was garbage collected
}
```

### Use Cases

- **Interning / deduplication tables** — return canonical pointers while letting unreferenced entries be reclaimed.
- **Automatic cache eviction** — cached entries disappear once no strong references remain.

### `runtime.AddCleanup` vs `runtime.SetFinalizer`

Prefer `runtime.AddCleanup` (Go 1.24+) over `runtime.SetFinalizer` to automatically remove dead weak-pointer map entries when the referent is collected:

- Multiple cleanups can be registered per object.
- The cleanup function receives a separate argument value, never a pointer to the collected object (no accidental resurrection).
- Works cleanly with `weak.Pointer[T]` and does not leak cyclic graphs.

```go
// Register cleanup when interning a canonical *string into a map[string]weak.Pointer[string]
runtime.AddCleanup(canonical, func(key string) {
    mu.Lock()
    defer mu.Unlock()
    if cur, ok := table[key]; ok && cur.Value() == nil {
        delete(table, key)
    }
}, s)
```
