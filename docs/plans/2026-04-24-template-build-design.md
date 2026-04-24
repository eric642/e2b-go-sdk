# Template Build — Design

Status: approved 2026-04-24.

## Goal

Implement server-side template building in the Go SDK so users can go from a
fluent `template.Builder` directly to a registered E2B template, with cache
semantics compatible with the Python/JS SDKs. Today `template.Builder.Build()`
returns `e2b.ErrNotImplemented` — callers must drop to the `e2b` CLI to
register a template. This fills that gap.

The Go SDK exposes the full surface of `Template` from the Python/JS SDKs:
Builder methods, static-like operations (build / build-in-background /
get-build-status / exists / assign-tags / remove-tags / get-tags), and the
cache-keyed file upload flow that makes repeat builds cheap.

## Non-goals

- Rewriting `internal/api/` generated code.
- New transport abstractions — reuse `transport.NewAPIClient` and the existing
  `*http.Client` wiring.
- A non-Go template DSL (Python dockerfile parsing is handled via an existing
  upstream parser; we port equivalent logic in Go).

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Scope = C (full parity with Python `Template`) | Half parity leaves users dropping to CLI; tags/aliases are thin wrappers over already-generated endpoints |
| 2 | API style = B (`template.Client`) | Matches `e2b.Create` sandbox object idiom; pools one API client; keeps `Builder` single-purpose |
| 3 | Context discovery = A (explicit `WithContext`) | Go convention favors explicit. Caller-frame magic is brittle across builds/vendor |
| 4 | Hash compatibility = A (byte-identical to Python) | Cross-SDK cache hits are the core product promise |
| 5 | Log delivery = B (`<-chan BuildEvent`) | Gives caller `select` composability; `Build` wraps it for block-and-return |
| 6 | Test strategy = B (unit + integration) | Unit locks hash/serialize; integration validates end-to-end + cache behavior |
| 7 | Package layout = A (flat `template/`) | Matches Python `e2b.template`; private helpers need no cross-package exports |
| 8 | Build log tail in `BuildError` = 20 entries | Enough to diagnose failures; keeps `%v` output bounded |
| 9 | Integration cleanup = delete created templates | Prevents junk accumulation on test accounts |

## Public API

### Builder (DSL)

Full parity with Python `TemplateBase`/`TemplateBuilder` and JS `templateBase`.
All builder methods return `*Builder` except the terminal ones, which return
`*FinalBuilder` to enforce ordering at compile time.

**From-*:** `FromImage(image, cred...)`, `FromBaseImage()`,
`FromDebianImage(variant)`, `FromUbuntuImage(variant)`, `FromPythonImage(v)`,
`FromNodeImage(variant)`, `FromBunImage(variant)`, `FromGoImage(v)`,
`FromTemplate(id)`, `FromDockerfileContent(s)`, `FromDockerfileFile(path)`,
`FromAWSRegistry(image, ak, sk, region)`,
`FromGCPRegistry(image, serviceAccountJSON)`.

**Files:** `Copy(src, dest, opts...)`, `CopyItems([]CopyItem)`,
`Remove([]path, opts...)`, `Rename(src, dest, opts...)`,
`MakeDir([]paths, opts...)`, `MakeSymlink(src, dest, opts...)`.

**Commands/env:** `RunCmd(cmd, opts...)`, `RunCmds([]cmds, opts...)`,
`SetWorkdir(path)`, `SetUser(user)`, `SetEnvs(map)`.

**Package managers / shortcuts:** `PipInstall(pkgs, opts...)`,
`NpmInstall(pkgs, opts...)`, `BunInstall(pkgs, opts...)`,
`AptInstall(pkgs, opts...)`, `GitClone(url, opts...)`,
`AddMCPServer([]names)`, `BetaDevContainerPrebuild(dir)`,
`BetaSetDevContainerStart(dir)` (terminal).

**Start/ready:** `SetStartCmd(cmd, ready) *FinalBuilder`,
`SetReadyCmd(ready) *FinalBuilder`. Accept a `ReadyCmd` value (with helpers
`WaitForPort(port)`, `WaitForURL(url, status)`, `WaitForFile(path)`,
`WaitForProcess(name)`, `WaitForTimeout(duration)`).

**Cache / context / export:** `SkipCache()`, `WithContext(dir)`,
`WithIgnore(patterns...)`, `ToDockerfile()`, `ToJSON()`.

