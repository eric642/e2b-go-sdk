# Template Build Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement server-side E2B template build in the Go SDK, achieving full API parity with the Python/JS `Template` surfaces and Python-compatible cache hashing.

**Architecture:** Expand the `template/` package. Add a `Client` with `NewClient(cfg)` that reuses `transport.NewAPIClient`. Keep `Builder` as a pure DSL that records instructions; on build it computes Python-byte-compatible SHA-256 hashes per COPY, uploads missing files to signed URLs as tar-gz streams, triggers a server-side build, and streams log/status events back via `<-chan BuildEvent`.

**Tech Stack:** Go 1.21+, `archive/tar`, `compress/gzip`, `crypto/sha256`, `github.com/bmatcuk/doublestar/v4`, existing `internal/api` generated client, `internal/transport`.

**Reference design:** `docs/plans/2026-04-24-template-build-design.md`.

---

## Phase 0 — Setup

### Task 1: Add doublestar dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

**Step 1: Add dependency**

Run: `go get github.com/bmatcuk/doublestar/v4@v4.6.1`

Expected: updates `go.mod` / `go.sum`.

**Step 2: Verify build still works**

Run: `go build ./...`

Expected: success.

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "Add doublestar/v4 dependency for template file globbing"
```

---

## Phase 1 — Pure primitives (dockerignore, mode, file walk, hash, tar)

### Task 2: Parse `.dockerignore`

**Files:**
- Create: `template/ignore.go`
- Test: `template/ignore_test.go`

**Step 1: Write the failing test**

```go
package template

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadDockerignore_SkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	contents := "# comment\n\nnode_modules\n  dist/\n#another\n\n"
	if err := writeFile(filepath.Join(dir, ".dockerignore"), contents); err != nil {
		t.Fatal(err)
	}
	got := readDockerignore(dir)
	want := []string{"node_modules", "dist/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReadDockerignore_MissingFileReturnsEmpty(t *testing.T) {
	got := readDockerignore(t.TempDir())
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

// small helper lives in test file
func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
```

Add `import "os"` at the top.

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run TestReadDockerignore`
Expected: FAIL (`readDockerignore` undefined).

**Step 3: Implement `readDockerignore`**

Create `template/ignore.go`:

```go
package template

import (
	"os"
	"path/filepath"
	"strings"
)

// readDockerignore returns the non-comment, non-empty lines of
// <contextDir>/.dockerignore. Missing file → empty slice.
func readDockerignore(contextDir string) []string {
	data, err := os.ReadFile(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./template/ -run TestReadDockerignore -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/ignore.go template/ignore_test.go
git commit -m "Parse .dockerignore for template file context"
```

---

### Task 3: `goModeToPosix` mode conversion

**Files:**
- Create: `template/mode.go`
- Test: `template/mode_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestGoModeToPosix_RegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(path)
	got := goModeToPosix(info.Mode())
	// regular file (0o100000) | 0o644 = 33188
	if got != 0o100644 {
		t.Fatalf("got %o want %o", got, 0o100644)
	}
}

func TestGoModeToPosix_Directory(t *testing.T) {
	dir := t.TempDir()
	info, _ := os.Lstat(dir)
	got := goModeToPosix(info.Mode())
	// directory bit 0o40000; lower bits are tmp dir permissions
	if got&0o170000 != 0o040000 {
		t.Fatalf("not a directory mode: %o", got)
	}
}

func TestGoModeToPosix_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	_ = os.WriteFile(target, nil, 0o644)
	_ = os.Symlink(target, link)
	info, _ := os.Lstat(link)
	got := goModeToPosix(info.Mode())
	if got&0o170000 != 0o120000 {
		t.Fatalf("not a symlink mode: %o", got)
	}
}

// compile check — not exported but referenced
var _ fs.FileMode = os.ModeSymlink
```

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run TestGoModeToPosix`
Expected: FAIL (`goModeToPosix` undefined).

**Step 3: Implement**

Create `template/mode.go`:

```go
package template

import "io/fs"

// goModeToPosix converts Go's fs.FileMode to POSIX-style st_mode (as an
// unsigned integer matching what Python's os.stat().st_mode returns). This
// is used when hashing file metadata so Go-produced filesHash values match
// hashes produced by the Python/JS SDKs for the same inputs.
func goModeToPosix(m fs.FileMode) uint32 {
	// Permission bits (lower 9) map 1:1.
	posix := uint32(m.Perm())

	// Type bits (upper 4 bits of st_mode, traditionally):
	//   0o140000 socket, 0o120000 symlink, 0o100000 regular,
	//   0o060000 block, 0o040000 directory, 0o020000 char, 0o010000 fifo.
	switch {
	case m&fs.ModeSymlink != 0:
		posix |= 0o120000
	case m.IsDir():
		posix |= 0o040000
	case m&fs.ModeSocket != 0:
		posix |= 0o140000
	case m&fs.ModeNamedPipe != 0:
		posix |= 0o010000
	case m&fs.ModeCharDevice != 0:
		posix |= 0o020000
	case m&fs.ModeDevice != 0:
		posix |= 0o060000
	default:
		posix |= 0o100000 // regular file
	}

	// Setuid / setgid / sticky.
	if m&fs.ModeSetuid != 0 {
		posix |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		posix |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		posix |= 0o1000
	}
	return posix
}
```

**Step 4: Run tests**

Run: `go test ./template/ -run TestGoModeToPosix -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/mode.go template/mode_test.go
git commit -m "Add fs.FileMode → POSIX st_mode converter for hash compatibility"
```

---

### Task 4: File collection via doublestar glob + ignore

**Files:**
- Create: `template/files.go`
- Test: `template/files_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestGetAllFilesInPath_FlatMatch(t *testing.T) {
	ctx := t.TempDir()
	write(t, ctx, "a.txt", "a")
	write(t, ctx, "b.txt", "b")

	got, err := getAllFilesInPath("*.txt", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(ctx, "a.txt"), filepath.Join(ctx, "b.txt")}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGetAllFilesInPath_RecursesDirectory(t *testing.T) {
	ctx := t.TempDir()
	write(t, ctx, "dir/a.txt", "a")
	write(t, ctx, "dir/sub/b.txt", "b")

	got, err := getAllFilesInPath("dir", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// Expect the directory entries themselves plus all files under them
	expectContains := []string{
		filepath.Join(ctx, "dir"),
		filepath.Join(ctx, "dir", "a.txt"),
		filepath.Join(ctx, "dir", "sub"),
		filepath.Join(ctx, "dir", "sub", "b.txt"),
	}
	for _, w := range expectContains {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", w, got)
		}
	}
}

func TestGetAllFilesInPath_AppliesIgnore(t *testing.T) {
	ctx := t.TempDir()
	write(t, ctx, "keep.txt", "x")
	write(t, ctx, "skip.log", "x")
	got, err := getAllFilesInPath("*", ctx, []string{"*.log"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if filepath.Base(g) == "skip.log" {
			t.Fatalf("ignore rule not applied: %v", got)
		}
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run TestGetAllFilesInPath`
Expected: FAIL.

**Step 3: Implement**

Create `template/files.go`:

```go
package template

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

// getAllFilesInPath returns absolute paths of all files (and directories if
// includeDirs) matching `src` under contextDir, applying ignore patterns.
// Results are sorted by absolute path to match Python's sorted(files) output.
func getAllFilesInPath(src, contextDir string, ignorePatterns []string, includeDirs bool) ([]string, error) {
	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	fsys := os.DirFS(absCtx)

	matches, err := doublestar.Glob(fsys, filepath.ToSlash(src), doublestar.WithFilesOnly(), doublestar.WithNoFollow())
	_ = matches // placeholder; replaced below
	// doublestar.WithFilesOnly excludes dirs, but we want both files and dirs.
	matches, err = doublestar.Glob(fsys, filepath.ToSlash(src))
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}
	for _, m := range matches {
		if isIgnored(m, ignorePatterns) {
			continue
		}
		abs := filepath.Join(absCtx, m)
		info, err := os.Lstat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if includeDirs {
				out[abs] = struct{}{}
			}
			sub, err := doublestar.Glob(fsys, filepath.ToSlash(m)+"/**/*")
			if err != nil {
				return nil, err
			}
			for _, s := range sub {
				if isIgnored(s, ignorePatterns) {
					continue
				}
				out[filepath.Join(absCtx, s)] = struct{}{}
			}
		} else {
			out[abs] = struct{}{}
		}
	}

	result := make([]string, 0, len(out))
	for p := range out {
		result = append(result, p)
	}
	sort.Strings(result)
	return result, nil
}

// isIgnored returns true if any ignore pattern matches the relative path.
func isIgnored(rel string, patterns []string) bool {
	for _, p := range patterns {
		ok, err := doublestar.Match(p, rel)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// ensure fs import is used
var _ = fs.ValidPath
```

Remove the dead `_ = matches` placeholder once implementation works; here's the cleaner final body — replace the function above if you prefer:

```go
func getAllFilesInPath(src, contextDir string, ignorePatterns []string, includeDirs bool) ([]string, error) {
	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	fsys := os.DirFS(absCtx)
	matches, err := doublestar.Glob(fsys, filepath.ToSlash(src))
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, m := range matches {
		if isIgnored(m, ignorePatterns) {
			continue
		}
		abs := filepath.Join(absCtx, m)
		info, err := os.Lstat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if includeDirs {
				out[abs] = struct{}{}
			}
			sub, err := doublestar.Glob(fsys, filepath.ToSlash(m)+"/**/*")
			if err != nil {
				return nil, err
			}
			for _, s := range sub {
				if isIgnored(s, ignorePatterns) {
					continue
				}
				out[filepath.Join(absCtx, s)] = struct{}{}
			}
		} else {
			out[abs] = struct{}{}
		}
	}
	result := make([]string, 0, len(out))
	for p := range out {
		result = append(result, p)
	}
	sort.Strings(result)
	return result, nil
}
```

**Step 4: Run tests**

Run: `go test ./template/ -run TestGetAllFilesInPath -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/files.go template/files_test.go
git commit -m "Collect template files with doublestar glob and ignore filters"
```

---

### Task 5: `calculateFilesHash` with Python golden fixtures

**Files:**
- Create: `template/hash.go`
- Create: `template/hash_test.go`
- Create: `template/testdata/hash/single/app.txt`
- Create: `template/testdata/hash/single.hash`
- Create: `template/testdata/hash/nested/a/b/c.txt`
- Create: `template/testdata/hash/nested.hash`
- Create: `template/testdata/hash/ignored/.dockerignore` (content: `*.log`)
- Create: `template/testdata/hash/ignored/keep.txt` / `skip.log`
- Create: `template/testdata/hash/ignored.hash`

**Note on golden hashes:** Generate by running the Python SDK's `calculate_files_hash` against each fixture and freezing the hex output in the `.hash` file. If Python is unavailable at plan-exec time, substitute with the Go output as golden and leave a `# XXX frozen from Go — verify against Python` TODO comment at the top of each `.hash` file; a follow-up task (Task 24) reconciles.

**Step 1: Write fixtures**

```bash
mkdir -p template/testdata/hash/single
echo -n "hello" > template/testdata/hash/single/app.txt

mkdir -p template/testdata/hash/nested/a/b
echo -n "x" > template/testdata/hash/nested/a/b/c.txt

mkdir -p template/testdata/hash/ignored
echo -n "keep" > template/testdata/hash/ignored/keep.txt
echo -n "skip" > template/testdata/hash/ignored/skip.log
printf "*.log\n" > template/testdata/hash/ignored/.dockerignore
```

**Step 2: Write failing test**

```go
package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalculateFilesHash_Golden(t *testing.T) {
	cases := []struct {
		name           string
		src, dest      string
		ignorePatterns []string
	}{
		{name: "single", src: "app.txt", dest: "/app/", ignorePatterns: nil},
		{name: "nested", src: "a", dest: "/opt/", ignorePatterns: nil},
		{name: "ignored", src: ".", dest: "/work/", ignorePatterns: nil /* read from .dockerignore */},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctxDir := filepath.Join("testdata", "hash", c.name)
			pats := c.ignorePatterns
			pats = append(pats, readDockerignore(ctxDir)...)
			got, err := calculateFilesHash(c.src, c.dest, ctxDir, pats, false)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join("testdata", "hash", c.name+".hash"))
			if err != nil {
				t.Fatalf("missing golden: %v", err)
			}
			want := strings.TrimSpace(string(raw))
			if got != want {
				t.Fatalf("%s: hash mismatch\n got=%s\nwant=%s", c.name, got, want)
			}
		})
	}
}

func TestCalculateFilesHash_EmptyMatchErrors(t *testing.T) {
	_, err := calculateFilesHash("no-such", "/x", t.TempDir(), nil, false)
	if err == nil {
		t.Fatal("expected error for empty match")
	}
}
```

**Step 3: Implement**

Create `template/hash.go`:

```go
package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// calculateFilesHash produces a SHA-256 hex string whose byte-for-byte input
// matches the Python SDK's calculate_files_hash, so cache keys align across
// SDKs. Inputs:
//   src:             glob pattern relative to contextDir (as given by user)
//   dest:            destination path (opaque; mixed into the hash preamble)
//   contextDir:      base directory for resolving src
//   ignorePatterns:  .dockerignore + WithIgnore patterns
//   resolveSymlinks: whether to follow symlinks for their target content
func calculateFilesHash(src, dest, contextDir string, ignorePatterns []string, resolveSymlinks bool) (string, error) {
	h := sha256.New()
	io.WriteString(h, fmt.Sprintf("COPY %s %s", src, dest))

	files, err := getAllFilesInPath(src, contextDir, ignorePatterns, true)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no files found in %s", filepath.Join(contextDir, src))
	}

	for _, f := range files {
		rel, err := filepath.Rel(contextDir, f)
		if err != nil {
			return "", err
		}
		io.WriteString(h, filepath.ToSlash(rel))

		lst, err := os.Lstat(f)
		if err != nil {
			return "", err
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			shouldFollow := resolveSymlinks
			if shouldFollow {
				if _, err := os.Stat(f); err != nil {
					shouldFollow = false
				}
			}
			if !shouldFollow {
				writeStats(h, lst)
				target, err := os.Readlink(f)
				if err != nil {
					return "", err
				}
				io.WriteString(h, target)
				continue
			}
		}
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		writeStats(h, st)
		if st.Mode().IsRegular() {
			data, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeStats(h io.Writer, info os.FileInfo) {
	mode := goModeToPosix(info.Mode())
	io.WriteString(h, strconv.FormatUint(uint64(mode), 10))
	io.WriteString(h, strconv.FormatInt(info.Size(), 10))
}
```

**Step 4: Freeze goldens (first pass, Go-produced)**

Run: `go test ./template/ -run TestCalculateFilesHash -v` — it will fail because `.hash` files don't exist. Create them with a one-off helper:

Create `template/testdata/hash/gen_goldens_test.go` (build-tagged so it doesn't run by default):

```go
//go:build generate_goldens

package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateGoldens(t *testing.T) {
	cases := []struct{ name, src, dest string }{
		{"single", "app.txt", "/app/"},
		{"nested", "a", "/opt/"},
		{"ignored", ".", "/work/"},
	}
	for _, c := range cases {
		ctx := filepath.Join("testdata", "hash", c.name)
		pats := readDockerignore(ctx)
		got, err := calculateFilesHash(c.src, c.dest, ctx, pats, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "hash", c.name+".hash"), []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
```

Run: `go test -tags generate_goldens -run TestGenerateGoldens ./template/`

Then run the real test: `go test ./template/ -run TestCalculateFilesHash -v` → PASS.

**Step 5: Commit**

```bash
git add template/hash.go template/hash_test.go template/testdata/ template/testdata/hash/gen_goldens_test.go
git commit -m "Compute Python-compatible SHA-256 file hash for cache keys"
```

---

### Task 6: Tar streaming for uploads

**Files:**
- Create: `template/tar.go`
- Create: `template/tar_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"path/filepath"
	"testing"
)

func TestTarFileStream_IncludesContextRelative(t *testing.T) {
	ctx := t.TempDir()
	write(t, ctx, "hello/a.txt", "a")
	write(t, ctx, "hello/b.txt", "b")

	r, errc := tarFileStream("hello", ctx, nil, false)

	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names[filepath.ToSlash(hdr.Name)] = true
	}
	if err := <-errc; err != nil {
		t.Fatalf("producer err: %v", err)
	}
	if !names["hello/a.txt"] || !names["hello/b.txt"] {
		t.Fatalf("missing entries: %#v", names)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run TestTarFileStream`
Expected: FAIL.

**Step 3: Implement**

Create `template/tar.go`:

```go
package template

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

// tarFileStream packs files matched by `src` (relative to contextDir) into a
// gzip-compressed tar archive. It returns a pipe reader the caller streams
// to an HTTP PUT and an error channel that is closed once the producer
// goroutine finishes; receive from it to collect the terminal error.
func tarFileStream(src, contextDir string, ignorePatterns []string, resolveSymlinks bool) (io.ReadCloser, <-chan error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- writeTar(pw, src, contextDir, ignorePatterns, resolveSymlinks)
		close(errc)
	}()
	return pr, errc
}

func writeTar(pw *io.PipeWriter, src, contextDir string, ignorePatterns []string, resolveSymlinks bool) error {
	gz := gzip.NewWriter(pw)
	tw := tar.NewWriter(gz)

	finish := func(err error) error {
		twErr := tw.Close()
		gzErr := gz.Close()
		pwErr := pw.CloseWithError(err)
		if err != nil {
			return err
		}
		for _, e := range []error{twErr, gzErr, pwErr} {
			if e != nil {
				return e
			}
		}
		return nil
	}

	files, err := getAllFilesInPath(src, contextDir, ignorePatterns, true)
	if err != nil {
		return finish(err)
	}
	for _, f := range files {
		if err := addTarEntry(tw, f, contextDir, resolveSymlinks); err != nil {
			return finish(err)
		}
	}
	return finish(nil)
}

func addTarEntry(tw *tar.Writer, abs, contextDir string, resolveSymlinks bool) error {
	rel, err := filepath.Rel(contextDir, abs)
	if err != nil {
		return err
	}
	lst, err := os.Lstat(abs)
	if err != nil {
		return err
	}

	if lst.Mode()&os.ModeSymlink != 0 {
		if !resolveSymlinks {
			target, err := os.Readlink(abs)
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(lst, target)
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			return tw.WriteHeader(hdr)
		}
		// fall through: resolve target
		lst, err = os.Stat(abs)
		if err != nil {
			return err
		}
	}

	hdr, err := tar.FileInfoHeader(lst, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !lst.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
```

**Step 4: Run tests**

Run: `go test ./template/ -run TestTarFileStream -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/tar.go template/tar_test.go
git commit -m "Stream tar.gz archive of template build context via io.Pipe"
```

---

## Phase 2 — Instruction model, errors, types

### Task 7: Introduce structured `instruction` model + `serialize()`

**Files:**
- Modify: `template/template.go` — replace the existing ad-hoc `instruction{op, args}` with a structured type backed by typed constants; add `serialize(steps []TemplateStep) (*apiclient.TemplateBuildStartV2, error)` and `instructionsWithHashes()` internal helpers.
- Test: `template/serialize_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"testing"
)

func TestInstructionsWithHashes_ComputesCopyHash(t *testing.T) {
	ctx := t.TempDir()
	write(t, ctx, "f.txt", "abc")

	b := New().FromImage("alpine:3").WithContext(ctx).Copy("f.txt", "/x/")
	steps, err := b.instructionsWithHashes()
	if err != nil {
		t.Fatal(err)
	}
	var copyStep *instruction
	for i := range steps {
		if steps[i].Type == instTypeCopy {
			copyStep = &steps[i]
		}
	}
	if copyStep == nil || copyStep.FilesHash == "" {
		t.Fatalf("no COPY step with hash: %#v", steps)
	}
}

func TestSerialize_ShapesAPIBody(t *testing.T) {
	b := New().FromImage("alpine:3").Run("echo hi")
	body, err := b.serialize(false)
	if err != nil {
		t.Fatal(err)
	}
	if body.FromImage == nil || *body.FromImage != "alpine:3" {
		t.Fatalf("fromImage: %v", body.FromImage)
	}
	if body.Steps == nil || len(*body.Steps) != 1 || (*body.Steps)[0].Type != "RUN" {
		t.Fatalf("steps: %#v", body.Steps)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run "TestInstructionsWithHashes|TestSerialize"`
Expected: FAIL — types and methods don't exist yet.

**Step 3: Implement**

Modify `template/template.go` — replace internal representation:

```go
type instType string

const (
	instTypeRun     instType = "RUN"
	instTypeCopy    instType = "COPY"
	instTypeWorkdir instType = "WORKDIR"
	instTypeEnv     instType = "ENV"
	instTypeUser    instType = "USER"
	instTypeExpose  instType = "EXPOSE"
)

type instruction struct {
	Type            instType
	Args            []string
	Force           bool
	ForceUpload     bool
	HasForceUpload  bool   // whether ForceUpload was set (nilable in Python)
	ResolveSymlinks *bool
	FilesHash       string
}

// Existing Builder fields become:
//   instructions []instruction   (replace current []instruction type)
//   ignorePatterns []string
//   contextDir string
//   forceNextLayer bool
//   registryConfig *apiclient.FromImageRegistry
// Keep baseImage, baseTemplate, startCmd, readyCmd, tag, envs, workdir.
```

Then add (in the same file, below existing methods):

```go
func (b *Builder) WithContext(dir string) *Builder {
	b.contextDir = dir
	return b
}

func (b *Builder) WithIgnore(patterns ...string) *Builder {
	b.ignorePatterns = append(b.ignorePatterns, patterns...)
	return b
}

func (b *Builder) instructionsWithHashes() ([]instruction, error) {
	out := make([]instruction, len(b.instructions))
	copy(out, b.instructions)

	ignores := append([]string{}, b.ignorePatterns...)
	if b.contextDir != "" {
		ignores = append(ignores, readDockerignore(b.contextDir)...)
	}
	for i := range out {
		if out[i].Type != instTypeCopy {
			continue
		}
		if b.contextDir == "" {
			return nil, &e2b.InvalidArgumentError{Message: "COPY requires WithContext(dir) to be set on the builder"}
		}
		resolve := defaultResolveSymlinks
		if out[i].ResolveSymlinks != nil {
			resolve = *out[i].ResolveSymlinks
		}
		hash, err := calculateFilesHash(out[i].Args[0], out[i].Args[1], b.contextDir, ignores, resolve)
		if err != nil {
			return nil, err
		}
		out[i].FilesHash = hash
	}
	return out, nil
}

const defaultResolveSymlinks = false

func (b *Builder) serialize(force bool) (*apiclient.TemplateBuildStartV2, error) {
	steps, err := b.instructionsWithHashes()
	if err != nil {
		return nil, err
	}
	apiSteps := make([]apiclient.TemplateStep, 0, len(steps))
	for _, s := range steps {
		stp := apiclient.TemplateStep{
			Type: string(s.Type),
			Args: &s.Args,
		}
		if s.Force {
			f := true
			stp.Force = &f
		}
		if s.FilesHash != "" {
			fh := s.FilesHash
			stp.FilesHash = &fh
		}
		apiSteps = append(apiSteps, stp)
	}

	body := &apiclient.TemplateBuildStartV2{Steps: &apiSteps}
	if force {
		f := true
		body.Force = &f
	}
	if b.baseImage != "" {
		img := b.baseImage
		body.FromImage = &img
	}
	if b.baseTemplate != "" {
		t := b.baseTemplate
		body.FromTemplate = &t
	}
	if b.registryConfig != nil {
		body.FromImageRegistry = b.registryConfig
	}
	if b.startCmd != "" {
		s := b.startCmd
		body.StartCmd = &s
	}
	if b.readyCmd != "" {
		r := b.readyCmd
		body.ReadyCmd = &r
	}
	return body, nil
}
```

Add imports: `apiclient "github.com/eric642/e2b-go-sdk/internal/api"`.

Also update the existing `Run`, `Copy`, `Workdir`, `Env`, `Entrypoint`, `Expose` methods to build the new `instruction` struct (instead of the old `{op, args}` shape).

**Step 4: Run all template tests**

Run: `go test ./template/... -v`
Expected: existing + new tests PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Introduce typed Instruction model + serialize to TemplateBuildStartV2"
```

---

### Task 8: Error types + sentinels

**Files:**
- Modify: `errors.go` (add sentinels)
- Create: `template/errors.go`
- Create: `template/errors_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"errors"
	"testing"

	"github.com/eric642/e2b-go-sdk"
)

func TestBuildError_UnwrapsToSentinel(t *testing.T) {
	e := &BuildError{Op: "poll", Message: "boom"}
	if !errors.Is(e, e2b.ErrTemplateBuild) {
		t.Fatalf("should match e2b.ErrTemplateBuild")
	}
}

func TestUploadError_UnwrapsToSentinel(t *testing.T) {
	e := &UploadError{Src: "a", Hash: "h", Err: errors.New("x")}
	if !errors.Is(e, e2b.ErrTemplateUpload) {
		t.Fatalf("should match e2b.ErrTemplateUpload")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./template/ -run "TestBuildError|TestUploadError"`
Expected: FAIL.

**Step 3: Implement**

Modify `errors.go` to append:

```go
var (
	ErrTemplateBuild  = errors.New("template build failed")
	ErrTemplateUpload = errors.New("template file upload failed")
	ErrTemplate       = errors.New("template error")
)
```

Create `template/errors.go`:

```go
package template

import (
	"fmt"

	"github.com/eric642/e2b-go-sdk"
)

type BuildError struct {
	Op         string
	TemplateID string
	BuildID    string
	Step       string
	Message    string
	LogTail    []LogEntry
	Err        error
}

func (e *BuildError) Error() string {
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("template build %s: templateID=%s buildID=%s step=%s: %s",
		e.Op, e.TemplateID, e.BuildID, e.Step, msg)
}

func (e *BuildError) Unwrap() error { return e2b.ErrTemplateBuild }

type UploadError struct {
	Src, Hash string
	Err       error
}

func (e *UploadError) Error() string {
	return fmt.Sprintf("template upload src=%s hash=%s: %v", e.Src, e.Hash, e.Err)
}

func (e *UploadError) Unwrap() error { return e2b.ErrTemplateUpload }
```

**Step 4: Run test to verify it passes**

Run: `go test ./template/ -run "TestBuildError|TestUploadError" -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add errors.go template/errors.go template/errors_test.go
git commit -m "Add template-domain error types and sentinels"
```

---

### Task 9: BuildInfo / BuildEvent / BuildStatus / TagInfo types

**Files:**
- Create: `template/types.go`
- Test: minimal construction smoke — single `types_test.go` that just compiles.

**Step 1: Write smoke test**

```go
package template

import "testing"

func TestTypesCompileAndZeroValue(t *testing.T) {
	_ = BuildInfo{}
	_ = BuildEvent{}
	_ = BuildStatus{Status: BuildStatusReady}
	_ = TagInfo{}
	_ = TemplateTag{}
}
```

**Step 2: Run to confirm it fails**

Run: `go test ./template/ -run TestTypesCompile`
Expected: FAIL (types undefined).

**Step 3: Implement**

Create `template/types.go`:

```go
package template

import "time"

type BuildStatusValue string

const (
	BuildStatusBuilding BuildStatusValue = "building"
	BuildStatusWaiting  BuildStatusValue = "waiting"
	BuildStatusReady    BuildStatusValue = "ready"
	BuildStatusError    BuildStatusValue = "error"
)

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

type BuildStatus struct {
	TemplateID string
	BuildID    string
	Status     BuildStatusValue
	Logs       []LogEntry
	Reason     *BuildReason
}

type BuildReason struct {
	Step    string
	Message string
	Logs    []LogEntry
}

type TagInfo struct {
	BuildID string
	Tags    []string
}

type TemplateTag struct {
	Tag       string
	BuildID   string
	CreatedAt time.Time
}
```

**Step 4: Run test**

Run: `go test ./template/ -run TestTypes -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/types.go template/types_test.go
git commit -m "Add template build event and status types"
```

---

## Phase 3 — Builder expansion (parity with Python / JS)

### Task 10: `WithContext`, `WithIgnore`, `SkipCache`

Already scaffolded in Task 7 for `WithContext` / `WithIgnore`. This task adds `SkipCache()` and tests for all three.

**Files:**
- Modify: `template/template.go` (add `SkipCache`)
- Create: `template/builder_context_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestSkipCache_ForcesNextLayer(t *testing.T) {
	b := New().FromImage("alpine:3").SkipCache().Run("echo hi")
	if len(b.instructions) != 1 || !b.instructions[0].Force {
		t.Fatalf("SkipCache did not mark next instruction forced: %+v", b.instructions)
	}
	// subsequent instructions should not be force-marked
	b.Run("echo other")
	if b.instructions[1].Force {
		t.Fatal("force flag should reset after one instruction")
	}
}

func TestWithContextAndIgnore(t *testing.T) {
	b := New().WithContext("/tmp/x").WithIgnore("*.log", "build/")
	if b.contextDir != "/tmp/x" {
		t.Fatalf("context: %s", b.contextDir)
	}
	if len(b.ignorePatterns) != 2 {
		t.Fatalf("ignore: %v", b.ignorePatterns)
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestSkipCache|TestWithContextAndIgnore"`
Expected: FAIL (SkipCache undefined).

**Step 3: Implement**

In `template.go`, modify instruction-producing methods so they consume `b.forceNextLayer` and reset it:

```go
func (b *Builder) consumeForce() bool {
	f := b.forceNextLayer
	b.forceNextLayer = false
	return f
}

func (b *Builder) SkipCache() *Builder {
	b.forceNextLayer = true
	return b
}
```

Update `Run`:

```go
func (b *Builder) Run(cmd string) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instTypeRun, Args: []string{cmd}, Force: b.consumeForce()})
	return b
}
```

Apply the same `consumeForce()` to `Copy`, `Workdir`, `Env`, `Expose`, `Entrypoint`.

**Step 4: Run**

Run: `go test ./template/ -run "TestSkipCache|TestWithContextAndIgnore" -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/template.go template/builder_context_test.go
git commit -m "Add SkipCache and confirm WithContext/WithIgnore behavior"
```

---

### Task 11: Copy / CopyItems with options

**Files:**
- Modify: `template/template.go` (extend `Copy` signature, add `CopyItems`, define `CopyOption`, `CopyItem`)
- Create: `template/builder_copy_test.go`

**Step 1: Write failing test**

```go
package template

import (
	"os"
	"testing"
)

func TestCopy_EncodesUserAndMode(t *testing.T) {
	b := New().Copy("src", "/dst", WithCopyUser("root"), WithCopyMode(0o755), WithCopyForceUpload())
	if len(b.instructions) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(b.instructions))
	}
	in := b.instructions[0]
	if in.Type != instTypeCopy {
		t.Fatalf("type: %s", in.Type)
	}
	if in.Args[0] != "src" || in.Args[1] != "/dst" || in.Args[2] != "root" || in.Args[3] != "0755" {
		t.Fatalf("args: %v", in.Args)
	}
	if !in.HasForceUpload || !in.ForceUpload {
		t.Fatalf("forceUpload missing: %+v", in)
	}
}

func TestCopy_RejectsAbsolutePath(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			// no panic expected; error is returned via Builder state or panic?
			t.Fatal("expected validation error somehow")
		}
	}()
	_ = New().Copy("/etc/passwd", "/dst")
}

func TestCopyItems_EncodesAllEntries(t *testing.T) {
	items := []CopyItem{
		{Src: "a.py", Dest: "/app/"},
		{Src: "b.py", Dest: "/app/", Mode: os.FileMode(0o644)},
	}
	b := New().CopyItems(items)
	if len(b.instructions) != 2 {
		t.Fatalf("got %d instructions", len(b.instructions))
	}
	if b.instructions[1].Args[3] != "0644" {
		t.Fatalf("mode: %v", b.instructions[1].Args)
	}
}
```

Decision: Copy validation (absolute / escape) should return an error via a deferred `lastErr` on the Builder — surface during `ToDockerfile` / `serialize`, or panic? Python raises immediately with a traceback. The cleanest Go-idiomatic choice is to store `b.err` and return it on serialize. Adjust the rejection test:

```go
func TestCopy_RejectsAbsolutePath(t *testing.T) {
	b := New().FromImage("alpine:3").WithContext(t.TempDir()).Copy("/etc/passwd", "/dst")
	_, err := b.serialize(false)
	if err == nil {
		t.Fatal("expected error for absolute src")
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestCopy_|TestCopyItems"`
Expected: FAIL.

**Step 3: Implement**

In `template.go`, add:

```go
type CopyItem struct {
	Src             string
	Dest            string
	User            string
	Mode            os.FileMode
	ForceUpload     bool
	ResolveSymlinks *bool
}

type CopyOption func(*copyOpts)

type copyOpts struct {
	user            string
	mode            os.FileMode
	hasMode         bool
	forceUpload     bool
	hasForceUpload  bool
	resolveSymlinks *bool
}

func WithCopyUser(u string) CopyOption { return func(o *copyOpts) { o.user = u } }
func WithCopyMode(m os.FileMode) CopyOption {
	return func(o *copyOpts) { o.mode = m; o.hasMode = true }
}
func WithCopyForceUpload() CopyOption {
	return func(o *copyOpts) { o.forceUpload = true; o.hasForceUpload = true }
}
func WithCopyResolveSymlinks(b bool) CopyOption {
	return func(o *copyOpts) { o.resolveSymlinks = &b }
}

func (b *Builder) Copy(src, dest string, opts ...CopyOption) *Builder {
	if err := validateRelativePath(src); err != nil {
		b.err = err
		return b
	}
	o := copyOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	modeStr := ""
	if o.hasMode {
		modeStr = fmt.Sprintf("%04o", o.mode)
	}
	in := instruction{
		Type:            instTypeCopy,
		Args:            []string{src, dest, o.user, modeStr},
		Force:           b.consumeForce(),
		ForceUpload:     o.forceUpload,
		HasForceUpload:  o.hasForceUpload,
		ResolveSymlinks: o.resolveSymlinks,
	}
	b.instructions = append(b.instructions, in)
	return b
}

func (b *Builder) CopyItems(items []CopyItem) *Builder {
	for _, it := range items {
		opts := []CopyOption{}
		if it.User != "" {
			opts = append(opts, WithCopyUser(it.User))
		}
		if it.Mode != 0 {
			opts = append(opts, WithCopyMode(it.Mode))
		}
		if it.ForceUpload {
			opts = append(opts, WithCopyForceUpload())
		}
		if it.ResolveSymlinks != nil {
			opts = append(opts, WithCopyResolveSymlinks(*it.ResolveSymlinks))
		}
		b.Copy(it.Src, it.Dest, opts...)
	}
	return b
}
```

Add `validateRelativePath(src string) error` in `template.go` (or `files.go`):

```go
func validateRelativePath(src string) error {
	if filepath.IsAbs(src) {
		return &e2b.InvalidArgumentError{Message: fmt.Sprintf("copy src %q must be a relative path", src)}
	}
	normalized := filepath.ToSlash(filepath.Clean(src))
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return &e2b.InvalidArgumentError{Message: fmt.Sprintf("copy src %q escapes the context directory", src)}
	}
	return nil
}
```

Surface `b.err` in `serialize`:

```go
func (b *Builder) serialize(force bool) (*apiclient.TemplateBuildStartV2, error) {
	if b.err != nil {
		return nil, b.err
	}
	// ... existing body
}
```

Add `err error` field to Builder struct.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Extend Copy with options + add CopyItems"
```

---

### Task 12: File ops via RUN — Remove, Rename, MakeDir, MakeSymlink

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_fileops_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestRemove_ShellEscape(t *testing.T) {
	b := New().Remove([]string{"/tmp/cache"}, WithRemoveRecursive(), WithRemoveForce(), WithRemoveUser("root"))
	if len(b.instructions) != 1 {
		t.Fatalf("got %d", len(b.instructions))
	}
	in := b.instructions[0]
	if in.Type != instTypeRun {
		t.Fatal("not a RUN")
	}
	want := "rm -r -f /tmp/cache"
	if in.Args[0] != want {
		t.Fatalf("got %q", in.Args[0])
	}
	if in.Args[1] != "root" {
		t.Fatalf("user: %q", in.Args[1])
	}
}

func TestRename_Basic(t *testing.T) {
	b := New().Rename("/a", "/b", WithRenameForce())
	if b.instructions[0].Args[0] != "mv /a /b -f" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestMakeDir_MultipleWithMode(t *testing.T) {
	b := New().MakeDir([]string{"/a", "/b"}, WithMkdirMode(0o755))
	got := b.instructions[0].Args[0]
	want := "mkdir -p -m 0755 /a /b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMakeSymlink_Force(t *testing.T) {
	b := New().MakeSymlink("/usr/bin/python3", "/usr/bin/python", WithSymlinkForce())
	want := "ln -s -f /usr/bin/python3 /usr/bin/python"
	if b.instructions[0].Args[0] != want {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestRemove|TestRename|TestMakeDir|TestMakeSymlink"`
Expected: FAIL.

**Step 3: Implement**

Add to `template.go`:

```go
type RemoveOption func(*removeOpts)
type removeOpts struct{ force, recursive bool; user string }

func WithRemoveForce() RemoveOption     { return func(o *removeOpts) { o.force = true } }
func WithRemoveRecursive() RemoveOption { return func(o *removeOpts) { o.recursive = true } }
func WithRemoveUser(u string) RemoveOption { return func(o *removeOpts) { o.user = u } }

func (b *Builder) Remove(paths []string, opts ...RemoveOption) *Builder {
	o := removeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"rm"}
	if o.recursive { cmd = append(cmd, "-r") }
	if o.force { cmd = append(cmd, "-f") }
	cmd = append(cmd, paths...)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

type RenameOption func(*renameOpts)
type renameOpts struct{ force bool; user string }
func WithRenameForce() RenameOption { return func(o *renameOpts) { o.force = true } }
func WithRenameUser(u string) RenameOption { return func(o *renameOpts) { o.user = u } }

func (b *Builder) Rename(src, dest string, opts ...RenameOption) *Builder {
	o := renameOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"mv", src, dest}
	if o.force { cmd = append(cmd, "-f") }
	return b.runAs(strings.Join(cmd, " "), o.user)
}

type MkdirOption func(*mkdirOpts)
type mkdirOpts struct{ mode os.FileMode; hasMode bool; user string }
func WithMkdirMode(m os.FileMode) MkdirOption { return func(o *mkdirOpts) { o.mode = m; o.hasMode = true } }
func WithMkdirUser(u string) MkdirOption      { return func(o *mkdirOpts) { o.user = u } }

func (b *Builder) MakeDir(paths []string, opts ...MkdirOption) *Builder {
	o := mkdirOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"mkdir", "-p"}
	if o.hasMode {
		cmd = append(cmd, fmt.Sprintf("-m %04o", o.mode))
	}
	cmd = append(cmd, paths...)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

type SymlinkOption func(*symOpts)
type symOpts struct{ force bool; user string }
func WithSymlinkForce() SymlinkOption { return func(o *symOpts) { o.force = true } }
func WithSymlinkUser(u string) SymlinkOption { return func(o *symOpts) { o.user = u } }

func (b *Builder) MakeSymlink(src, dest string, opts ...SymlinkOption) *Builder {
	o := symOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"ln", "-s"}
	if o.force { cmd = append(cmd, "-f") }
	cmd = append(cmd, src, dest)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

// runAs appends a RUN instruction with optional user arg for parity with the
// internal instruction model where Args[1] is user (empty if not set).
func (b *Builder) runAs(cmd, user string) *Builder {
	args := []string{cmd}
	if user != "" {
		args = append(args, user)
	}
	b.instructions = append(b.instructions, instruction{Type: instTypeRun, Args: args, Force: b.consumeForce()})
	return b
}
```

Import `strings`.

**Step 4: Run tests**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add Remove/Rename/MakeDir/MakeSymlink builder methods"
```

---

### Task 13: `RunCmd`, `RunCmds`, `SetWorkdir`, `SetUser`, `SetEnvs`

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_runenv_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestRunCmd_WithUser(t *testing.T) {
	b := New().RunCmd("apt-get update", WithRunUser("root"))
	if b.instructions[0].Args[0] != "apt-get update" || b.instructions[0].Args[1] != "root" {
		t.Fatalf("args: %v", b.instructions[0].Args)
	}
}

func TestRunCmds_JoinsWithAmpAmp(t *testing.T) {
	b := New().RunCmds([]string{"a", "b"})
	if b.instructions[0].Args[0] != "a && b" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestSetWorkdir(t *testing.T) {
	b := New().SetWorkdir("/app")
	if b.instructions[0].Type != instTypeWorkdir || b.instructions[0].Args[0] != "/app" {
		t.Fatalf("%+v", b.instructions[0])
	}
}

func TestSetUser(t *testing.T) {
	b := New().SetUser("root")
	if b.instructions[0].Type != instTypeUser || b.instructions[0].Args[0] != "root" {
		t.Fatalf("%+v", b.instructions[0])
	}
}

func TestSetEnvs_InterleavedArgs(t *testing.T) {
	b := New().SetEnvs(map[string]string{"A": "1", "B": "2"})
	in := b.instructions[0]
	if in.Type != instTypeEnv || len(in.Args) != 4 {
		t.Fatalf("%+v", in)
	}
	// map order is unstable; just check content
	set := map[string]string{}
	for i := 0; i+1 < len(in.Args); i += 2 {
		set[in.Args[i]] = in.Args[i+1]
	}
	if set["A"] != "1" || set["B"] != "2" {
		t.Fatalf("envs: %v", set)
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestRunCmd|TestRunCmds|TestSetWorkdir|TestSetUser|TestSetEnvs"`
Expected: FAIL.

**Step 3: Implement**

Add:

```go
type RunOption func(*runOpts)
type runOpts struct{ user string }
func WithRunUser(u string) RunOption { return func(o *runOpts) { o.user = u } }

func (b *Builder) RunCmd(cmd string, opts ...RunOption) *Builder {
	o := runOpts{}
	for _, opt := range opts { opt(&o) }
	return b.runAs(cmd, o.user)
}

func (b *Builder) RunCmds(cmds []string, opts ...RunOption) *Builder {
	return b.RunCmd(strings.Join(cmds, " && "), opts...)
}

func (b *Builder) SetWorkdir(path string) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instTypeWorkdir, Args: []string{path}, Force: b.consumeForce()})
	return b
}

func (b *Builder) SetUser(user string) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instTypeUser, Args: []string{user}, Force: b.consumeForce()})
	return b
}

func (b *Builder) SetEnvs(envs map[string]string) *Builder {
	if len(envs) == 0 {
		return b
	}
	args := make([]string, 0, 2*len(envs))
	for k, v := range envs {
		args = append(args, k, v)
	}
	b.instructions = append(b.instructions, instruction{Type: instTypeEnv, Args: args, Force: b.consumeForce()})
	return b
}
```

**Step 4: Run tests**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add RunCmd/RunCmds/SetWorkdir/SetUser/SetEnvs builder methods"
```

---

### Task 14: Package manager shortcuts — Pip / Npm / Bun / Apt / GitClone

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_pkg_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestPipInstall_GlobalAndUser(t *testing.T) {
	got := New().PipInstall([]string{"numpy"}).instructions[0].Args[0]
	if got != "pip install numpy" {
		t.Fatalf("got %q", got)
	}
	gotUser := New().PipInstall([]string{"numpy"}, WithPipUserInstall()).instructions[0].Args[0]
	if gotUser != "pip install --user numpy" {
		t.Fatalf("got %q", gotUser)
	}
}

func TestNpmInstall_Global(t *testing.T) {
	got := New().NpmInstall([]string{"typescript"}, WithNpmGlobal()).instructions[0].Args[0]
	if got != "npm install -g typescript" {
		t.Fatalf("got %q", got)
	}
}

func TestBunInstall_Dev(t *testing.T) {
	got := New().BunInstall([]string{"tsx"}, WithBunDev()).instructions[0].Args[0]
	if got != "bun install --dev tsx" {
		t.Fatalf("got %q", got)
	}
}

func TestAptInstall_FixMissing(t *testing.T) {
	got := New().AptInstall([]string{"vim"}, WithAptFixMissing()).instructions[0].Args[0]
	// Two-step command joined with &&
	want := "apt-get update && DEBIAN_FRONTEND=noninteractive DEBCONF_NOWARNINGS=yes apt-get install -y --fix-missing vim"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestGitClone_BranchDepthPath(t *testing.T) {
	got := New().GitClone("https://x/y.git", WithGitBranch("main"), WithGitDepth(1), WithGitPath("/app")).instructions[0].Args[0]
	want := "git clone https://x/y.git --branch main --single-branch --depth 1 /app"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestPipInstall|TestNpmInstall|TestBunInstall|TestAptInstall|TestGitClone"`
Expected: FAIL.

**Step 3: Implement**

Add:

```go
type PipOption func(*pipOpts)
type pipOpts struct{ userInstall bool }
func WithPipUserInstall() PipOption { return func(o *pipOpts) { o.userInstall = true } }

func (b *Builder) PipInstall(packages []string, opts ...PipOption) *Builder {
	o := pipOpts{}
	for _, opt := range opts { opt(&o) }
	args := []string{"pip", "install"}
	if o.userInstall { args = append(args, "--user") }
	if len(packages) > 0 {
		args = append(args, packages...)
	} else {
		args = append(args, ".")
	}
	user := "root"
	if o.userInstall { user = "" }
	return b.runAs(strings.Join(args, " "), user)
}

type NpmOption func(*npmOpts)
type npmOpts struct{ global, dev bool }
func WithNpmGlobal() NpmOption { return func(o *npmOpts) { o.global = true } }
func WithNpmDev() NpmOption    { return func(o *npmOpts) { o.dev = true } }

func (b *Builder) NpmInstall(packages []string, opts ...NpmOption) *Builder {
	o := npmOpts{}
	for _, opt := range opts { opt(&o) }
	args := []string{"npm", "install"}
	if o.global { args = append(args, "-g") }
	if o.dev    { args = append(args, "--save-dev") }
	if len(packages) > 0 { args = append(args, packages...) }
	user := ""
	if o.global { user = "root" }
	return b.runAs(strings.Join(args, " "), user)
}

type BunOption func(*bunOpts)
type bunOpts struct{ global, dev bool }
func WithBunGlobal() BunOption { return func(o *bunOpts) { o.global = true } }
func WithBunDev() BunOption    { return func(o *bunOpts) { o.dev = true } }

func (b *Builder) BunInstall(packages []string, opts ...BunOption) *Builder {
	o := bunOpts{}
	for _, opt := range opts { opt(&o) }
	args := []string{"bun", "install"}
	if o.global { args = append(args, "-g") }
	if o.dev    { args = append(args, "--dev") }
	if len(packages) > 0 { args = append(args, packages...) }
	user := ""
	if o.global { user = "root" }
	return b.runAs(strings.Join(args, " "), user)
}

type AptOption func(*aptOpts)
type aptOpts struct{ noRecommends, fixMissing bool }
func WithAptNoInstallRecommends() AptOption { return func(o *aptOpts) { o.noRecommends = true } }
func WithAptFixMissing() AptOption           { return func(o *aptOpts) { o.fixMissing = true } }

func (b *Builder) AptInstall(packages []string, opts ...AptOption) *Builder {
	o := aptOpts{}
	for _, opt := range opts { opt(&o) }
	flags := ""
	if o.noRecommends { flags += "--no-install-recommends " }
	if o.fixMissing   { flags += "--fix-missing " }
	install := "DEBIAN_FRONTEND=noninteractive DEBCONF_NOWARNINGS=yes apt-get install -y " + flags + strings.Join(packages, " ")
	install = strings.TrimRight(install, " ")
	cmd := "apt-get update && " + install
	return b.runAs(cmd, "root")
}

type GitCloneOption func(*gitOpts)
type gitOpts struct{ branch string; depth int; path, user string }
func WithGitBranch(b string) GitCloneOption  { return func(o *gitOpts) { o.branch = b } }
func WithGitDepth(d int) GitCloneOption      { return func(o *gitOpts) { o.depth = d } }
func WithGitPath(p string) GitCloneOption    { return func(o *gitOpts) { o.path = p } }
func WithGitUser(u string) GitCloneOption    { return func(o *gitOpts) { o.user = u } }

func (b *Builder) GitClone(url string, opts ...GitCloneOption) *Builder {
	o := gitOpts{}
	for _, opt := range opts { opt(&o) }
	parts := []string{"git", "clone", url}
	if o.branch != "" {
		parts = append(parts, "--branch", o.branch, "--single-branch")
	}
	if o.depth > 0 {
		parts = append(parts, "--depth", fmt.Sprint(o.depth))
	}
	if o.path != "" {
		parts = append(parts, o.path)
	}
	return b.runAs(strings.Join(parts, " "), o.user)
}
```

**Step 4: Run tests**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add Pip/Npm/Bun/Apt/GitClone builder shortcuts"
```

---

### Task 15: `AddMCPServer` + beta devcontainer methods

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_beta_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestAddMCPServer_RequiresMCPGatewayBase(t *testing.T) {
	// from wrong base → err stored on builder, surfaces on serialize
	b := New().FromImage("alpine:3").AddMCPServer([]string{"brave"})
	if _, err := b.serialize(false); err == nil {
		t.Fatal("expected error because base isn't mcp-gateway")
	}
}

func TestAddMCPServer_Pulls(t *testing.T) {
	b := New().FromTemplate("mcp-gateway").AddMCPServer([]string{"brave", "exa"})
	if b.instructions[0].Args[0] != "mcp-gateway pull brave exa" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestBetaDevContainer_RequiresDevcontainerBase(t *testing.T) {
	b := New().FromImage("alpine:3").BetaDevContainerPrebuild("/work")
	if _, err := b.serialize(false); err == nil {
		t.Fatal("expected error for wrong base")
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestAddMCPServer|TestBetaDev"`
Expected: FAIL.

**Step 3: Implement**

```go
func (b *Builder) AddMCPServer(servers []string) *Builder {
	if b.baseTemplate != "mcp-gateway" {
		b.err = &e2b.InvalidArgumentError{Message: "MCP servers can only be added to mcp-gateway template"}
		return b
	}
	return b.runAs("mcp-gateway pull "+strings.Join(servers, " "), "root")
}

func (b *Builder) BetaDevContainerPrebuild(dir string) *Builder {
	if b.baseTemplate != "devcontainer" {
		b.err = &e2b.InvalidArgumentError{Message: "devcontainer prebuild requires devcontainer base template"}
		return b
	}
	return b.runAs("devcontainer build --workspace-folder "+dir, "root")
}
```

`BetaSetDevContainerStart` returns `*FinalBuilder` — deferred to Task 18.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add AddMCPServer and BetaDevContainerPrebuild with base-template guards"
```

---

### Task 16: New base-image factories (Debian / Ubuntu / Bun)

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_fromimage_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestFromDebianImage(t *testing.T) {
	if got := New().FromDebianImage("").BaseImage(); got != "debian:stable" {
		t.Fatalf("got %q", got)
	}
	if got := New().FromDebianImage("bookworm").BaseImage(); got != "debian:bookworm" {
		t.Fatalf("got %q", got)
	}
}

func TestFromUbuntuImage(t *testing.T) {
	if got := New().FromUbuntuImage("").BaseImage(); got != "ubuntu:latest" {
		t.Fatalf("got %q", got)
	}
}

func TestFromBunImage(t *testing.T) {
	if got := New().FromBunImage("").BaseImage(); got != "oven/bun:latest" {
		t.Fatalf("got %q", got)
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestFromDebian|TestFromUbuntu|TestFromBun"`
Expected: FAIL.

**Step 3: Implement**

```go
func (b *Builder) FromDebianImage(variant string) *Builder {
	if variant == "" { variant = "stable" }
	return b.FromImage("debian:" + variant)
}
func (b *Builder) FromUbuntuImage(variant string) *Builder {
	if variant == "" { variant = "latest" }
	return b.FromImage("ubuntu:" + variant)
}
func (b *Builder) FromBunImage(variant string) *Builder {
	if variant == "" { variant = "latest" }
	return b.FromImage("oven/bun:" + variant)
}
```

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add FromDebian/FromUbuntu/FromBun image factories"
```

---

### Task 17: Registry credential helpers (basic / AWS / GCP)

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_registry_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestFromImage_WithBasicCreds(t *testing.T) {
	b := New().FromImage("priv/x:latest", RegistryCredentials{Username: "u", Password: "p"})
	if b.registryConfig == nil {
		t.Fatal("no registry config stored")
	}
}

func TestFromAWSRegistry(t *testing.T) {
	b := New().FromAWSRegistry("123.dkr.ecr.us-west-2.amazonaws.com/x:latest", "AKIA", "SECRET", "us-west-2")
	if b.baseImage != "123.dkr.ecr.us-west-2.amazonaws.com/x:latest" || b.registryConfig == nil {
		t.Fatalf("%+v / %+v", b.baseImage, b.registryConfig)
	}
}

func TestFromGCPRegistry(t *testing.T) {
	b := New().FromGCPRegistry("gcr.io/p/i:latest", `{"type":"sa"}`)
	if b.baseImage == "" || b.registryConfig == nil {
		t.Fatalf("missing state")
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestFromImage_WithBasicCreds|TestFromAWSRegistry|TestFromGCPRegistry"`
Expected: FAIL.

**Step 3: Implement**

Consult `apiclient.FromImageRegistry` to see its shape:

Run: `grep -n "type FromImageRegistry" internal/api/client.gen.go`

Expected: discriminated union. Handle via the generated helpers (`FromAWSRegistry0`, `FromBasicRegistry0`, `FromGCPRegistry0` setter methods on the union type).

```go
type RegistryCredentials struct{ Username, Password string }

func (b *Builder) FromImage(image string, creds ...RegistryCredentials) *Builder {
	b.baseImage = image
	b.baseTemplate = ""
	b.registryConfig = nil
	if len(creds) > 0 && (creds[0].Username != "" || creds[0].Password != "") {
		reg := &apiclient.FromImageRegistry{}
		if err := reg.FromBasicAuthRegistry(apiclient.BasicAuthRegistry{
			Type: "registry", Username: creds[0].Username, Password: creds[0].Password,
		}); err != nil {
			b.err = err
			return b
		}
		b.registryConfig = reg
	}
	if b.forceNextLayer { b.force = true }
	return b
}

func (b *Builder) FromAWSRegistry(image, accessKeyID, secretAccessKey, region string) *Builder {
	b.baseImage = image
	b.baseTemplate = ""
	reg := &apiclient.FromImageRegistry{}
	if err := reg.FromAWSRegistry(apiclient.AWSRegistry{
		Type: "aws",
		AwsAccessKeyId: accessKeyID,
		AwsSecretAccessKey: secretAccessKey,
		AwsRegion: region,
	}); err != nil {
		b.err = err
		return b
	}
	b.registryConfig = reg
	if b.forceNextLayer { b.force = true }
	return b
}

func (b *Builder) FromGCPRegistry(image, serviceAccountJSON string) *Builder {
	b.baseImage = image
	b.baseTemplate = ""
	reg := &apiclient.FromImageRegistry{}
	if err := reg.FromGCPRegistry(apiclient.GCPRegistry{
		Type: "gcp",
		ServiceAccountJson: serviceAccountJSON,
	}); err != nil {
		b.err = err
		return b
	}
	b.registryConfig = reg
	if b.forceNextLayer { b.force = true }
	return b
}
```

Field names may differ — adjust to match `apiclient` generated types (grep them first).

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add FromImage credentials, FromAWSRegistry, FromGCPRegistry"
```

---

### Task 18: `FinalBuilder` for `SetStartCmd` / `SetReadyCmd` / `BetaSetDevContainerStart`

**Files:**
- Modify: `template/template.go`
- Create: `template/builder_final_test.go`

**Step 1: Write failing test**

```go
package template

import "testing"

func TestSetStartCmd_ReturnsFinal(t *testing.T) {
	fb := New().FromImage("alpine:3").SetStartCmd("run", WaitForPort(8000))
	var _ interface{ Build(_ ...any) } = fb // compile-time: FinalBuilder must exist
	// can't call Run on FinalBuilder — check via reflection that the type
	// doesn't export a Run method
	// (here we just ensure FinalBuilder has ToDockerfile)
	if _, err := fb.ToDockerfile(); err != nil {
		t.Fatal(err)
	}
}
```

**Step 2: Run test**

Run: `go test ./template/ -run TestSetStartCmd_ReturnsFinal`
Expected: FAIL.

**Step 3: Implement**

```go
type FinalBuilder struct{ b *Builder }

func (f *FinalBuilder) ToDockerfile() (string, error) { return f.b.ToDockerfile() }
func (f *FinalBuilder) ToJSON() (string, error)       { return f.b.ToJSON() }
// Build helpers delegate to Client elsewhere

func (b *Builder) SetStartCmd(cmd string, ready ReadyCmd) *FinalBuilder {
	b.startCmd = cmd
	b.readyCmd = ready.Cmd()
	return &FinalBuilder{b: b}
}

func (b *Builder) SetReadyCmd(ready ReadyCmd) *FinalBuilder {
	b.readyCmd = ready.Cmd()
	return &FinalBuilder{b: b}
}

func (b *Builder) BetaSetDevContainerStart(dir string) *FinalBuilder {
	if b.baseTemplate != "devcontainer" {
		b.err = &e2b.InvalidArgumentError{Message: "devcontainer start requires devcontainer base template"}
		return &FinalBuilder{b: b}
	}
	b.startCmd = "sudo devcontainer up --workspace-folder " + dir +
		" && sudo /prepare-exec.sh " + dir +
		" | sudo tee /devcontainer.sh > /dev/null && sudo chmod +x /devcontainer.sh && sudo touch /devcontainer.up"
	b.readyCmd = WaitForFile("/devcontainer.up").Cmd()
	return &FinalBuilder{b: b}
}
```

Update existing `SetStartCmd(cmd string, ready *ReadyCmd) *Builder` and `SetReadyCmd(ready ReadyCmd) *Builder` signatures. Adjust existing tests in `template_test.go` to the new signatures. Keep backward-compat shim if needed: remove old signatures cleanly.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS; old tests may fail — fix them to use the new signatures (remove `*ReadyCmd` pointer).

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Introduce FinalBuilder and return it from SetStartCmd/SetReadyCmd"
```

---

### Task 19: `ToJSON` + `FromDockerfileContent` / `FromDockerfileFile`

**Files:**
- Modify: `template/template.go`
- Create: `template/dockerfile_parser.go`
- Create: `template/dockerfile_parser_test.go`
- Create: `template/builder_json_test.go`

**Step 1: Write failing test**

```go
// template/builder_json_test.go
package template

import (
	"encoding/json"
	"testing"
)

func TestToJSON_IncludesFromImageAndSteps(t *testing.T) {
	b := New().FromImage("alpine:3").RunCmd("echo hi")
	raw, err := b.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if parsed["fromImage"] != "alpine:3" {
		t.Fatalf("fromImage: %v", parsed["fromImage"])
	}
	if _, ok := parsed["steps"]; !ok {
		t.Fatal("steps missing")
	}
}
```

```go
// template/dockerfile_parser_test.go
package template

import (
	"strings"
	"testing"
)

func TestFromDockerfileContent_SimpleFrom(t *testing.T) {
	b := New().FromDockerfileContent("FROM python:3.12\nRUN pip install numpy\n")
	if b.baseImage != "python:3.12" {
		t.Fatalf("base: %q", b.baseImage)
	}
	if len(b.instructions) != 1 || b.instructions[0].Args[0] != "pip install numpy" {
		t.Fatalf("instructions: %+v", b.instructions)
	}
}

func TestFromDockerfileFile_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/Dockerfile"
	if err := os.WriteFile(path, []byte("FROM alpine\nRUN apk add curl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := New().FromDockerfileFile(path)
	if b.baseImage != "alpine" {
		t.Fatalf("base: %q", b.baseImage)
	}
}

// helper import
var _ = strings.TrimSpace
```

Add `import "os"` where needed.

**Step 2: Run test**

Run: `go test ./template/ -run "TestToJSON|TestFromDockerfile"`
Expected: FAIL.

**Step 3: Implement**

```go
// ToJSON
func (b *Builder) ToJSON() (string, error) {
	body, err := b.serialize(false)
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
```

Create `template/dockerfile_parser.go` — a minimal parser covering the instructions we support (FROM, RUN, COPY, ENV, WORKDIR, USER, EXPOSE, ENTRYPOINT/CMD):

```go
package template

import (
	"bufio"
	"os"
	"strings"
)

func (b *Builder) FromDockerfileContent(content string) *Builder {
	return parseDockerfile(b, content)
}

func (b *Builder) FromDockerfileFile(path string) *Builder {
	data, err := os.ReadFile(path)
	if err != nil {
		b.err = err
		return b
	}
	return parseDockerfile(b, string(data))
}

func parseDockerfile(b *Builder, src string) *Builder {
	scanner := bufio.NewScanner(strings.NewReader(src))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, rest := splitInstruction(line)
		switch strings.ToUpper(head) {
		case "FROM":
			b.baseImage = rest
		case "RUN":
			b.RunCmd(rest)
		case "COPY":
			parts := strings.Fields(rest)
			if len(parts) >= 2 {
				b.Copy(parts[0], parts[1])
			}
		case "WORKDIR":
			b.SetWorkdir(rest)
		case "USER":
			b.SetUser(rest)
		case "ENV":
			kvs := strings.Fields(rest)
			envs := map[string]string{}
			for _, kv := range kvs {
				if eq := strings.IndexByte(kv, '='); eq > 0 {
					envs[kv[:eq]] = strings.Trim(kv[eq+1:], `"`)
				}
			}
			b.SetEnvs(envs)
		case "EXPOSE":
			// swallow — we serialize EXPOSE via Expose(port int) if ever needed
		}
	}
	return b
}

func splitInstruction(line string) (head, rest string) {
	space := strings.IndexByte(line, ' ')
	if space < 0 {
		return line, ""
	}
	return line[:space], strings.TrimSpace(line[space+1:])
}
```

Note: this parser is intentionally minimal (we don't need `HEALTHCHECK`, `LABEL`, etc.). If a Dockerfile uses unsupported instructions, those are silently skipped — add a TODO.

Update old `FromDockerfile` (raw string append) to redirect to `FromDockerfileContent` and deprecate its docstring.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Parse Dockerfiles and add ToJSON for template serialization"
```

---

## Phase 4 — Client + low-level API operations

### Task 20: `NewClient` plus `requestBuild`, `getFileUploadLink`, `uploadFile`, `triggerBuild`, `getBuildStatus`

**Files:**
- Create: `template/client.go`
- Create: `template/buildapi.go`
- Create: `template/client_test.go`

**Step 1: Write failing test** (using `httptest.Server`)

```go
package template

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eric642/e2b-go-sdk"
)

func TestClient_RequestBuild_PostsV3(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/templates" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","tags":[],"names":["demo"]}`))
	}))
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cli.requestBuild(nil, "demo", nil, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.TemplateID != "tpl_1" || got.BuildID != "bld_1" {
		t.Fatalf("response: %+v", got)
	}
	if body["name"] != "demo" {
		t.Fatalf("body: %v", body)
	}
	_ = bytes.Buffer{} // just to keep import
}
```

**Step 2: Run test**

Run: `go test ./template/ -run TestClient_RequestBuild`
Expected: FAIL.

**Step 3: Implement**

Create `template/client.go`:

```go
package template

