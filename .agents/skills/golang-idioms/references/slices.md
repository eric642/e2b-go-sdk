# Slices: Internals and Safety

## Table of Contents

- [Memory Layout and Backing Array](#memory-layout-and-backing-array)
- [Capacity Growth and Preallocation](#capacity-growth-and-preallocation)
- [Append Aliasing and Full-Slice Expressions](#append-aliasing-and-full-slice-expressions)
- [Subslice Memory Retention and Defensive Copying](#subslice-memory-retention-and-defensive-copying)
- [Deleting During Iteration and Releasing Excess Capacity](#deleting-during-iteration-and-releasing-excess-capacity)
- [Range Loop Variable Capture](#range-loop-variable-capture)
- [`slices` Package Reference (Go 1.21+)](#slices-package-reference-go-121)

## Memory Layout and Backing Array

A slice is a 24-byte header (3 machine words): `{pointer, length, capacity}`. Assigning or passing a slice copies only the header, so multiple slices can share the same backing array:

```text
a := make([]int, 3, 5)
┌─────┬─────┬─────┐
│ ptr │len=3│cap=5│  ← slice header for a
└──┬──┴─────┴─────┘
   │
   ▼
┌───┬───┬───┬───┬───┐
│ 0 │ 0 │ 0 │   │   │  ← backing array (5 elements)
└───┴───┴───┴───┴───┘

b := a[1:2]
┌─────┬─────┬─────┐
│ ptr │len=1│cap=4│  ← slice header for b (shares backing array at a[1])
└──┬──┴─────┴─────┘
```

Mutations through either header modify the shared backing array.

## Capacity Growth and Preallocation

When `append` exceeds capacity, the runtime allocates a larger backing array and copies all elements — O(n) per growth. Recent Go versions roughly double capacity below 256 elements and grow by ~25% above, **but the growth heuristic is an implementation detail and not guaranteed by the Go spec**. NEVER hardcode growth thresholds or assume when a reallocation occurs.

Preallocate when the target size is known or estimable to eliminate intermediate copies:

```go
// Exact size known — direct indexing
out := make([]Result, len(input))
for i, v := range input {
    out[i] = transform(v)
}

// Approximate size known — zero length, preallocated capacity
out := make([]Result, 0, len(input))
for _, v := range input {
    out = append(out, transform(v))
}

// Pre-grow an existing slice before bulk append (Go 1.21+)
s = slices.Grow(s, additionalNeeded)
```

## Append Aliasing and Full-Slice Expressions

`append` writes into the existing backing array whenever `len(s) < cap(s)`. Appending to a caller-supplied slice or subslice silently overwrites adjacent elements visible to the caller:

```go
// ✗ Dangerous — a and b share backing array; b[0] = 99 mutates a[0]
a := make([]int, 3, 5)
b := append(a, 4)
b[0] = 99

// ✓ Good — full-slice expression sets cap == len, forcing a new allocation on append
b := append(a[:len(a):len(a)], 4)
```

Full-slice expressions `data[start:end:end]` are especially useful when splitting a buffer into append-safe chunks without eagerly allocating copies unless a caller actually appends.

## Subslice Memory Retention and Defensive Copying

A small subslice of a large slice keeps the **entire** backing array alive for GC. Copy the needed bytes on **every** return path (including fallback branches):

```go
// ✗ Bad — retains the entire multi-MB response buffer
func extractToken(resp []byte) []byte {
    return resp[:32]
}

// ✓ Good (Go 1.21+) — independent copy releases the large backing array
func extractToken(resp []byte) []byte {
    if len(resp) < 32 {
        return slices.Clone(resp)
    }
    return slices.Clone(resp[:32])
}
```

For structs owning slices, keep collection fields unexported and clone on **both ingress (constructors) and egress (getters)**:

```go
type Team struct {
    name    string
    members []string
}

func NewTeam(name string, members []string) *Team {
    return &Team{name: name, members: slices.Clone(members)}
}

func (t *Team) Members() []string {
    return slices.Clone(t.members)
}
```

`slices.Clone` preserves `nil` inputs (returning `nil`, not an empty slice).

## Deleting During Iteration and Releasing Excess Capacity

Forward indexing while deleting shifts elements and skips entries. Prefer `slices.DeleteFunc` (Go 1.21+) or iterate backwards. When elements contain pointers or slices (e.g., `Profile []byte`), chain `slices.Clip` (and note `slices.DeleteFunc` zeroes cleared tail slots since Go 1.22) so removed elements are not retained in excess capacity:

```go
// ✓ Good — remove inactive users and drop excess capacity
active := slices.Clip(slices.DeleteFunc(users, func(u User) bool {
    return !u.Active
}))
```

## Range Loop Variable Capture

Before Go 1.22, a `for range` loop reused a single iteration variable across iterations, so closures and `&v` all pointed to the last element. Go 1.22+ scopes loop variables per iteration **when `go.mod` specifies `go 1.22` or newer**; older `go` directives retain pre-1.22 behavior. Taking `&items[i]` avoids copying the struct and works identically across every Go version:

```go
// ✓ Good — takes address of the backing slice element directly
for i := range items {
    ptrs = append(ptrs, &items[i])
}
```

## `slices` Package Reference (Go 1.21+)

| Category | Key Functions |
| --- | --- |
| **Sort** | `Sort`, `SortFunc`, `SortStableFunc`, `IsSorted`, `Sorted` (1.23+) |
| **Search** | `BinarySearch`, `BinarySearchFunc`, `Contains`, `Index`, `IndexFunc` |
| **Mutate** | `Insert`, `Delete`, `DeleteFunc`, `Replace`, `Compact`, `Reverse`, `Grow`, `Clip` |
| **Create** | `Concat` (1.22+), `Repeat` (1.23+), `Chunk` (1.23+) |
| **Compare / Copy** | `Clone`, `Equal`, `EqualFunc`, `Compare` |

Use `slices.Equal` / `slices.EqualFunc` instead of `==` (which does not compile for slices) or `reflect.DeepEqual`.

| Operation | Use When |
| --- | --- |
| `copy(dst, src)` | Copying into a preallocated destination slice |
| `append(dst, src...)` | Appending elements to a slice |
| `slices.Clone(s)` | Creating an independent shallow copy |
| `s[:len(s):len(s)]` | Zero-alloc append isolation (forces realloc only on append) |