Option functions cover each method's optional params. Examples:
`WithCopyUser`, `WithCopyMode`, `WithCopyForceUpload`, `WithCopyResolveSymlinks`,
`WithRemoveRecursive`, `WithRemoveForce`, `WithRunUser`, `WithPipUserInstall`,
`WithNpmGlobal`, `WithNpmDev`, `WithAptNoInstallRecommends`,
`WithAptFixMissing`, `WithGitBranch`, `WithGitDepth`, `WithGitUser`. A
`CopyItem` struct mirrors Python's `CopyItem` dict. `FromImage` accepts a
variadic `RegistryCredentials{Username, Password}` for basic-auth registries.

### Client

```go
type Client struct { /* private */ }
func NewClient(cfg e2b.Config) (*Client, error)

type BuildOptions struct {
    Name         string
    Tags         []string
    CPUCount     int32         // default 2
    MemoryMB     int32         // default 1024
    SkipCache    bool
    PollInterval time.Duration // default 200ms
}

type BuildInfo struct {
    TemplateID string
    BuildID    string
    Name       string
    Tags       []string
}

type BuildEvent struct {
    Log  *LogEntry
    Done *BuildInfo
    Err  error
}

func (c *Client) Build(ctx, b *Builder, opts BuildOptions) (*BuildInfo, error)
func (c *Client) BuildStream(ctx, b *Builder, opts BuildOptions) (<-chan BuildEvent, error)
func (c *Client) BuildInBackground(ctx, b *Builder, opts BuildOptions) (*BuildInfo, error)
func (c *Client) GetBuildStatus(ctx, info BuildInfo, logsOffset int) (*BuildStatus, error)
func (c *Client) Exists(ctx, name string) (bool, error)
func (c *Client) AssignTags(ctx, targetName string, tags []string) (*TagInfo, error)
func (c *Client) RemoveTags(ctx, name string, tags []string) error
func (c *Client) GetTags(ctx, templateID string) ([]TemplateTag, error)
func (c *Client) Delete(ctx, templateID string) error  // for integration cleanup
```

`Build` is a thin wrapper over `BuildStream` that drains the event channel and
returns `(*BuildInfo, error)`. `BuildInBackground` runs steps 1–5 synchronously
and does not poll. All long-running calls honor `ctx` cancellation.

## Build Pipeline

```
validate → compute hashes → POST /v3/templates → upload missing files →
POST /v2/templates/{id}/builds/{id} → poll /builds/{id}/status
```

1. **Validate.** Empty `opts.Name` → error. COPY present but no `WithContext`
   → error. `FromTemplate` incompatible with `ToDockerfile`.
2. **Compute hashes.** For each COPY build a `TemplateStep` with
   `filesHash`; capture the upload plan (src, hash, resolveSymlinks,
   forceUpload).
3. **Request build.** `POST /v3/templates` with `{name, tags, cpuCount,
   memoryMB}` → `{templateID, buildID, names, tags}`.
4. **Upload.** For each COPY: `GET /templates/{id}/files/{hash}`. If
   `present=false` or `forceUpload=true` and a signed URL is returned,
   tar-gz the file set and `PUT` to the URL via the shared `*http.Client`.
   Stream the tar through `io.Pipe` — do not buffer the whole payload.
5. **Trigger.** `POST /v2/templates/{templateID}/builds/{buildID}` with
   `TemplateBuildStartV2{steps, force, fromImage|fromTemplate,
   fromImageRegistry, startCmd, readyCmd}`.
6. **Poll.** `GET /templates/{id}/builds/{id}/status?logsOffset=N`. On each
   response forward new `log_entries` as `BuildEvent{Log: ...}`. Status
   transitions:
   - `building` / `waiting` → sleep `PollInterval`, repeat.
   - `ready` → emit `Done`, close channel.
   - `error` → build `*BuildError` with `Reason.Message`, last 20 log
     entries, close channel.
   - `ctx.Done()` → propagate `ctx.Err()`, close channel.

## Hash Algorithm (Python-compatible)

`calculateFilesHash(src, dest, contextDir, ignorePatterns, resolveSymlinks)`
must produce byte-identical SHA-256 hex strings as Python's
`calculate_files_hash` in `e2b/template/utils.py`.

```
h = sha256()
h.update("COPY <src> <dest>")
files = getAllFilesInPath(src, contextDir, ignorePatterns, includeDirs=true)
  # sorted by absolute path (Python's sorted() behavior)
if len(files) == 0: error "no files found in <src>"
for f in files:
    h.update(filepath.ToSlash(rel(f, contextDir)))
    if isSymlink(f):
        shouldFollow = resolveSymlinks && (isFile(f) || isDir(f))
        if !shouldFollow:
            writeStats(h, lstat(f))   # mode, size
            h.update(readlink(f))
            continue
    writeStats(h, stat(f))            # mode, size
    if regular(f): h.update(readAll(f))
return hex(h.Sum())
```

`writeStats` writes `str(mode)` then `str(size)` — decimal, no separators.

