# Error Wrapping and Inspection

## Table of Contents

- [Error Wrapping with `%w`](#error-wrapping-with-w)
  - [`%w` vs `%v`: controlling exposure](#w-vs-v-controlling-exposure)
- [Inspecting Errors: `errors.Is` and `errors.As`](#inspecting-errors-errorsis-and-errorsas)
  - [`errors.Is` — match against a sentinel value](#errorsis--match-against-a-sentinel-value)
  - [`errors.As / errors.AsType` — extract a typed error from the chain](#errorsas--errorsastype--extract-a-typed-error-from-the-chain)
- [Combining Errors with `errors.Join`](#combining-errors-with-errorsjoin)
  - [Use case: validating multiple fields](#use-case-validating-multiple-fields)
  - [Use case: parallel or sequential cleanup with independent failures](#use-case-parallel-or-sequential-cleanup-with-independent-failures)
  - [`errors.Is` works through joined errors](#errorsis-works-through-joined-errors)

## Error Wrapping with `%w`

Wrapping preserves the original error in a chain that callers can inspect with `errors.Is` and `errors.As`. Errors SHOULD be wrapped at each layer to build a readable chain:

```go
// ✓ Good — wraps with context, preserves the chain
func (s *UserService) GetUser(id string) (*User, error) {
    user, err := s.repo.FindByID(id)
    if err != nil {
        return nil, fmt.Errorf("getting user: %w", err)
    }
    return user, nil
}
```

### `%w` vs `%v`: controlling exposure

Use `%w` within your module to preserve the error chain. Use `%v` at public API / system boundaries to prevent callers from coupling to internal error types:

```go
// Internal layer — wrap to preserve chain
func (r *repo) fetch(id string) error {
    return fmt.Errorf("querying database: %w", err)
}

// Public API boundary — inspect internally, break chain to hide backend internals
func (s *PublicService) GetItem(id string) error {
    err := s.repo.fetch(id)
    if err != nil {
        var te *BackendTimeoutError
        if errors.As(err, &te) {
            return fmt.Errorf("item lookup timed out: %v", err) // %v — callers cannot unwrap *BackendTimeoutError
        }
        return fmt.Errorf("item unavailable: %v", err)
    }
    return nil
}
```

## Inspecting Errors: `errors.Is` and `errors.As`

### `errors.Is` — match against a sentinel value

```go
// ✗ Bad — direct comparison breaks on wrapped errors
if err == sql.ErrNoRows { ... }

// ✓ Good — traverses the entire error chain
if errors.Is(err, sql.ErrNoRows) {
    return nil, ErrNotFound
}
```

### `errors.As / errors.AsType` — extract a typed error from the chain

```go
// ✗ Bad — type assertion breaks on wrapped errors
if ve, ok := err.(*ValidationError); ok { ... }

// ✓ Good — traverses the entire error chain
var ve *ValidationError
if errors.As(err, &ve) {
    slog.Warn("validation failed", "field", ve.Field, "reason", ve.Message)
}

// ✓ Better (Go 1.26+) — generic type-safe extraction for error types
if ve, ok := errors.AsType[*ValidationError](err); ok {
    slog.Warn("validation failed", "field", ve.Field, "reason", ve.Message)
}
```

Use `errors.As(err, &target)` when targeting Go <1.26 or when matching a non-`error` interface target.

## Combining Errors with `errors.Join`

`errors.Join` (Go 1.20+) combines multiple independent errors into one. The combined error works with `errors.Is` and `errors.As` — every inner error remains inspectable, and `errors.Join()` with only `nil` inputs returns `nil`.

### Use case: validating multiple fields

```go
func validateUser(u User) error {
    var errs []error

    if u.Name == "" {
        errs = append(errs, errors.New("name is required"))
    }
    if u.Email == "" {
        errs = append(errs, errors.New("email is required"))
    }

    return errors.Join(errs...) // returns nil if errs is empty
}
```

### Use case: parallel or sequential cleanup with independent failures

```go
func closeAll(closers ...io.Closer) error {
    var errs []error
    for _, c := range closers {
        if err := c.Close(); err != nil {
            errs = append(errs, fmt.Errorf("closing resource: %w", err))
        }
    }
    return errors.Join(errs...)
}
```

### `errors.Is` works through joined errors

```go
err := errors.Join(ErrNotFound, ErrUnauthorized)

errors.Is(err, ErrNotFound)     // true
errors.Is(err, ErrUnauthorized) // true
```
