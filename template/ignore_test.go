package template

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestReadDockerignore_SkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	contents := "# comment\n\nnode_modules\n  dist/\n#another\n\n"
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDockerignore(dir)
	want := []string{"node_modules", "dist/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReadDockerignore_StripsUTF8BOMAndCRLF(t *testing.T) {
	dir := t.TempDir()
	contents := "\uFEFF# comment\r\nnode_modules\r\ndist/\r\n!dist/keep.txt\r\n"
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDockerignore(dir)
	want := []string{"node_modules", "dist/", "!dist/keep.txt"}
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

func TestIgnoreMatcher_DockerignoreSemantics(t *testing.T) {
	patterns := []string{
		"node_modules",
		"dist/",
		"/secret.txt",
		"*.log",
		"!dist/keep.txt",
		"dist/keep/sub.txt",
		"!dist/keep/",
		"dist/keep/blocked.txt",
	}
	m, err := newIgnoreMatcher(patterns, "")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{".", false},
		{"keep.txt", false},
		{"secret.txt", true},
		{"sub/secret.txt", false},
		{"app.log", true},
		{"sub/app.log", false},
		{"node_modules", true},
		{"node_modules/pkg/index.js", true},
		{"dist", true},
		{"dist/bundle.js", true},
		{"dist/keep.txt", false},
		{"dist/keep", false},
		{"dist/keep/ok.txt", false},
		{"dist/keep/blocked.txt", true},
	}
	for _, tc := range cases {
		if got := m.matches(tc.path); got != tc.want {
			t.Errorf("matches(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}

	if m.mayMatchUnder("node_modules") {
		t.Error("mayMatchUnder(node_modules) should be false when no ! rule targets node_modules")
	}
	if !m.mayMatchUnder("dist") {
		t.Error("mayMatchUnder(dist) should be true because !dist/keep.txt exists")
	}
}

func TestIgnoreMatcher_InvalidPatternsReturnInvalidArgumentError(t *testing.T) {
	for _, pat := range []string{"!", "!   ", "[unclosed"} {
		_, err := newIgnoreMatcher([]string{pat}, "")
		if err == nil {
			t.Fatalf("pattern %q: expected error", pat)
		}
		if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
			t.Fatalf("pattern %q: want *e2b.InvalidArgumentError, got %T: %v", pat, err, err)
		}
	}
}