Compatibility hinges on `mode`: Python's `os.stat().st_mode` is POSIX with
type bits (e.g. `0o100644`); Go's `fs.FileMode` uses different constants
(`ModeDir`, `ModeSymlink`, etc.). A helper `goModeToPosix(fs.FileMode) uint32`
converts between them. Absolute sort ordering and forward-slash relative
paths in the hash stream are also required to match.

File collection uses `github.com/bmatcuk/doublestar/v4` (covers `**`, glob
character classes, ignore matching). `.dockerignore` is parsed manually:
split on newline, trim, drop empty and `#`-prefixed lines.

Tar packaging is `tar.gz` via `archive/tar` + `compress/gzip`, streamed
through `io.Pipe`. `arcname` is the relative path; dereference flag
follows `resolveSymlinks`. No custom mtime/uid/gid overrides — we keep
Python's behavior of passing stat through.

## Errors

```go
// errors.go
var ErrTemplateBuild  = errors.New("template build failed")
var ErrTemplateUpload = errors.New("template file upload failed")
var ErrTemplate       = errors.New("template error")

// template/errors.go
type BuildError struct {
    Op, TemplateID, BuildID, Step, Message string
    LogTail []LogEntry  // last 20 entries
    Err     error
}
func (e *BuildError) Error() string
func (e *BuildError) Unwrap() error // → ErrTemplateBuild

type UploadError struct{ Src, Hash string; Err error }
```

Validation errors (absolute/escape paths, missing context dir, empty name)
use the existing `e2b.InvalidArgumentError`.

## Testing

**Unit (`go test ./template/...`):**

- `hash_test.go`: golden fixtures under `template/testdata/hash/<case>/`
  (single file, nested dir, `.dockerignore`, symlinks with
  `resolveSymlinks` on/off). Golden hex strings are frozen from a Python
  run against the same fixture.
- `ignore_test.go`: `.dockerignore` parsing (comments, blanks, patterns).
- `builder_test.go`: each new method serializes to the expected JSON via
  `ToJSON()`, compared against a Python-frozen JSON golden.
- `client_test.go`: `httptest.Server` covers happy path, cache hit (skip
  upload), upload path, build error, ctx cancel. Asserts on emitted
  `BuildEvent` sequence.

**Integration (`*_integration_test.go`, skip when `E2B_API_KEY` unset):**

- `TestIntegrationBuildSimpleDebian`: `FromDebianImage.RunCmd` →
  `Build` → `e2b.Create(Template: info.TemplateID)` → run `echo`.
- `TestIntegrationBuildWithCopy`: first build uploads,
  second build hits cache (verify via log message).
- `TestIntegrationTags`: `AssignTags` / `GetTags` / `RemoveTags`.
- `TestIntegrationExists`: false before, true after a build.

All integration tests `t.Cleanup(func() { cli.Delete(ctx, id) })`.

## Example

`examples/template/main.go` (new):

```go
tpl := template.New().
    FromPythonImage("3.12").
    PipInstall([]string{"numpy"}).
    SetStartCmd("python -m http.server 8000", template.WaitForPort(8000))

cli, _ := template.NewClient(e2b.Config{})
events, _ := cli.BuildStream(ctx, tpl, template.BuildOptions{
    Name: "my-numpy-template:v1",
})
var info *template.BuildInfo
for ev := range events {
    switch {
    case ev.Log != nil:  fmt.Printf("[%s] %s\n", ev.Log.Level, ev.Log.Message)
    case ev.Err != nil:  log.Fatal(ev.Err)
    case ev.Done != nil: info = ev.Done
    }
}
sbx, _ := e2b.Create(ctx, e2b.CreateOptions{Template: info.TemplateID})
```

## Open Risks

- **Hash byte-compat.** Minor stat or ordering differences silently
  invalidate cross-SDK cache. Mitigated by golden tests against Python
  fixtures and an integration test that proves the second build logs
  `skipping cached upload`.
- **Glob semantics.** doublestar v4 ≠ wcmatch's exact flag set. We assume
  the subset used by `.dockerignore` patterns (globstar + dotmatch)
  overlaps. If a real fixture diverges we add a normalization pass
  rather than swap glob libraries.
- **`goModeToPosix` correctness.** Needs a round-trip test covering
  regular files, dirs, symlinks, and executable bits.

## Out-of-scope Followups

- Windows support for `calculateFilesHash` (Go `fs.FileMode` on Windows
  lacks POSIX mode bits; Python SDK has the same limitation).
- `BuildStream`-on-`BuildInBackground`-input — i.e. resume streaming logs
  for a build started elsewhere. Easy to add later via a `StreamBuild(info)`
  helper; keeping v1 surface small.
