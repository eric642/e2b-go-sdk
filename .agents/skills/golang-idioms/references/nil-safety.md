# Nil Safety and Zero-Value API Patterns

## Table of Contents

- [Nil Pointer Receivers](#nil-pointer-receivers)
  - [Designing nil-safe receivers](#designing-nil-safe-receivers)
- [Nil Function Values](#nil-function-values)
  - [Default function pattern](#default-function-pattern)
- [Nil and Error Comparisons](#nil-and-error-comparisons)
  - [Returning nil error correctly](#returning-nil-error-correctly)
  - [Named returns and wrapper functions](#named-returns-and-wrapper-functions)
  - [Checking error chains with nil](#checking-error-chains-with-nil)
- [Nil in Generic Code](#nil-in-generic-code)
  - [The `comparable` constraint and zero values](#the-comparable-constraint-and-zero-values)
  - [Nil checks with unconstrained type parameters](#nil-checks-with-unconstrained-type-parameters)
- [Patterns for Nil-Safe APIs](#patterns-for-nil-safe-apis)
  - [Constructor with defaults](#constructor-with-defaults)
  - [Lazy initialization for zero-value usability](#lazy-initialization-for-zero-value-usability)
  - [`sync.Once` for concurrent lazy initialization](#synconce-for-concurrent-lazy-initialization)

## Nil Pointer Receivers

Always check pointers from external callers for `nil` before invoking methods. In Go, calling a method on a nil pointer succeeds only if the method body never dereferences the receiver; accessing any field panics immediately:

```go
type Notifier struct {
    webhook string
}

// ✗ Panics if n == nil — dereferences n to read webhook
func (n *Notifier) Send(msg string) error {
    return post(n.webhook, msg)
}

// ✓ Good — caller or method guards against nil explicitly
func NotifyAll(n *Notifier, messages []string) error {
    if n == nil {
        return errors.New("notifier is nil")
    }
    for _, msg := range messages {
        if err := n.Send(msg); err != nil {
            return err
        }
    }
    return nil
}
```

### Designing nil-safe receivers

When a nil receiver is an intentional optional state (e.g., optional logger), guard explicitly and document the behavior:

```go
func (l *Logger) Log(msg string) {
    if l == nil {
        return // intentional no-op when logger is nil
    }
    fmt.Printf("[%s] %s\n", l.prefix, msg)
}
```

Use nil-safe receivers sparingly — for most types, a nil receiver indicates a bug and should fail fast with an explicit error.

## Nil Function Values

Calling a nil `func` variable panics at runtime. Always check `!= nil` before invoking optional callbacks:

```go
type Client struct {
    OnRequest  func(url string)
    OnResponse func(status int)
}

func (c *Client) Fetch(url string) (int, error) {
    if c.OnRequest != nil {
        c.OnRequest(url)
    }
    status := 200
    if c.OnResponse != nil {
        c.OnResponse(status)
    }
    return status, nil
}
```

### Default function pattern

When using a constructor, install a no-op default to avoid repeating nil checks:

```go
func NewWorker(opts ...Option) *Worker {
    w := &Worker{
        onComplete: func(string) {}, // no-op default
    }
    for _, opt := range opts {
        opt(w)
    }
    return w
}
```

## Nil and Error Comparisons

### Returning nil error correctly

An interface value holds `(type, value)` and equals `nil` only when **both** are nil. Returning a typed nil pointer sets the type descriptor, making `err != nil` evaluate to `true`:

```go
// ✗ Bad — returns interface{type: *ValidationError, value: nil} != nil
func validate(s string) error {
    var err *ValidationError
    if s == "" {
        err = &ValidationError{Field: "name"}
    }
    return err
}

// ✓ Good — return untyped nil explicitly
func validate(s string) error {
    if s == "" {
        return &ValidationError{Field: "name"}
    }
    return nil
}
```

### Named returns and wrapper functions

The same trap occurs when wrapping a function returning a concrete error pointer (`*AppError`, `*ParseError`):

```go
// ✗ Bad — implicitly converts (*AppError)(nil) into a non-nil error interface
func Handle(fn func() *AppError) error {
    return fn()
}

// ✓ Good — check concrete pointer before returning or assigning to named error return
func Handle(fn func() *AppError) error {
    if err := fn(); err != nil {
        return err
    }
    return nil
}
```

### Checking error chains with nil

`errors.Is(err, nil)` returns `true` only if `err` is truly untyped `nil`. It does not repair a typed-nil interface returned from a buggy function.

## Nil in Generic Code

### The `comparable` constraint and zero values

Type parameters constrained by `comparable` support `==`, so comparing against `var zero T` works for both pointer and value types:

```go
func IsZero[T comparable](v T) bool {
    var zero T
    return v == zero // nil for *Foo, 0 for int, "" for string
}

// Explicit pointer-only constraint when specifically testing for nil
func IsNil[T interface{ ~*U }, U any](v T) bool {
    return v == nil
}
```

### Nil checks with unconstrained type parameters

You cannot compare an unconstrained `[T any]` value directly to `nil`:

```go
// ✗ Compile error: cannot compare v == nil
func Check[T any](v T) bool { return v == nil }

// ✓ Good — accept *T directly
func IsNilPtr[T any](v *T) bool { return v == nil }
```

## Patterns for Nil-Safe APIs

### Constructor with defaults

When a type requires non-nil collaborators, enforce construction through `New*`:

```go
type APIClient struct {
    httpClient *http.Client
    baseURL    string
}

func NewAPIClient(baseURL string) *APIClient {
    return &APIClient{
        httpClient: http.DefaultClient,
        baseURL:    baseURL,
    }
}
```

### Lazy initialization for zero-value usability

When the zero value should work out of the box (like `sync.Mutex` and `bytes.Buffer`), lazily initialize internal maps on first write:

```go
type Cache struct {
    mu   sync.Mutex
    data map[string]any
}

func (c *Cache) Get(key string) (any, bool) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.data == nil {
        return nil, false
    }
    v, ok := c.data[key]
    return v, ok
}

func (c *Cache) Set(key string, val any) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.data == nil {
        c.data = make(map[string]any)
    }
    c.data[key] = val
}
```

### `sync.Once` for concurrent lazy initialization

Use `sync.Once` when one-time lazy setup must be safe under concurrent callers:

```go
type DB struct {
    once sync.Once
    conn *sql.DB
    err  error
}

func (db *DB) connection() (*sql.DB, error) {
    db.once.Do(func() {
        db.conn, db.err = sql.Open("postgres", connStr)
    })
    return db.conn, db.err
}
```