import (
	"net/http"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

type Client struct {
	cfg     e2b.Config
	apiCli  *apiclient.Client
	httpCli *http.Client
}

func NewClient(cfg e2b.Config) (*Client, error) {
	resolved := cfg.Resolve() // if Resolve isn't exported, call cfg via e2b package trickery; else inline
	// NOTE: e2b.Config.resolve is unexported. Wire this via e2b.CreateOptions-like path.
	hc := resolved.HTTPClient()
	auth := transport.Auth{APIKey: resolved.APIKey, AccessToken: resolved.AccessToken, Headers: resolved.Headers}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, hc, auth)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: resolved, apiCli: apiCli, httpCli: hc}, nil
}
```

**Problem:** `e2b.Config.resolve()` and `e2b.Config.httpClient()` are unexported. Resolution:

- Add exported wrappers in `config.go`: `func (c Config) Resolve() Config { return c.resolve() }` and `func (c Config) HTTPClient() *http.Client { return c.httpClient() }`.
- Commit that first as part of this task.

After that, create `template/buildapi.go`:

```go
package template

import (
	"context"
	"errors"
	"io"
	"net/http"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

func (c *Client) requestBuild(ctx context.Context, name string, tags []string, cpu, mem int32) (*apiclient.TemplateRequestResponseV3, error) {
	body := apiclient.TemplateBuildRequestV3{Name: &name}
	if len(tags) > 0 {
		body.Tags = &tags
	}
	if cpu > 0 {
		cc := cpu
		body.CpuCount = (*apiclient.CPUCount)(&cc)
	}
	if mem > 0 {
		mm := mem
		body.MemoryMB = (*apiclient.MemoryMB)(&mm)
	}
	resp, err := c.apiCli.PostV3Templates(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "request", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePostV3TemplatesResponse(resp)
	if err != nil {
		return nil, err
	}
	if parsed.JSON200 == nil {
		return nil, &BuildError{Op: "request", Err: errors.New("empty response body")}
	}
	return parsed.JSON200, nil
}

func (c *Client) getFileUploadLink(ctx context.Context, templateID, hash string) (*apiclient.TemplateBuildFileUpload, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDFilesHash(ctx, apiclient.TemplateID(templateID), hash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &UploadError{Hash: hash, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDFilesHashResponse(resp)
	if err != nil {
		return nil, err
	}
	return parsed.JSON200, nil
}

func (c *Client) uploadFile(ctx context.Context, url string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &UploadError{Err: errors.New(resp.Status)}
	}
	return nil
}

func (c *Client) triggerBuild(ctx context.Context, templateID, buildID string, body apiclient.TemplateBuildStartV2) error {
	resp, err := c.apiCli.PostV2TemplatesTemplateIDBuildsBuildID(ctx, apiclient.TemplateID(templateID), apiclient.BuildID(buildID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "trigger", TemplateID: templateID, BuildID: buildID, Err: errors.New(resp.Status)}
	}
	return nil
}

func (c *Client) getBuildStatus(ctx context.Context, templateID, buildID string, logsOffset int) (*BuildStatus, error) {
	params := &apiclient.GetTemplatesTemplateIDBuildsBuildIDStatusParams{}
	if logsOffset > 0 {
		lo := logsOffset
		params.LogsOffset = &lo
	}
	resp, err := c.apiCli.GetTemplatesTemplateIDBuildsBuildIDStatus(ctx, apiclient.TemplateID(templateID), apiclient.BuildID(buildID), params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDBuildsBuildIDStatusResponse(resp)
	if err != nil {
		return nil, err
	}
	return mapBuildStatus(parsed.JSON200), nil
}

func mapBuildStatus(r *apiclient.TemplateBuildInfo) *BuildStatus {
	if r == nil { return nil }
	bs := &BuildStatus{
		TemplateID: r.TemplateID,
		BuildID:    r.BuildID,
		Status:     BuildStatusValue(r.Status),
	}
	for _, le := range r.LogEntries {
		bs.Logs = append(bs.Logs, LogEntry{Timestamp: le.Timestamp, Level: LogLevel(le.Level), Message: le.Message})
	}
	if r.Reason != nil {
		br := &BuildReason{}
		if r.Reason.Message != "" {
			br.Message = r.Reason.Message
		}
		if r.Reason.Step != nil {
			br.Step = *r.Reason.Step
		}
		for _, le := range r.Reason.LogEntries {
			br.Logs = append(br.Logs, LogEntry{Timestamp: le.Timestamp, Level: LogLevel(le.Level), Message: le.Message})
		}
		bs.Reason = br
	}
	return bs
}
```

Adjust field names in `mapBuildStatus` to match the generated types (grep first).

**Step 4: Run tests**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add config.go template/client.go template/buildapi.go template/client_test.go
git commit -m "Add template.Client low-level API wrappers"
```

---

### Task 21: Tags + Exists + Delete Client methods

**Files:**
- Modify: `template/client.go` (or split into `template/tags.go`)
- Create: `template/tags_test.go` (httptest)

**Step 1: Write failing test**

```go
package template

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eric642/e2b-go-sdk"
)

func TestClient_Exists_True(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"templateID": "tpl_1"})
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	ok, err := cli.Exists(context.Background(), "my-alias")
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
}

// TestClient_Exists_False (404), TestClient_AssignTags, TestClient_RemoveTags,
// TestClient_GetTags, TestClient_Delete — similar scaffold.
```

**Step 2: Run test**

Run: `go test ./template/ -run "TestClient_Exists|TestClient_AssignTags|TestClient_RemoveTags|TestClient_GetTags|TestClient_Delete"`
Expected: FAIL.

**Step 3: Implement**

```go
func (c *Client) Exists(ctx context.Context, alias string) (bool, error) {
	resp, err := c.apiCli.GetTemplatesAliasesAlias(ctx, alias)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		return true, nil
	}
	if resp.StatusCode >= 300 {
		return false, &BuildError{Op: "exists", Err: errors.New(resp.Status)}
	}
	return true, nil
}

func (c *Client) AssignTags(ctx context.Context, target string, tags []string) (*TagInfo, error) {
	body := apiclient.AssignTemplateTagsRequest{Target: target, Tags: tags}
	resp, err := c.apiCli.PostTemplatesTags(ctx, body)
	if err != nil { return nil, err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "tags.assign", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePostTemplatesTagsResponse(resp)
	if err != nil { return nil, err }
	return &TagInfo{BuildID: parsed.JSON200.BuildID.String(), Tags: parsed.JSON200.Tags}, nil
}

func (c *Client) RemoveTags(ctx context.Context, name string, tags []string) error {
	body := apiclient.DeleteTemplateTagsRequest{Name: name, Tags: tags}
	resp, err := c.apiCli.DeleteTemplatesTags(ctx, body)
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "tags.remove", Err: errors.New(resp.Status)}
	}
	return nil
}

func (c *Client) GetTags(ctx context.Context, templateID string) ([]TemplateTag, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDTags(ctx, apiclient.TemplateID(templateID))
	if err != nil { return nil, err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "tags.get", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsResponse(resp)
	if err != nil { return nil, err }
	out := make([]TemplateTag, 0, len(*parsed.JSON200))
	for _, it := range *parsed.JSON200 {
		out = append(out, TemplateTag{Tag: it.Tag, BuildID: it.BuildID.String(), CreatedAt: it.CreatedAt})
	}
	return out, nil
}

func (c *Client) Delete(ctx context.Context, templateID string) error {
	resp, err := c.apiCli.DeleteTemplatesTemplateID(ctx, apiclient.TemplateID(templateID))
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "delete", TemplateID: templateID, Err: errors.New(resp.Status)}
	}
	return nil
}
```

Adjust operation/method names to actual generated signatures.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Add Exists/AssignTags/RemoveTags/GetTags/Delete client methods"
```

---

### Task 22: Build orchestration — `BuildStream`, `Build`, `BuildInBackground`, `GetBuildStatus`

**Files:**
- Modify: `template/client.go` (add build orchestration)
- Create: `template/build_test.go` (httptest covering the full flow)

**Step 1: Write failing test**

```go
package template

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eric642/e2b-go-sdk"
)

func TestBuildStream_HappyPath(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v3/templates":
			_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","tags":[],"names":["demo"]}`))
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v2/templates/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","status":"ready","logEntries":[],"logs":[]}`))
		default:
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	events, err := cli.BuildStream(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var done *BuildInfo
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("err: %v", ev.Err)
		}
		if ev.Done != nil {
			done = ev.Done
		}
	}
	if done == nil || done.TemplateID != "tpl_1" {
		t.Fatalf("done: %+v", done)
	}
	// ensure request-build and trigger-build both called
	foundV3, foundV2 := false, false
	for _, c := range calls {
		if strings.Contains(c, "/v3/templates") { foundV3 = true }
		if strings.Contains(c, "/v2/templates/") { foundV2 = true }
	}
	if !foundV3 || !foundV2 {
		t.Fatalf("missing calls: %v", calls)
	}
	_ = io.Discard
	_ = json.Marshal
}
```

Add additional tests: `TestBuild_SynchronousWraps` (drain events), `TestBuildStream_CtxCancel`, `TestBuildStream_ErrorStatus`, `TestBuildInBackground_NoPoll`.

**Step 2: Run test**

Run: `go test ./template/ -run "TestBuildStream|TestBuild_|TestBuildInBackground"`
Expected: FAIL.

**Step 3: Implement**

Add to `template/client.go`:

```go
const defaultPollInterval = 200 * time.Millisecond
const defaultCPU = 2
const defaultMemoryMB = 1024
const logTailLimit = 20

type BuildOptions struct {
	Name         string
	Tags         []string
	CPUCount     int32
	MemoryMB     int32
	SkipCache    bool
	PollInterval time.Duration
}

func (c *Client) Build(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	ch, err := c.BuildStream(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	var done *BuildInfo
	for ev := range ch {
		if ev.Err != nil {
			return nil, ev.Err
		}
		if ev.Done != nil {
			done = ev.Done
		}
	}
	if done == nil {
		return nil, &BuildError{Op: "poll", Err: errors.New("channel closed without terminal event")}
	}
	return done, nil
}

func (c *Client) BuildInBackground(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	info, _, err := c.startBuild(ctx, b, opts)
	return info, err
}

func (c *Client) BuildStream(ctx context.Context, b *Builder, opts BuildOptions) (<-chan BuildEvent, error) {
	info, body, err := c.startBuild(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	events := make(chan BuildEvent, 16)
	go c.pollUntilDone(ctx, info, interval, events)
	_ = body
	return events, nil
}

// startBuild runs steps 1–5: validate → request → upload → trigger.
// Returns BuildInfo + serialized body for status mapping.
func (c *Client) startBuild(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, *apiclient.TemplateBuildStartV2, error) {
	if opts.Name == "" {
		return nil, nil, &e2b.InvalidArgumentError{Message: "BuildOptions.Name is required"}
	}
	cpu := opts.CPUCount
	if cpu == 0 {
		cpu = defaultCPU
	}
	mem := opts.MemoryMB
	if mem == 0 {
		mem = defaultMemoryMB
	}

	// Request build
	reqResp, err := c.requestBuild(ctx, opts.Name, opts.Tags, cpu, mem)
	if err != nil {
		return nil, nil, err
	}

	// Compute hashes + identify uploads
	steps, err := b.instructionsWithHashes()
	if err != nil {
		return nil, nil, err
	}
	for _, s := range steps {
		if s.Type != instTypeCopy {
			continue
		}
		link, err := c.getFileUploadLink(ctx, reqResp.TemplateID, s.FilesHash)
		if err != nil {
			return nil, nil, err
		}
		shouldUpload := (link.Url != nil) && (!link.Present || (s.HasForceUpload && s.ForceUpload))
		if !shouldUpload {
			continue
		}
		resolve := defaultResolveSymlinks
		if s.ResolveSymlinks != nil {
			resolve = *s.ResolveSymlinks
		}
		ignores := append([]string{}, b.ignorePatterns...)
		ignores = append(ignores, readDockerignore(b.contextDir)...)
		body, errc := tarFileStream(s.Args[0], b.contextDir, ignores, resolve)
		if err := c.uploadFile(ctx, *link.Url, body); err != nil {
			<-errc
			return nil, nil, &UploadError{Src: s.Args[0], Hash: s.FilesHash, Err: err}
		}
		if err := <-errc; err != nil {
			return nil, nil, &UploadError{Src: s.Args[0], Hash: s.FilesHash, Err: err}
		}
	}

	// Trigger
	body, err := b.serialize(opts.SkipCache)
	if err != nil {
		return nil, nil, err
	}
	if err := c.triggerBuild(ctx, reqResp.TemplateID, reqResp.BuildID, *body); err != nil {
		return nil, nil, err
	}

	return &BuildInfo{
		TemplateID: reqResp.TemplateID,
		BuildID:    reqResp.BuildID,
		Name:       opts.Name,
		Tags:       append([]string{}, reqResp.Tags...),
	}, body, nil
}

func (c *Client) pollUntilDone(ctx context.Context, info *BuildInfo, interval time.Duration, out chan<- BuildEvent) {
	defer close(out)
	var (
		logsOffset int
		tail       []LogEntry
	)
	emitLog := func(le LogEntry) {
		tail = append(tail, le)
		if len(tail) > logTailLimit {
			tail = tail[len(tail)-logTailLimit:]
		}
		out <- BuildEvent{Log: &le}
	}
	for {
		select {
		case <-ctx.Done():
			out <- BuildEvent{Err: ctx.Err()}
			return
		default:
		}
		status, err := c.getBuildStatus(ctx, info.TemplateID, info.BuildID, logsOffset)
		if err != nil {
			out <- BuildEvent{Err: err}
			return
		}
		logsOffset += len(status.Logs)
		for _, le := range status.Logs {
			emitLog(le)
		}
		switch status.Status {
		case BuildStatusReady:
			out <- BuildEvent{Done: info}
			return
		case BuildStatusError:
			msg := ""
			step := ""
			if status.Reason != nil {
				msg = status.Reason.Message
				step = status.Reason.Step
			}
			out <- BuildEvent{Err: &BuildError{
				Op:         "poll",
				TemplateID: info.TemplateID,
				BuildID:    info.BuildID,
				Step:       step,
				Message:    msg,
				LogTail:    tail,
			}}
			return
		}
		select {
		case <-ctx.Done():
			out <- BuildEvent{Err: ctx.Err()}
			return
		case <-time.After(interval):
		}
	}
}

// GetBuildStatus is the public wrapper for polling externally.
func (c *Client) GetBuildStatus(ctx context.Context, info BuildInfo, logsOffset int) (*BuildStatus, error) {
	return c.getBuildStatus(ctx, info.TemplateID, info.BuildID, logsOffset)
}
```

Also remove the now-obsolete `Builder.Build()` (which returned `ErrNotImplemented`). Update `template_test.go` to remove `TestBuildNotImplemented`.

**Step 4: Run**

Run: `go test ./template/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
git add template/*.go
git commit -m "Orchestrate template build: stream, sync, and background variants"
```

---

## Phase 5 — Example + integration

### Task 23: `examples/template/main.go`

**Files:**
- Create: `examples/template/main.go`
- Create: `examples/template/run.sh`

**Step 1: Write example**

```go
// Example: build a simple template and start a sandbox from it.
//
// Usage:
//   source ./.env && go run ./examples/template
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/template"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tpl := template.New().
		FromDebianImage("bookworm").
		RunCmd("apt-get update && apt-get install -y curl").
		SetStartCmd("sleep infinity", template.WaitForTimeoutMs(1000))

	cli, err := template.NewClient(e2b.Config{})
	if err != nil {
		log.Fatal(err)
	}
	events, err := cli.BuildStream(ctx, tpl, template.BuildOptions{Name: "go-sdk-demo:latest"})
	if err != nil {
		log.Fatal(err)
	}
	var info *template.BuildInfo
	for ev := range events {
		switch {
		case ev.Log != nil:
			fmt.Printf("[%s] %s\n", ev.Log.Level, ev.Log.Message)
		case ev.Err != nil:
			log.Fatal(ev.Err)
		case ev.Done != nil:
			info = ev.Done
		}
	}
	fmt.Printf("template built: %s\n", info.TemplateID)

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{Template: info.TemplateID})
	if err != nil {
		log.Fatal(err)
	}
	defer sbx.Kill(ctx)
	fmt.Printf("sandbox id: %s\n", sbx.ID)
}
```

Create `examples/template/run.sh` modeled on `examples/basic/run.sh`.

**Step 2: Verify build**

Run: `go build ./examples/template/`
Expected: success.

**Step 3: Commit**

```bash
git add examples/template/
git commit -m "Add template build example"
```

---

### Task 24: Integration tests

**Files:**
- Create: `template/client_integration_test.go`

**Step 1: Write tests**

```go
package template

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

func skipIfNoAPIKey(t *testing.T) {
	if os.Getenv("E2B_API_KEY") == "" {
		t.Skip("E2B_API_KEY not set; skipping integration test")
	}
}

func TestIntegrationBuildSimpleDebian(t *testing.T) {
	skipIfNoAPIKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cli, err := NewClient(e2b.Config{})
	if err != nil { t.Fatal(err) }

	tpl := New().FromDebianImage("bookworm").RunCmd("echo ok").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	info, err := cli.Build(ctx, tpl, BuildOptions{Name: "go-sdk-test-" + strings.ToLower(t.Name())})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), info.TemplateID)
	})

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{Template: info.TemplateID, Timeout: 60 * time.Second})
	if err != nil { t.Fatal(err) }
	defer sbx.Kill(ctx)
	if sbx.ID == "" {
		t.Fatal("no sandbox id")
	}
}

func TestIntegrationBuildWithCopyCaches(t *testing.T) {
	skipIfNoAPIKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/data.txt", []byte("hello"), 0o644); err != nil { t.Fatal(err) }

	cli, _ := NewClient(e2b.Config{})
	tpl := New().FromDebianImage("bookworm").
		WithContext(dir).Copy("data.txt", "/work/").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	name := "go-sdk-test-" + strings.ToLower(t.Name())
	info1, err := cli.Build(ctx, tpl, BuildOptions{Name: name})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = cli.Delete(context.Background(), info1.TemplateID) })

	// second build: should hit upload cache (files_hash present=true)
	info2, err := cli.Build(ctx, tpl, BuildOptions{Name: name})
	if err != nil { t.Fatal(err) }
	if info2.TemplateID == "" {
		t.Fatal("second build returned empty id")
	}
}

func TestIntegrationTags(t *testing.T) {
	skipIfNoAPIKey(t)
	// build a template, then assign/get/remove tags
	// ... (same pattern)
}

func TestIntegrationExists(t *testing.T) {
	skipIfNoAPIKey(t)
	cli, _ := NewClient(e2b.Config{})
	ok, err := cli.Exists(context.Background(), "non-existent-"+time.Now().Format("150405"))
	if err != nil { t.Fatal(err) }
	if ok { t.Fatal("should not exist") }
}
```

**Step 2: Run unit tests (integration skips automatically)**

Run: `go test ./template/ -v`
Expected: unit PASS; integration SKIP when no API key.

**Step 3: Verify integration runs (optional — only if E2B_API_KEY set locally)**

Run: `E2B_API_KEY=... go test ./template/ -run Integration -timeout 15m -v`
Expected: PASS.

**Step 4: Commit**

```bash
git add template/client_integration_test.go
git commit -m "Add template build integration tests with cleanup"
```

---

### Task 25: Reconcile Python-frozen hash goldens

**Files:**
- Modify: `template/testdata/hash/*.hash` (only if a Python reference is available)

**Step 1: Run Python reference**

On a machine with the Python SDK installed:

```bash
python - <<'PY'
import sys, pathlib
sys.path.insert(0, str(pathlib.Path("E2B/packages/python-sdk").resolve()))
from e2b.template.utils import calculate_files_hash
cases = [("single", "app.txt", "/app/", False),
         ("nested", "a", "/opt/", False),
         ("ignored", ".", "/work/", False)]
for name, src, dest, rs in cases:
    h = calculate_files_hash(src, dest, f"template/testdata/hash/{name}",
                             [], rs, None) if name != "ignored" else \
        calculate_files_hash(src, dest, f"template/testdata/hash/{name}",
                             ["*.log"], rs, None)
    print(name, h)
PY
```

**Step 2: Diff outputs**

If the Python hash differs from `template/testdata/hash/<name>.hash`, update the golden file to match Python.

**Step 3: Re-run Go test**

Run: `go test ./template/ -run TestCalculateFilesHash_Golden -v`
Expected: PASS — Go matches Python byte-for-byte.

**Step 4: Fix algorithm discrepancies (if any)**

If Go can't reproduce Python's hash, inspect `calculateFilesHash` vs `calculate_files_hash`:
- Order of file set (absolute path sort).
- Stat formatting (mode as decimal `uint32`, size as decimal `int64`).
- Empty `user`/`mode` fields in COPY args — Python hashes `"COPY <src> <dest>"` only; those are not in the hash preamble.

**Step 5: Commit**

```bash
git add template/testdata/hash/
git commit -m "Align file hash goldens with Python SDK reference"
```

---

## Verification Summary

After all tasks:

1. `make fmt vet test` — passes locally.
2. `go build ./examples/template/` — compiles.
3. `E2B_API_KEY=... go test ./template/ -run Integration -timeout 15m` — passes (if a key is available).
4. `make sync-spec` + `make codegen` still idempotent (no template code relies on private generated helpers that might be renamed).

## Execution Handoff

Plan saved to `docs/plans/2026-04-24-template-build-plan.md`. Two options:

1. **Subagent-Driven (this session)** — I dispatch a fresh subagent per task, review between tasks; fast iteration in one place.
2. **Parallel Session (separate)** — you open a new session in this directory with the executing-plans skill; batch execution with checkpoints.

Which?
