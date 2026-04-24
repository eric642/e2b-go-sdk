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
	writeFile(t, ctx, "a.txt", "a")
	writeFile(t, ctx, "b.txt", "b")

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
	writeFile(t, ctx, "keep.txt", "x")
	writeFile(t, ctx, "skip.log", "x")
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

func TestGetAllFilesInPath_SortedByAbsPath(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "z.txt", "z")
	writeFile(t, ctx, "a.txt", "a")
	writeFile(t, ctx, "m.txt", "m")
	got, err := getAllFilesInPath("*.txt", ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(got) {
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
