# Error Handling Patterns and Logging

## Table of Contents

- [The Single Handling Rule](#the-single-handling-rule)
- [Panic and Recover](#panic-and-recover)
  - [When to panic](#when-to-panic)
  - [Recovering from panics](#recovering-from-panics)
- [Why Use `samber/oops`](#why-use-samberoops)
- [Structured Error Logging with `slog`](#structured-error-logging-with-slog)

## The Single Handling Rule

An error MUST be handled exactly once: either log it OR return it, NEVER both. Logging at every layer produces duplicate entries across log aggregators and obscures the true failure site:

```go
// ✗ Bad — logs AND returns (duplicate noise at every call layer)
func processOrder(id string) error {
    err := chargeCard(id)
    if err != nil {
        slog.Error("failed to charge card", "order_id", id, "error", err)
        return fmt.Errorf("charging card: %w", err)
    }
    return nil
}

// ✓ Good — wrap with structured context and return; let the boundary decide
func processOrder(id string) error {
    err := chargeCard(id)
    if err != nil {
        return oops.
            With("order_id", id).
            Wrapf(err, "charging card")
    }
    return nil
}

// ✓ Good — handle once at the top-level boundary (HTTP handler, main, worker)
func handleOrder(w http.ResponseWriter, r *http.Request) {
    err := processOrder(r.FormValue("id"))
    if err != nil {
        slog.Error("order processing failed", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

In retry loops, logging _intermediate_ retries that are handled by retrying is fine, **but do not both log and return the final error** — return the wrapped final error to the caller to handle once.

## Panic and Recover

### When to panic

`panic` MUST be reserved for truly unrecoverable states — programmer errors, impossible conditions, or corrupt invariants. NEVER panic for expected failures like network timeouts, validation errors, or missing files:

```go
// ✓ Acceptable — programmer error in startup initialization
func MustCompileRegex(pattern string) *regexp.Regexp {
    re, err := regexp.Compile(pattern)
    if err != nil {
        panic(fmt.Sprintf("invalid regex %q: %v", pattern, err))
    }
    return re
}

// ✗ Bad — panic for a normal runtime failure
func GetUser(id string) *User {
    user, err := db.Find(id)
    if err != nil {
        panic(err) // callers cannot recover gracefully
    }
    return user
}
```

### Recovering from panics

Use `recover` inside a deferred function at goroutine boundaries (HTTP handlers, worker goroutines) so a single panic cannot crash the process:

```go
func safeHandler(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if r := recover(); r != nil {
                slog.Error("panic recovered",
                    "panic", r,
                    "stack", string(debug.Stack()),
                )
                http.Error(w, "internal error", http.StatusInternalServerError)
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

For structured panic recovery with stack traces and attributes, `github.com/samber/oops` provides `oops.Recover` and `oops.Recoverf`.

## Why Use `samber/oops`

Standard `fmt.Errorf` errors lack stack traces, machine-readable error codes, and attached structured attributes. `github.com/samber/oops` is a drop-in `error` implementation that works with `errors.Is`/`errors.As` while attaching:

- **Stack traces** — pinpointing the exact origination site.
- **Structured context** — user ID, tenant ID, order ID, or request metadata traveling with the error.
- **Error codes** — stable machine-readable identifiers for dashboards and alerts.
- **Public/private separation** — safe user-facing messages separated from internal diagnostics.

```go
func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderReq) error {
    if err := s.db.Insert(ctx, req.Order); err != nil {
        return oops.
            In("order-service").
            Code("order_insert_failed").
            User(req.UserID).
            With("order_id", req.Order.ID).
            Wrapf(err, "inserting order")
    }
    return nil
}
```

## Structured Error Logging with `slog`

- Prefer `log/slog` (Go 1.21+) over `log.Printf` or `fmt.Println`.
- Keep log messages static and low-cardinality; pass dynamic fields (`job_id`, `duration`, `status`, `error`) as key-value attributes.
- Use log levels (`Debug`, `Info`, `Warn`, `Error`) consistently to reflect severity.
- Translate internal errors to safe user-facing messages at API boundaries while logging technical details internally.
- Avoid logging PII (passwords, tokens, raw secrets) in error strings or attributes.
