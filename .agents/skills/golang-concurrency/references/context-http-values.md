# Context in HTTP, Databases & Tracing

## Table of Contents

- [Context in HTTP Servers](#context-in-http-servers)
- [Context in Outbound HTTP & Database Calls](#context-in-outbound-http--database-calls)
- [Context Values and Unexported Keys](#context-values-and-unexported-keys)
- [What Belongs in Context Values vs Function Parameters](#what-belongs-in-context-values-vs-function-parameters)
- [Middleware & Cross-Service Trace Propagation](#middleware--cross-service-trace-propagation)

## Context in HTTP Servers

`http.Request` carries a context that is cancelled when the client disconnects or the request handler returns. Always start from `r.Context()` — never create a fresh `context.Background()` inside a handler.

```go
func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context() // cancelled if the client disconnects

    order, err := h.orderService.Get(ctx, r.PathValue("id"))
    if err != nil {
        if ctx.Err() != nil {
            // Client disconnected, no point writing a response
            return
        }
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    _ = json.NewEncoder(w).Encode(order)
}
```

## Context in Outbound HTTP & Database Calls

Propagate `ctx` to every downstream I/O boundary using context-aware APIs: `http.NewRequestWithContext`, `db.BeginTx(ctx, nil)`, `QueryContext`, `QueryRowContext`, and `ExecContext`.

```go
// ✗ Bad — downstream HTTP call ignores request context
func (c *PaymentClient) Charge(ctx context.Context, amount int) error {
    req, _ := http.NewRequest("POST", c.url+"/charge", body)
    return c.client.Do(req)
}

// ✓ Good — respects caller cancellation and deadlines
func (c *PaymentClient) Charge(ctx context.Context, amount int) error {
    req, err := http.NewRequestWithContext(ctx, "POST", c.url+"/charge", body)
    if err != nil {
        return fmt.Errorf("creating request: %w", err)
    }
    return c.client.Do(req)
}
```

```go
// ✗ Bad — Begin() and QueryRow() ignore ctx
tx, err := r.db.Begin()
row := r.db.QueryRow("SELECT * FROM users WHERE id = $1", id)

// ✓ Good — BeginTx and *Context variants abort if ctx is done
tx, err := r.db.BeginTx(ctx, nil)
row := r.db.QueryRowContext(ctx, "SELECT * FROM users WHERE id = $1", id)
```

## Context Values and Unexported Keys

Context values carry request-scoped metadata that crosses API boundaries. Always declare keys using an unexported type and expose typed getter/setter functions so packages cannot collide.

```go
// ✓ Good — unexported key type prevents collisions across packages
type contextKey string

const (
    traceIDKey   contextKey = "trace_id"
    spanIDKey    contextKey = "span_id"
    requestIDKey contextKey = "request_id"
)

func WithTraceID(ctx context.Context, traceID string) context.Context {
    return context.WithValue(ctx, traceIDKey, traceID)
}

func TraceIDFromContext(ctx context.Context) (string, bool) {
    traceID, ok := ctx.Value(traceIDKey).(string)
    return traceID, ok
}

// ✗ Bad — plain string keys collide across packages
ctx = context.WithValue(ctx, "trace_id", traceID)
```

## What Belongs in Context Values vs Function Parameters

| Data | Context value? | Why |
| --- | --- | --- |
| `trace_id`, `span_id`, `request_id` | Yes | Request-scoped metadata for observability |
| Authenticated user / tenant | Yes | Request-scoped identity crossing API boundaries |
| Database connection | No | Infrastructure dependency — inject via struct field or parameter |
| Feature flags / config | No | Configuration — inject explicitly |
| Business arguments (order, amount) | No | Domain parameters — keep visible in function signatures |
| Logger | Depends | OK if enriched with request-scoped fields (`trace_id`); otherwise inject explicitly |

## Middleware & Cross-Service Trace Propagation

Middleware enriches `r.Context()` with incoming trace/correlation identifiers and passes the updated request via `r.WithContext(ctx)`. Outbound clients extract those values and forward them in headers so traces stay connected across services.

```go
func TracingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        traceID := r.Header.Get("X-Trace-ID")
        if traceID == "" {
            traceID = generateTraceID()
        }
        spanID := r.Header.Get("X-Span-ID")
        if spanID == "" {
            spanID = generateSpanID()
        }

        ctx := WithTraceID(r.Context(), traceID)
        ctx = context.WithValue(ctx, spanIDKey, spanID)

        w.Header().Set("X-Trace-ID", traceID)
        w.Header().Set("X-Span-ID", spanID)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

func (c *HTTPClient) Do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
    req, err := http.NewRequestWithContext(ctx, method, url, body)
    if err != nil {
        return nil, fmt.Errorf("creating request: %w", err)
    }

    if traceID, ok := TraceIDFromContext(ctx); ok {
        req.Header.Set("X-Trace-ID", traceID)
    }
    if spanID, ok := ctx.Value(spanIDKey).(string); ok {
        req.Header.Set("X-Span-ID", spanID)
    }
    return c.client.Do(req)
}
```

With OpenTelemetry, header extraction and injection are handled automatically via `otel` and `propagation.TraceContext`, using the same underlying mechanism: `context.Context` carries trace state and must flow unbroken through every layer.
