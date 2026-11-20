# Type Assertions & Type Switches

## Safe Type Assertion

Type assertions MUST use the comma-ok form. The single-value form panics when the dynamic type does not match, turning a recoverable branch into a crash:

```go
// Good — safe
s, ok := val.(string)
if !ok {
    // handle mismatch
}

// Bad — panics if val is not a string
s := val.(string)
```

## Type Switch

Discover the dynamic type of an interface value:

```go
switch v := val.(type) {
case nil:
    // handle nil interface value explicitly
case string:
    fmt.Println(v)
case int:
    fmt.Println(v * 2)
case io.Reader:
    _, _ = io.Copy(os.Stdout, v)
default:
    fmt.Printf("unexpected type %T\n", v)
}
```

Cases are evaluated in order, so put concrete types before interface types — a concrete type listed after an interface it satisfies is unreachable.

A `nil` interface value matches `case nil`, not `default`. Add `case nil` explicitly when nil is a valid input, otherwise it silently falls through to `default` and gets reported as an unexpected type.

## Optional Behavior with Type Assertions

Check if a value supports additional capabilities without requiring them upfront. This keeps the declared parameter type minimal while still exploiting richer implementations:

```go
type Flusher interface {
    Flush() error
}

func writeData(w io.Writer, data []byte) error {
    if _, err := w.Write(data); err != nil {
        return err
    }
    // Flush only if the writer supports it
    if f, ok := w.(Flusher); ok {
        return f.Flush()
    }
    return nil
}
```

This pattern is used extensively in the standard library (e.g., `http.Flusher`, `io.ReaderFrom`, `io.WriterTo`).

## Asserting to an Interface, not a Concrete Type

Assert to the smallest interface that carries the behavior you need rather than to a concrete type. Asserting to `*os.File` binds the code to one implementation; asserting to `interface{ Sync() error }` accepts every type that can do the job.

## Reflection Type Assertions (Go 1.25+)

In reflection code, prefer `reflect.TypeAssert[T]` over `v.Interface().(T)` to avoid an extra allocation and bare-assertion panic risk:

```go
v := reflect.ValueOf(x)
if s, ok := reflect.TypeAssert[string](v); ok {
    use(s)
}
```

## Errors

Error inspection has dedicated helpers — `errors.As` (and Go 1.26+ `errors.AsType[T]`) walks the wrap chain, whereas a plain type assertion does not. A bare `err.(*MyError)` misses an error wrapped with `%w` anywhere up the stack. See [Error Wrapping and Inspection](error-wrapping.md).
