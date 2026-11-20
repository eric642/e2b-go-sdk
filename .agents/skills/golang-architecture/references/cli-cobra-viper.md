# CLI Architecture & Configuration with Cobra + Viper

## Table of Contents

- [CLI Stack Quick Reference](#cli-stack-quick-reference)
- [Command Layout](#command-layout)
- [Root Command, Subcommands & Flags](#root-command-subcommands--flags)
- [Argument Validation](#argument-validation)
- [Configuration Layering with Viper](#configuration-layering-with-viper)
- [Version Embedding & Exit Codes](#version-embedding--exit-codes)
- [I/O, Signals, Completions & Testing](#io-signals-completions--testing)

## CLI Stack Quick Reference

| Concern | Package / Tool |
| --- | --- |
| Commands & flags | `github.com/spf13/cobra` |
| Configuration layering | `github.com/spf13/viper` |
| POSIX/GNU flags | `github.com/spf13/pflag` (via Cobra) |
| Colored terminal output | `github.com/fatih/color` |
| Table output | `github.com/olekukonko/tablewriter` |
| Interactive TUI | `github.com/charmbracelet/bubbletea` |
| Version injection | `go build -ldflags` |
| Release packaging | `goreleaser` |

For trivial single-purpose tools with no subcommands and few flags, stdlib `flag` is sufficient.

## Command Layout

Organize CLI commands under `cmd/myapp/` with one file per command. Keep `main.go` minimal — it only calls `Execute()` and handles `os.Exit` at the top level.

```text
myapp/
├── cmd/
│   └── myapp/
│       ├── main.go       # package main, only calls Execute()
│       ├── root.go       # Root command + Viper init
│       ├── config.go     # Config struct + Viper unmarshaling
│       ├── serve.go      # "serve" subcommand
│       ├── migrate.go    # "migrate" subcommand
│       └── version.go    # "version" subcommand
└── configs/
    └── config.yaml       # Optional default config file
```

See runnable templates: [main.go](../assets/examples/main.go), [root.go](../assets/examples/root.go), [serve.go](../assets/examples/serve.go), and [go.mod](../assets/examples/go.mod).

## Root Command, Subcommands & Flags

- Set `SilenceUsage: true` so runtime errors do not dump full usage help.
- Set `SilenceErrors: true` so your entry point controls error formatting and exit codes.
- Initialize Viper inside `PersistentPreRunE` so configuration loads before every subcommand.
- Register subcommands in separate files under `cmd/myapp/` and group related commands cleanly.
- Distinguish **persistent** flags (inherited by all subcommands, e.g., `--config`) from **local** flags (scoped to one command, e.g., `--port`).
- Enforce flag constraints with `MarkFlagRequired`, `MarkFlagsMutuallyExclusive`, and `MarkFlagsOneRequired`, and add value suggestions via `RegisterFlagCompletionFunc`.
- Bind every configurable flag with `viper.BindPFlag` so CLI flags, env vars, and config files resolve uniformly. See [flags.go](../assets/examples/flags.go).

## Argument Validation

Cobra provides built-in positional argument validators (see [args.go](../assets/examples/args.go)):

| Validator | Description |
| --- | --- |
| `cobra.NoArgs` | Fails if any positional args are provided |
| `cobra.ExactArgs(n)` | Requires exactly `n` args |
| `cobra.MinimumNArgs(n)` | Requires at least `n` args |
| `cobra.MaximumNArgs(n)` | Allows at most `n` args |
| `cobra.RangeArgs(min, max)` | Requires between `min` and `max` args |
| `cobra.MatchAll(cobra.ExactArgs(n), cobra.OnlyValidArgs)` | Requires `n` args, each in `ValidArgs` |

## Configuration Layering with Viper

Viper resolves values in strict precedence order (highest to lowest):

1. **CLI flags** (`--port 9090`)
2. **Environment variables** (`MYAPP_PORT=9090` via `viper.SetEnvPrefix("MYAPP")`)
3. **Config file** (`port: 9090` in `.myapp.yaml` or `configs/config.yaml`)
4. **Defaults** (set in code)

Always ignore `viper.ConfigFileNotFoundError` so the CLI works without a config file. Configuration must never be hardcoded, and secrets must come from environment variables or a secret manager — never committed config files.

Define a typed config struct with `mapstructure` tags (see [config.go](../assets/examples/config.go)):

```go
type Config struct {
    Port     int    `mapstructure:"port"`
    Host     string `mapstructure:"host"`
    LogLevel string `mapstructure:"log-level"`
    Database struct {
        DSN     string `mapstructure:"dsn"`
        MaxConn int    `mapstructure:"max-conn"`
    } `mapstructure:"database"`
}

func loadConfig() (Config, error) {
    var cfg Config
    if err := viper.Unmarshal(&cfg); err != nil {
        return Config{}, fmt.Errorf("unmarshaling config: %w", err)
    }
    return cfg, nil
}
```

## Version Embedding & Exit Codes

Inject build metadata (`version`, `commit`, `date`) at compile time using `go build -ldflags` rather than hardcoding version strings. See [version.go](../assets/examples/version.go).

Follow Unix exit code conventions and map typed errors to codes in `main()` — never call `os.Exit` inside `RunE` (which skips `defer` and Cobra cleanup). See [exit_codes.go](../assets/examples/exit_codes.go):

| Code | Meaning | When to Use |
| --- | --- | --- |
| `0` | Success | Operation completed normally |
| `1` | General error | Runtime failure |
| `2` | Usage error | Invalid flags or arguments |
| `64-78` | BSD sysexits | Specific error categories |
| `126` | Cannot execute | Permission denied |
| `127` | Command not found | Missing dependency |
| `128+N` | Signal `N` | Terminated by signal (e.g., `130` = `SIGINT`) |

## I/O, Signals, Completions & Testing

- **stdout vs stderr:** Write pipeable data to `cmd.OutOrStdout()` and logs/diagnostics/errors to `cmd.ErrOrStderr()` — never mix logs into stdout. Detect TTY via `os.ModeCharDevice`, support `--output` (`table`/`json`/`plain`), and auto-disable ANSI color when piped. See [output.go](../assets/examples/output.go).
- **Signals:** Capture `SIGINT`/`SIGTERM` with `signal.NotifyContext` to drain long-running subcommands cleanly. See [signal.go](../assets/examples/signal.go).
- **Shell completions:** Expose Cobra's built-in bash/zsh/fish/PowerShell completion command plus custom `ValidArgsFunction` handlers. See [completion.go](../assets/examples/completion.go).
- **Testing:** Execute commands programmatically with `cmd.SetArgs(...)` and capture buffers via `cmd.SetOut` / `cmd.SetErr`. See [cli_test.go](../assets/examples/cli_test.go).
