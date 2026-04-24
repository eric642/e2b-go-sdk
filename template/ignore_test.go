package template

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func TestReadDockerignore_MissingFileReturnsEmpty(t *testing.T) {
	got := readDockerignore(t.TempDir())
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}
