---
name: golang-testing
description: "Golang testing patterns covering table-driven tests, testify, t.Parallel, fuzzing, goleak, and integration build tags. Use when writing, reviewing, or fixing flaky Go unit and integration tests. Do not use for microbenchmarking."
user-invocable: true
license: MIT
compatibility: Designed for Claude Code, Codex or similar harness, and for projects using Golang.
metadata:
  author: eric642
  version: "1.0.0"
  openclaw:
    emoji: "🧪"
    homepage: https://github.com/eric642/golang-skills
    requires:
      bins:
        - go
        - gotests
    install:
      - kind: go
        package: github.com/cweill/gotests/gotests@latest
        bins: [gotests]
allowed-tools: Read Edit Write Glob Grep Bash(go:*) Bash(golangci-lint:*) Bash(git:*) Agent Bash(gotests:*) AskUserQuestion
paths:
  - "**/*.go"
---

**Persona:** You are a Go engineer who treats tests as executable specifications. You write tests to constrain behavior, not to hit coverage targets.

**Thinking mode:** Reason as thoroughly as possible for test strategy design and failure analysis — shallow reasoning misses edge cases and produces brittle tests that pass today but break tomorrow. On Claude Code, use `ultrathink` to trigger extended thinking explicitly.

**Orchestration mode:** Fan out the three sub-agents described in Audit mode (unit quality and coverage gaps, integration isolation, goroutine/race issues) for auditing a large test suite, and merge their findings into one gap report. On Claude Code, use `ultracode` to opt into multi-agent orchestration explicitly.

**Modes:**

- **Write mode** — generating new tests for existing or new code. Work sequentially through the code under test; use `gotests` to scaffold table-driven tests, then enrich with edge cases and error paths.
- **Review mode** — reviewing a PR's test changes. Focus on the diff: check coverage of new behaviour, assertion quality, table-driven structure, and absence of flakiness patterns. Sequential.
- **Audit mode** — auditing an existing test suite for gaps, flakiness, or bad patterns (order-dependent tests, missing `t.Parallel()`, implementation-detail coupling). Launch up to 3 parallel sub-agents split by concern: (1) unit test quality and coverage gaps, (2) integration test isolation and build tags, (3) goroutine leaks and race conditions.
- **Debug mode** — a test is failing or flaky. Work sequentially: reproduce reliably, isolate the failing assertion, trace the root cause in production code or test setup.

**Dependencies:**

- gotests: `go install github.com/cweill/gotests/gotests@latest`

# Go Testing Best Practices

Write fast, deterministic, behavior-focused tests that verify public contracts rather than implementation details.

## Best Practices Summary

1. Table-driven tests MUST use named subtests — every test case needs a `name` field passed to `t.Run`
2. Integration tests MUST use build tags (`//go:build integration`) — never `testing.Short()` — so they are excluded at compile time
3. Tests MUST NOT depend on execution order — each test MUST be independently runnable
4. Independent stateless tests SHOULD call `t.Parallel()` on both parent and subtests
5. Tests MUST assert observable behavior and public API contracts (`package foo_test`), not internal fields or caches
6. Packages spawning goroutines SHOULD use `goleak.VerifyTestMain` in `TestMain` (or `goleak.VerifyNone(t)`) to catch leaks
7. Test helpers MUST call `t.Helper()` as their first statement so failure line numbers point to the caller
8. Use `testify` as assertion/mock helpers, not a replacement for the standard library
9. Mock consumer-site interfaces, never concrete types; inject a fake clock (`clockwork.Clock`) instead of calling `time.Sleep`
10. Keep unit tests fast (< 1ms) and isolated; use `httptest.NewRecorder` for handler unit tests instead of starting a real server
11. Run tests with `-race` and `-shuffle=on` in CI, and include `Example` functions as executable documentation
12. Test files MUST be named after the source file under test (`foo.go` -> `foo_test.go`), not after individual functions
13. Test functions SHOULD appear in the same order as the functions/methods they test in the source file

