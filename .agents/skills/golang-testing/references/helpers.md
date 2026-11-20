# Test Helpers

## Test Timeout

For tests that may hang, use a timeout helper that panics with caller location:

```go
// https://github.com/stretchr/testify/issues/1101
func testWithTimeout(t *testing.T, timeout time.Duration) {
    t.Helper()

    testFinished := make(chan struct{})
    t.Cleanup(func() {
        close(testFinished)
    })

    var pc [1]uintptr
    n := runtime.Callers(2, pc[:])
    line, funcName := "", ""
    if n > 0 {
        frames := runtime.CallersFrames(pc[:])
        frame, _ := frames.Next()
        line = frame.File + ":" + strconv.Itoa(frame.Line)
        funcName = frame.Function
    }

    go func() {
        select {
        case <-testFinished:
        case <-time.After(timeout):
            panic(fmt.Sprintf("%s: Test timed out after: %v\n%s", funcName, timeout, line))
        }
    }()
}

// Usage
func TestLongRunningOperation(t *testing.T) {
    testWithTimeout(t, 2*time.Second)
    result := LongRunningOperation()
    // If this takes longer than 2 seconds, the test panics with location info
}
```

## Deterministic Goroutine Testing with `testing/synctest`

`testing/synctest` (Go 1.25+) runs concurrent code inside a fake-clock bubble where synthetic time advances only when all goroutines are durably blocked:

```go
import (
    "context"
    "testing"
    "testing/synctest"
    "time"
)

func TestContextTimeout(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        const timeout = 5 * time.Second

        ctx, cancel := context.WithTimeout(t.Context(), timeout)
        defer cancel()

        time.Sleep(timeout - time.Nanosecond)
        synctest.Wait()
        if err := ctx.Err(); err != nil {
            t.Fatalf("before timeout: %v", err)
        }

        time.Sleep(time.Nanosecond)
        synctest.Wait()
        if err := ctx.Err(); err != context.DeadlineExceeded {
            t.Fatalf("after timeout: got %v, want DeadlineExceeded", err)
        }
    })
}
```

- Use `synctest.Test` in Go 1.25+ — do not use the Go 1.24 experimental `synctest.Run` API unless a module explicitly targets Go 1.24 with `GOEXPERIMENT=synctest`.
- Go 1.27+ adds `synctest.Sleep(d)` to advance the bubble's fake clock directly (equivalent to `time.Sleep(d)` + `synctest.Wait()` without blocking a goroutine) and `httptest.NewTestServer()` for in-memory fake-network HTTP server tests inside a `synctest.Test` bubble.
