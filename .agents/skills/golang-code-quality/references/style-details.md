# Code Style Details

## Extract Complex Conditions

When `if` conditions span multiple operands, extract them into named booleans:

```go
// Good — self-documenting
isAdmin := user.Role == RoleAdmin
isOwner := resource.OwnerID == user.ID
hasOverride := permissions.Contains(PermOverride)
if isAdmin || isOwner || hasOverride {
    allow()
}

// Bad — wall of logic
if user.Role == RoleAdmin || resource.OwnerID == user.ID || permissions.Contains(PermOverride) {
    allow()
}
```

**Exception:** When the last condition involves expensive processing, keep it inline to benefit from short-circuit evaluation:

```go
// Good — avoid expensive operation when possible
if isAdmin || isOwner || expensivePermissionCheck(user, resource) {
    allow()
}

// Wasteful — always runs expensive check
canOverride := expensivePermissionCheck(user, resource)
if isAdmin || isOwner || canOverride {
    allow()
}
```

## Value vs Pointer Arguments

This section covers **function parameters** (method receiver selection depends on mutation, sync primitives, and method-set consistency).

Pass small, fixed-size types by value — strings are already a `(pointer, length)` header internally:

```go
// Good — value types by value
func FormatUser(name string, age int, createdAt time.Time) string

// Good — pointer for mutation
func PopulateDefaults(cfg *Config)

// Good — pointer when nil is meaningful (optional field update)
func UpdateUser(ctx context.Context, id string, name *string) error

// Bad — pointer for no reason
func Greet(name *string) string
```

**When to use pointers:**

- The function **mutates** the value
- The struct is **large** (~128+ bytes) — avoids copying overhead
- **Nil is meaningful** (optional/nullable parameter)

**When NOT to use pointers:**

- `string`, `int`, `bool`, `float64`, `time.Time` — pass by value
- Read-only access to small structs — pass by value (better cache locality)
- "Just to save memory" — value copy is negligible; stack allocation is fast

**Memory access trade-offs when performance matters:**

- **Values (no pointer):** Stack allocation, strong CPU cache locality for small types, zero indirection cost. Slower only when copying large structs.
- **Pointers:** One extra dereference (negligible on modern CPUs), but risk cache misses if pointed-to data is not in cache. Essential for large structs (>~128 bytes) where copy cost dominates.
- **Rule of thumb:** For structs <~128 bytes with read-only access, values are typically faster due to cache locality. For mutation or large structs, pointers win. When in doubt, benchmark.