## Test Structure and Organization

Use external test packages (`package mypackage_test`) by default to test only the public contract; use same-package tests (`package mypackage`) only when testing unexported helpers is genuinely unavoidable. Name each test file after its source file (`helloworld.go` -> `helloworld_test.go`, never `abcd_test.go`), and order test functions to match their source order. Split a very large test file only when unwieldy, always keeping the source-file prefix (e.g. `foo_edgecases_test.go`).

```go
func TestAdd(t *testing.T) { ... }               // function test
func TestMyStruct_MyMethod(t *testing.T) { ... } // method test
func BenchmarkAdd(b *testing.B) { ... }          // benchmark
func ExampleAdd() { ... }                        // example
func FuzzAdd(f *testing.F) { ... }               // fuzz test
```

## Table-Driven Tests

Table-driven tests are the idiomatic Go pattern for covering multiple cases. Always include a descriptive `name` field passed to `t.Run`:

```go
func TestCalculatePrice(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name      string
        quantity  int
        unitPrice float64
        expected  float64
    }{
        {name: "single item", quantity: 1, unitPrice: 10.0, expected: 10.0},
        {name: "bulk discount - 100 items", quantity: 100, unitPrice: 10.0, expected: 900.0},
        {name: "zero quantity", quantity: 0, unitPrice: 10.0, expected: 0.0},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            if got := CalculatePrice(tt.quantity, tt.unitPrice); got != tt.expected {
                t.Errorf("CalculatePrice(%d, %.2f) = %.2f, want %.2f",
                    tt.quantity, tt.unitPrice, got, tt.expected)
            }
        })
    }
}
```

## Common Pitfall: Assert Scope Leaking into Subtests

Never create a `testify` `assert.New(t)` or `require.New(t)` instance in the parent test and reuse it inside `t.Run`. It captures the parent `*testing.T`, so failures inside a subtest get attributed to the parent while the failing subtest still reports `--- PASS`:

```go
// ✗ WRONG — `is` is bound to the parent's t
func TestCalculatePrice(t *testing.T) {
    is := assert.New(t)
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            is.Equal(tt.expected, CalculatePrice(tt.quantity, tt.unitPrice))
        })
    }
}

// ✓ RIGHT — build the assertion helper from each subtest's own t
func TestCalculatePrice(t *testing.T) {
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            is := assert.New(t)
            is.Equal(tt.expected, CalculatePrice(tt.quantity, tt.unitPrice))
        })
    }
}
```

## Unit Tests, HTTP Handlers, and Helpers

Keep unit tests fast (< 1ms), isolated from external services, and deterministic. Every shared test helper MUST start with `t.Helper()` so `go test` reports failures at the caller's `file:line`, not inside the helper.

- **HTTP handlers**: Use `httptest.NewRecorder()` with table-driven requests rather than spinning up a real HTTP server. See [HTTP Testing](./references/http-testing.md) for request/response body, header, query parameter, and status assertions.
- **Test timeouts and `synctest`**: For tests that may hang or test timers/deadlines, see [Test Helpers](./references/helpers.md).

## Goroutine Leak Detection with `goleak`

Use `go.uber.org/goleak` in concurrent packages to verify that shutdown paths actually stop all spawned goroutines:

```go
// Package-wide leak check (pass goleak.IgnoreCurrent() to exclude known init goroutines)
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}

// Or per-test leak check
func TestWorkerPool(t *testing.T) {
    defer goleak.VerifyNone(t)
    // ... test code ...
}
```

## Deterministic Concurrency with `testing/synctest`

`testing/synctest` (Go 1.25+) runs concurrent code inside a fake-clock bubble where synthetic time advances instantaneously only when all goroutines are blocked — eliminating real `time.Sleep` flakiness when testing timers, deadlines, and cancellation.

