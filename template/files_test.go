package template

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestGetAllFilesInPath_FlatMatch(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "a.txt", "a")
	writeFile(t, ctx, "b.txt", "b")

	got, err := getAllFilesInPath("*.txt", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(ctx, "a.txt"), filepath.Join(ctx, "b.txt")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGetAllFilesInPath_RecursesDirectory(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "dir/a.txt", "a")
	writeFile(t, ctx, "dir/sub/b.txt", "b")

	got, err := getAllFilesInPath("dir", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	expectContains := []string{
		filepath.Join(ctx, "dir"),
		filepath.Join(ctx, "dir", "a.txt"),
		filepath.Join(ctx, "dir", "sub"),
		filepath.Join(ctx, "dir", "sub", "b.txt"),
	}
	for _, w := range expectContains {
		if !slices.Contains(got, w) {
			t.Fatalf("missing %q in %v", w, got)
		}
	}

	gotNoDirs, err := getAllFilesInPath("dir", ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	wantFilesOnly := []string{
		filepath.Join(ctx, "dir", "a.txt"),
		filepath.Join(ctx, "dir", "sub", "b.txt"),
	}
	if !reflect.DeepEqual(gotNoDirs, wantFilesOnly) {
		t.Fatalf("includeDirs=false got %v want %v", gotNoDirs, wantFilesOnly)
	}
}

func TestGetAllFilesInPath_DotSlashPrefixAndBracketDirNames(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "src/index.ts", "export {}")
	writeFile(t, ctx, "pages/[id]/page.tsx", "export default function Page() {}")
	writeFile(t, ctx, "pages/[...slug]/route.ts", "export function GET() {}")

	gotDotSlash, err := getAllFilesInPath("./src", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gotDotSlash, filepath.Join(ctx, "src", "index.ts")) {
		t.Fatalf("./src failed to match src/index.ts: %v", gotDotSlash)
	}

	gotBrackets, err := getAllFilesInPath("./pages", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"pages",
		filepath.Join("pages", "[id]"),
		filepath.Join("pages", "[id]", "page.tsx"),
		filepath.Join("pages", "[...slug]"),
		filepath.Join("pages", "[...slug]", "route.ts"),
	} {
		want := filepath.Join(ctx, rel)
		if !slices.Contains(gotBrackets, want) {
			t.Fatalf("missing %q in %v", want, gotBrackets)
		}
	}
}

func TestGetAllFilesInPath_AppliesDockerignoreDirAndNegation(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "keep.txt", "x")
	writeFile(t, ctx, "skip.log", "x")
	writeFile(t, ctx, "secret.txt", "s")
	writeFile(t, ctx, "node_modules/pkg/x.js", "js")
	writeFile(t, ctx, "dist/bundle.js", "b")
	writeFile(t, ctx, "dist/keep.txt", "k")

	got, err := getAllFilesInPath(".", ctx, []string{
		"*.log",
		"node_modules",
		"dist/",
		"/secret.txt",
		"!dist/keep.txt",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		filepath.Join(ctx, "skip.log"),
		filepath.Join(ctx, "secret.txt"),
		filepath.Join(ctx, "node_modules"),
		filepath.Join(ctx, "node_modules", "pkg", "x.js"),
		filepath.Join(ctx, "dist"),
		filepath.Join(ctx, "dist", "bundle.js"),
	} {
		if slices.Contains(got, forbidden) {
			t.Fatalf("ignored path %q leaked into results: %v", forbidden, got)
		}
	}
	for _, expected := range []string{
		ctx,
		filepath.Join(ctx, "keep.txt"),
		filepath.Join(ctx, "dist", "keep.txt"),
	} {
		if !slices.Contains(got, expected) {
			t.Fatalf("expected path %q missing from results: %v", expected, got)
		}
	}
}

func TestGetAllFilesInPath_RejectsEmptySrcAndBadGlob(t *testing.T) {
	ctx := t.TempDir()
	if _, err := getAllFilesInPath("", ctx, nil, true); err == nil {
		t.Fatal("expected error for empty src")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("empty src: want *e2b.InvalidArgumentError, got %T: %v", err, err)
	}

	if _, err := getAllFilesInPath("[unclosed", ctx, nil, true); err == nil {
		t.Fatal("expected error for invalid glob")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("bad glob: want *e2b.InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestGetAllFilesInPath_SortedByAbsPath(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "z.txt", "z")
	writeFile(t, ctx, "a.txt", "a")
	writeFile(t, ctx, "m.txt", "m")
	got, err := getAllFilesInPath("*.txt", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("not sorted ascending: %v", got)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
