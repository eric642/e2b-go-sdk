# Packages, Files & Test Naming

## Table of Contents

- [Packages](#packages)
  - [Directory vs Package Name](#directory-vs-package-name)
- [Files](#files)
- [Import Aliasing](#import-aliasing)
- [Test Naming](#test-naming)
  - [Test Functions](#test-functions)
  - [Table-Driven Tests](#table-driven-tests)
  - [Test Helpers](#test-helpers)

## Packages

Package names MUST be **lowercase, single-word**, with no underscores or MixedCaps. They should be short, concise, and evocative of their purpose. Numbers are allowed (`oauth2`, `k8s`).

```go
// Good
package json
package http
package tabwriter
package oauth2

// Bad
package httpServer    // no MixedCaps
package http_server   // no underscores
package util          // too generic
package common        // meaningless
package helpers       // what does it help with?
package base          // says nothing
package model         // too vague
```

NEVER use generic package names like `util`, `helper`, `common`, `base`, `model`. They fail to communicate purpose and cause import collisions. If you reach for `util`, the function probably belongs in a more specific package.

Package names SHOULD be **singular**, not plural — `net/url` not `net/urls`, `go/token` not `go/tokens`.

### Directory vs Package Name

Directory names SHOULD **match the package name** when possible. Multi-word directories use **hyphens**, and since package names cannot contain hyphens, the package drops them.

```text
// Good — directory matches package
httputil/          → package httputil
middleware/        → package middleware
auth/              → package auth

// Good — hyphenated directory, package drops hyphens
user-service/      → package userservice
rate-limit/        → package ratelimit
go-chi/            → package chi

// Good — special directories
cmd/api/           → package main       (cmd/ subdirectories are always main)
internal/auth/     → package auth       (internal/ restricts visibility)

// Bad
user_service/      → package user_service  (underscores in both)
UserService/       → package UserService   (no MixedCaps in directories)
myPackage/         → package mypackage     (directory has caps, package doesn't)
```

Special directories have Go toolchain meaning and do not follow normal naming:

- `cmd/` — entry points, each subdirectory is `package main`
- `internal/` — restricts import visibility to parent module
- `testdata/` — ignored by the Go tool
- `vendor/` — vendored dependencies

Package names SHOULD NOT duplicate exported names — users see `bufio.Reader`, not `bufio.BufReader`. Think about the call site.

## Files

File names MUST be **lowercase** with words separated by **underscores**.

```text
user_handler.go
string_converter.go
http_client_test.go
```

Special suffixes:

- `_test.go` — test files (excluded from production builds)
- `_linux.go`, `_amd64.go` — OS/architecture-specific (build constraints)

## Import Aliasing

Import aliases SHOULD only be used on name collision. When an alias is necessary, use a descriptive short name.

```go
// Good — no alias needed
import "github.com/go-chi/chi/v5"

// Good — alias resolves collision
import (
    "crypto/rand"
    mrand "math/rand/v2"
)

// Good — disambiguates versioned packages
import (
    apiv1 "myapp/api/v1"
    apiv2 "myapp/api/v2"
)

// Good — conventional alias for generated code
import pb "myapp/proto/userpb"

// Bad — unnecessary alias adds cognitive load without resolving a collision
import f "fmt"
```

## Test Naming

### Test Functions

Test functions follow `Test` + the name of what is being tested. Use underscores for subcases.

```go
func TestParseToken(t *testing.T) { ... }
func TestServer_Handle(t *testing.T) { ... }           // method test
func TestParseToken_InvalidInput(t *testing.T) { ... }  // subcase
```

### Table-Driven Tests

Table-driven test case names SHOULD be **fully lowercase, descriptive phrases** — including acronyms. Use `input` for inputs and `expected` for expected outputs to make the data flow clear:

```go
tests := []struct {
    name         string
    input        string
    expectedCode int
    expectedErr  bool
}{
    {name: "empty input", input: "", expectedCode: 400, expectedErr: true},
    {name: "valid token", input: "abc123", expectedCode: 200},
    {name: "expired token", input: "exp", expectedCode: 401, expectedErr: true},
    {name: "invalid id", input: "???", expectedCode: 400, expectedErr: true}, // "id" not "ID"
}

// Bad — mixed case in test names
{name: "valid ID", ...}      // should be "valid id"
{name: "Empty Input", ...}   // should be "empty input"
```

### Test Helpers

Test helper functions that panic on failure conventionally use the `must` prefix: `mustLoadFixture()`, `mustParseURL()`.
