# Maps: Internals and Safety

## Table of Contents

- [Hash Table Structure and Growth](#hash-table-structure-and-growth)
- [Why Maps Never Shrink (and How to Compact)](#why-maps-never-shrink-and-how-to-compact)
- [Preallocation and Value vs Pointer Sizing](#preallocation-and-value-vs-pointer-sizing)
- [Comparable Keys and Zero-Alloc Composite Keys](#comparable-keys-and-zero-alloc-composite-keys)
- [Iteration Order, Safe Deletion, and Concurrency](#iteration-order-safe-deletion-and-concurrency)
- [`maps` Package Reference (Go 1.21+)](#maps-package-reference-go-121)

## Hash Table Structure and Growth

Go maps are reference types backed by hash tables (bucket arrays of 8 key-value pairs in traditional Go maps and Swiss-table groups in Go 1.24+). Assigning a map copies only the header pointer, not the underlying entries.

- **Load factor growth:** Adding entries past the load factor threshold (~6.5 per 8-slot bucket) doubles bucket capacity and incrementally evacuates entries to avoid long GC pauses.
- **No `cap()` function:** Map capacity depends on bucket geometry and load factor, not a fixed element count.

## Why Maps Never Shrink (and How to Compact)

`delete(m, k)` clears the entry slot (zeroing keys/values so referenced objects can be GC'd), **but the map's backing bucket array never shrinks**. If a map spikes to 10M entries and later drops to 1,000, the gigabytes of allocated buckets remain live for as long as the map is reachable. Calling `runtime.GC()` alone cannot reclaim buckets from a live map.

The only way to reclaim bucket memory is to **rebuild into a fresh map** and drop references to the old one:

```go
func (s *EventStore) Compact() {
    s.mu.Lock()
    defer s.mu.Unlock()

    fresh := make(map[string]*Event, len(s.events))
    for k, v := range s.events {
        fresh[k] = v
    }
    s.events = fresh // old bucket array becomes eligible for GC
}
```

## Preallocation and Value vs Pointer Sizing

Preallocate with `make(map[K]V, expectedSize)` when the size is known or estimable to avoid repeated growth and rehashing cycles.

Choose value vs pointer map entries based on struct size:

- **Small structs (`< 128` bytes, e.g., `type Coord struct { Lat, Lon float64 }`):** Use a value map `map[K]V`. Inline storage eliminates per-entry heap allocations and reduces GC scan pressure.
- **Large structs (`>= 128` bytes, e.g., 20-field `Document`):** Use a pointer map `map[K]*V`. Map lookups copy the full value out of the bucket; storing `*V` copies only an 8-byte pointer.

```go
// Small 16-byte struct — value map maximizes cache locality and minimizes GC pressure
coords := make(map[string]Coord, expected)

// Large struct — pointer map avoids copying hundreds of bytes per read
docs := make(map[string]*Document, expected)
```

## Comparable Keys and Zero-Alloc Composite Keys

Map keys MUST be comparable (`==` must work): numeric types, `string`, `bool`, pointers, channels, interfaces, arrays of comparable types, and structs whose fields are all comparable. Slices, maps, and functions **cannot** be map keys.

For multi-part keys in hot paths, use a **struct or fixed-size array** instead of `fmt.Sprintf` or string concatenation — lookups allocate zero bytes:

```go
type routeKey struct {
    method string
    path   string
}

routes := make(map[routeKey]string)
handler, ok := routes[routeKey{method: method, path: path}] // zero allocations

// Arrays work identically for numeric coordinates
pixels := make(map[[2]int]uint32, width*height)
color, ok := pixels[[2]int{x, y}]
```

## Iteration Order, Safe Deletion, and Concurrency

### Deterministic Iteration

Map iteration order is intentionally randomized by the Go runtime and MUST NOT be relied upon. Sort keys before iterating whenever output order matters:

```go
// ✓ Good (Go 1.23+) — deterministic sorted iteration
for _, k := range slices.Sorted(maps.Keys(headers)) {
    fmt.Fprintf(&b, "%s: %s\r\n", k, headers[k])
}
```

### Deleting During Iteration

Deleting entries from a map during `for k, v := range m` is explicitly safe and well-defined in Go:

```go
for k, v := range m {
    if shouldDelete(v) {
        delete(m, k)
    }
}
```

### Concurrent Access

Concurrent read/write or write/write access to a plain `map` triggers a fatal, unrecoverable runtime panic (`fatal error: concurrent map writes`). Protect shared maps with `sync.RWMutex` / `sync.Mutex` or use `sync.Map` for read-mostly/disjoint-key workloads.

### Defensive Copying

Exported accessors MUST NOT return a struct's internal map directly — callers can mutate it and violate invariants. Return `maps.Clone(c.data)` instead, and clone incoming maps in constructors when callers retain a reference.

## `maps` Package Reference (Go 1.21+)

| Function | Purpose |
| --- | --- |
| `Clone(m)` | Shallow copy preserving `nil` input |
| `Equal(m1, m2)` / `EqualFunc` | Key-value equality comparison |
| `Copy(dst, src)` | Copy entries from `src` into `dst` |
| `DeleteFunc(m, del)` | Delete entries matching predicate |
| `Keys(m)` / `Values(m)` / `All(m)` (1.23+) | Iterators over keys, values, or pairs |
| `Collect(seq)` / `Insert(m, seq)` (1.23+) | Build or populate maps from iterators |
