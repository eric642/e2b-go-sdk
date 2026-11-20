# samber/do — Generics-Based DI

Type-safe dependency injection using Go generics. No reflection, no code generation, simple API.

- Docs: [do.samber.dev](https://do.samber.dev) | [github.com/samber/do/v2](https://github.com/samber/do)

Before writing `samber/do` code, refer to the library's official documentation for up-to-date API signatures and examples.

## Core Pattern

```go
// Register services with providers
injector := do.New()
do.Provide(injector, func(i do.Injector) (*UserService, error) {
    db := do.MustInvoke[*Database](i)
    return NewUserService(db), nil
})

// Invoke services (lazy — created on demand)
svc := do.MustInvoke[*UserService](injector)

// Graceful shutdown — all services implementing Shutdowner are closed
injector.ShutdownOnSignalsWithContext(ctx, os.Interrupt)
```

## Why samber/do

- **No code generation** — no build step, no generated files to maintain
- **No reflection** — errors are caught at compile time via generics, not at runtime
- **Strongly typed** — Go generics provide full type safety without `interface{}` casts
- **Built-in lifecycle** — health checks and graceful shutdown detected automatically
- **Container cloning** — create isolated test containers from production configuration
- **Simple API** — `Provide`, `Invoke`, `Shutdown` — that's most of what you need
- **Package system** — organize services by domain without manual wiring order

For full application setup, package organization (`do.Package`), lifecycle management (`Healthchecker`/`Shutdowner`), debugging (`ExplainInjector`), and testing with `Clone` + `Override`, consult the official [`samber/do` documentation](https://do.samber.dev) and the `github.com/samber/do/v2` package reference.
