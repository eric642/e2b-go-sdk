# Container Packages, `bufio`, and String Builders

## Table of Contents

- [`container/list` — Doubly-Linked List](#containerlist--doubly-linked-list)
- [`container/heap` — Priority Queue](#containerheap--priority-queue)
- [`container/ring` — Circular Buffer](#containerring--circular-buffer)
- [`bufio` — Buffered I/O and Scanner Limits](#bufio--buffered-io-and-scanner-limits)
- [`strings.Builder` vs `bytes.Buffer`](#stringsbuilder-vs-bytesbuffer)
- [Third-Party Collection Libraries](#third-party-collection-libraries)

## `container/list` — Doubly-Linked List

A general-purpose doubly-linked list whose elements hold `any` values (wrap in a generic struct if type safety is needed).

| Operation | Complexity | Notes |
| --- | --- | --- |
| Insert/remove front or back | O(1) | `PushFront`, `PushBack`, `Remove` |
| Insert/remove arbitrary position | O(1) | Given `*list.Element` handle |
| Access by index / search | O(n) | Linear traversal required |

- **When to use:** LRU caches (O(1) `MoveToFront`), ordered maps (`map[K]*list.Element` for O(1) deletion while preserving insertion order), and collections needing stable element handles across mutations.
- **When NOT to use:** Almost everywhere else — slices outperform linked lists due to contiguous cache locality and avoiding one heap allocation per node.

## `container/heap` — Priority Queue

An array-backed min-heap operating on any type implementing `heap.Interface` (`sort.Interface` plus `Push(x any)` and `Pop() any`).

| Operation | Complexity | Notes |
| --- | --- | --- |
| `heap.Init(h)` | O(n) | Establish heap invariant before first use |
| `heap.Push(h, x)` / `heap.Pop(h)` | O(log n) | Always use package functions, not receiver methods directly |
| `heap.Fix(h, i)` | O(log n) | Re-heapify in place after mutating priority at index `i` |
| Peek root (`(*h)[0]`) | O(1) | Direct slice access |

**Priority updates:** Track each item's current heap index in a `map[string]int` (updated inside `Swap`), then call `heap.Fix(h, idx)` in O(log n) instead of linearly scanning or removing and re-pushing.

**Use cases:** Task schedulers, top-K queries, Dijkstra/A\* pathfinding, event timers.

## `container/ring` — Circular Buffer

A fixed-size circular doubly-linked list suited for rolling windows and overflow-free round-robin rotation (no integer counter to overflow even after billions of calls):

```go
// Rolling average of last 5 values
r := ring.New(5)
for _, v := range values {
    r.Value = v
    r = r.Next()
}

sum := 0.0
r.Do(func(v any) {
    if f, ok := v.(float64); ok {
        sum += f
    }
})
avg := sum / float64(r.Len())
```

## `bufio` — Buffered I/O and Scanner Limits

`bufio` wraps `io.Reader` and `io.Writer` with an internal buffer (default 4 KB) to amortize syscall overhead on small reads/writes.

- **`bufio.Writer`:** Always call `w.Flush()` and check its returned error — unflushed data is silently lost on exit.
- **`bufio.Scanner`:** Convenient line-by-line reading (`scanner.Scan()`, `scanner.Text()`), **but its default max token size is 64 KB** (`bufio.MaxScanTokenSize`). For larger lines (e.g., 2 MB JSON logs), call `scanner.Buffer()` before scanning, and always check `scanner.Err()` after the loop:

```go
scanner := bufio.NewScanner(r)
buf := make([]byte, 64*1024)
scanner.Buffer(buf, 2*1024*1024) // allow lines up to 2 MB

for scanner.Scan() {
    processLine(scanner.Bytes())
}
if err := scanner.Err(); err != nil {
    return fmt.Errorf("scanning input: %w", err)
}
```

## `strings.Builder` vs `bytes.Buffer`

| Type | Best For | Key Behavior |
| --- | --- | --- |
| `strings.Builder` | Pure string concatenation/assembly | `String()` returns built string with zero copy; call `Grow(n)` when final size is known |
| `bytes.Buffer` | Bidirectional I/O (`io.Reader` + `io.Writer`), binary/byte encoding, `sync.Pool` reuse | `String()` allocates and copies `[]byte` to `string`; `Reset()` reuses capacity |

When writing assembled strings to an `io.Writer`, preallocate a `strings.Builder` with `b.Grow(totalLen)`, write parts, and stream with `io.WriteString(w, b.String())` — avoiding both repeated reallocations and extra intermediate copies.

## Third-Party Collection Libraries

When standard library containers are insufficient (trees, concurrent sets, deques):

- **`github.com/emirpasic/gods`** — trees, sets, lists, stacks, maps, queues
- **`github.com/deckarep/golang-set`** — thread-safe and non-thread-safe sets
- **`github.com/gammazero/deque`** — fast ring-buffer double-ended queue
