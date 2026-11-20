# Writing Generic Data Structures (Go 1.18+)

## Type Constraints

Use the tightest constraint that satisfies your needs:

| Constraint | What It Allows | Use For |
| --- | --- | --- |
| `any` | All types | Containers that only store/retrieve |
| `comparable` | Types supporting `==` and `!=` | Map keys, set membership, dedup |
| `cmp.Ordered` | Numeric types + `string` (`<`, `<=`, `>`, `>=`) | Sorting, min/max, binary search |
| Custom interface | Domain-specific operations | Specialized containers |

### Custom Constraints

```go
// Union constraint — restrict to specific types
type Number interface {
    ~int | ~int64 | ~float64
}

// Method constraint — require specific behavior
type ComparableStringer interface {
    comparable
    String() string
}
```

The `~` prefix includes all types whose underlying type matches (e.g., `~int` matches `type UserID int`).

## Generic Set Example

```go
type Set[T comparable] map[T]struct{}

func NewSet[T comparable](vals ...T) Set[T] {
    s := make(Set[T], len(vals))
    for _, v := range vals {
        s[v] = struct{}{}
    }
    return s
}

func (s Set[T]) Add(v T)           { s[v] = struct{}{} }
func (s Set[T]) Remove(v T)        { delete(s, v) }
func (s Set[T]) Contains(v T) bool { _, ok := s[v]; return ok }
func (s Set[T]) Len() int          { return len(s) }

func (s Set[T]) Union(other Set[T]) Set[T] {
    result := make(Set[T], len(s)+len(other))
    for v := range s {
        result.Add(v)
    }
    for v := range other {
        result.Add(v)
    }
    return result
}
```

## Generic Sorted Slice

Use `cmp.Ordered` (not `comparable`, which lacks `<`, and not a custom `Less` interface when standard ordering suffices):

```go
func InsertSorted[T cmp.Ordered](s []T, v T) []T {
    i, _ := slices.BinarySearch(s, v)
    return slices.Insert(s, i, v)
}
```

## Constraint Composition

Combine multiple constraints with embedded interfaces:

```go
type OrderedStringer interface {
    cmp.Ordered
    fmt.Stringer
}
```

## When NOT to Use Generics

- **Single concrete type** — generics add complexity for no benefit.
- **`any` constraint forwarding to `any` APIs (e.g., `json.Marshal`)** — `func RespondJSON[T any](w, status, data T)` provides zero extra type safety over `data any`; it is just `interface{}` with extra syntax.
- **`any` constraint with type switches** — use a plain interface parameter instead.
- **Two or fewer instantiations** — the abstraction overhead is rarely justified.
- **Complex type relationships** — Go does not support higher-kinded types; if constraints become convoluted, prefer small interfaces.

Generics shine for data structures (containers, sets, trees), algorithms (sort, search, transform), and utility functions (min, max, clamp) where the logic is identical across types and type safety matters.
