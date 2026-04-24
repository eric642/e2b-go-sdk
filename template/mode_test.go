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
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := goModeToPosix(info.Mode())
	if got != 0o100644 {
		t.Fatalf("got %o want %o", got, 0o100644)
	}
}

func TestGoModeToPosix_Directory(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := goModeToPosix(info.Mode())
	if got&0o170000 != 0o040000 {
		t.Fatalf("not a directory mode: %o", got)
	}
}

func TestGoModeToPosix_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	got := goModeToPosix(info.Mode())
	if got&0o170000 != 0o120000 {
		t.Fatalf("not a symlink mode: %o", got)
	}
}

// compile check for fs import
var _ fs.FileMode = os.ModeSymlink
