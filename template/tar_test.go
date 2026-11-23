package template

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestTarFileToTemp_IncludesContextRelativeAndCleansUp(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "hello/a.txt", "a")
	writeFile(t, ctx, "hello/b.txt", "b")

	f, size, cleanup, err := tarFileToTemp("hello", ctx, nil, false)
	if err != nil {
		t.Fatalf("tarFileToTemp: %v", err)
	}
	tmpPath := f.Name()
	if size <= 0 {
		cleanup()
		t.Fatalf("expected positive archive size, got %d", size)
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		cleanup()
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = gz.Close()
			cleanup()
			t.Fatalf("tar: %v", err)
		}
		names[filepath.ToSlash(hdr.Name)] = true
	}
	if err := gz.Close(); err != nil {
		cleanup()
		t.Fatalf("gzip close: %v", err)
	}
	cleanup()

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected temp file %s to be removed on cleanup, stat err=%v", tmpPath, err)
	}
	if !names["hello/a.txt"] || !names["hello/b.txt"] {
		t.Fatalf("missing entries: %#v", names)
	}
}

func TestTarFileToTemp_RespectsIgnoreAndNegation(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "keep.txt", "k")
	writeFile(t, ctx, "skip.log", "s")
	writeFile(t, ctx, "node_modules/pkg/index.js", "js")
	writeFile(t, ctx, "dist/bundle.js", "b")
	writeFile(t, ctx, "dist/keep.txt", "dk")

	f, _, cleanup, err := tarFileToTemp(".", ctx, []string{"*.log", "node_modules", "dist/", "!dist/keep.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	saw := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		saw[filepath.ToSlash(hdr.Name)] = true
	}
	if saw["skip.log"] || saw["node_modules/pkg/index.js"] || saw["dist/bundle.js"] {
		t.Fatalf("ignored files made it into tar: %v", saw)
	}
	if !saw["keep.txt"] || !saw["dist/keep.txt"] {
		t.Fatalf("expected keep.txt and dist/keep.txt to be present: %v", saw)
	}
}

func TestTarFileToTemp_BrokenSymlinkFallsBackWhenResolving(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "real.txt", "hello")
	if err := os.Symlink("real.txt", filepath.Join(ctx, "good.link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Symlink("does-not-exist", filepath.Join(ctx, "broken.link")); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}

	f, _, cleanup, err := tarFileToTemp(".", ctx, nil, true)
	if err != nil {
		t.Fatalf("tarFileToTemp with broken symlink failed: %v", err)
	}
	defer cleanup()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	types := map[string]byte{}
	linkNames := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.ToSlash(hdr.Name)
		types[name] = hdr.Typeflag
		linkNames[name] = hdr.Linkname
	}
	if types["good.link"] != tar.TypeReg {
		t.Fatalf("good.link should be resolved to regular file, got type %v", types["good.link"])
	}
	if types["broken.link"] != tar.TypeSymlink || linkNames["broken.link"] != "does-not-exist" {
		t.Fatalf("broken.link should fall back to symlink header, got type=%v target=%q",
			types["broken.link"], linkNames["broken.link"])
	}
}