- Use `synctest.Test(t, func(t *testing.T) { ... })` with `synctest.Wait()` in Go 1.25+ (do not use the Go 1.24 experimental `synctest.Run` API unless targeting Go 1.24 with `GOEXPERIMENT=synctest`).
- Go 1.27+ adds `synctest.Sleep(d)` to advance the bubble's fake clock directly and `httptest.NewTestServer()` for in-memory fake-network server tests inside a `synctest.Test` bubble.
- See [Test Helpers](./references/helpers.md) for a full `synctest.Test` example.

## Fuzzing

Add fuzz tests for parsers, decoders, sanitizers, and security-critical functions with seed corpus entries and invariant checks:

```go
func FuzzReverse(f *testing.F) {
    f.Add("hello")
    f.Add("")

    f.Fuzz(func(t *testing.T, input string) {
        if doubleReversed := Reverse(Reverse(input)); input != doubleReversed {
            t.Errorf("Reverse(Reverse(%q)) = %q, want %q", input, doubleReversed, input)
        }
    })
}
```

## Benchmarks

Write benchmarks next to package tests using `b.Run` sub-benchmarks per variant/size, `b.ReportAllocs()`, and `b.Loop()` (Go 1.24+; use legacy `b.N` only for older Go targets). See [Benchmarks in a Test Suite](./references/benchmarks.md) for variant and size-parameterized examples.

## Modern Go Testing Features (Go 1.26+)

- **Test artifacts (Go 1.26+):** Use `t.ArtifactDir()` (also on `*testing.B` and `*testing.F`) to persist golden or debug artifacts instead of ad-hoc paths:

  ```go
  out := filepath.Join(t.ArtifactDir(), "rendered.json")
  if err := os.WriteFile(out, renderedBytes, 0o644); err != nil {
      t.Fatal(err)
  }
  ```

- **Automatic `stdversion` check (Go 1.27+):** `go test` runs the `stdversion` vet check by default and flags any stdlib API newer than the module's `go` directive. Fix by bumping `go` in `go.mod` or avoiding the newer API — do not silence it.

## Examples and Code Coverage

- **Executable documentation:** Write `ExampleXxx` functions with `// Output:` comments so `go test` verifies documented usage shown in `go doc` and pkg.go.dev. See [Examples as Documentation](./references/examples.md).
- **Coverage as a gap finder:** Generate profiles with `go test -coverprofile=coverage.out ./...` and inspect uncovered lines via `go tool cover -html=coverage.out`. Coverage reveals untested paths, not assertion quality. See [Code Coverage](./references/coverage.md).

## Integration Tests

Separate integration tests from unit tests with `//go:build integration` at the top of the file (before `package`). Do not use `testing.Short()` — it still compiles integration tests and leaks DB connections when `-short` is omitted.

```go
//go:build integration

package mypackage_test
```

Run with `go test -tags=integration ./...`. Organize database/service setup and per-test cleanup with `testify/suite` (`SetupSuite`, `TearDownTest`, `TearDownSuite`). See [Integration Testing](./references/integration-testing.md) for Docker Compose fixtures, SQL schemas, and suite patterns.

## Mocking and Fake Clocks

Define narrow interfaces at the consumer site and mock those interfaces — never embed or wrap concrete dependency structs. For time-dependent logic, inject a clock interface (`clockwork.Clock`) and advance `clockwork.FakeClock` in tests instead of calling `time.Sleep`. See [Mocking](./references/mocking.md) for `testify/mock`, fixtures, and `clockwork` examples.

## Enforce with Linters

Enforce test hygiene automatically with `golangci-lint` linters: `thelper`, `paralleltest`, and `testifylint`.

## Quick Reference

```bash
go test ./...                          # all unit tests
go test -run TestName/subtest ./...    # specific subtest (regexp supported)
go test -race -shuffle=on ./...        # race detection + randomized order
go test -coverprofile=coverage.out ./... # coverage profile
go test -bench=. -benchmem ./...       # benchmarks with allocations
go test -fuzz=FuzzName ./...           # fuzzing
go test -tags=integration ./...        # integration tests
```
