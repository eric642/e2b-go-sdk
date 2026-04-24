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
	writeFile(t, ctx, "hello/a.txt", "a")
	writeFile(t, ctx, "hello/b.txt", "b")

	r, errc := tarFileStream("hello", ctx, nil, false)
	defer r.Close()

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

func TestTarFileStream_RespectsIgnore(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "keep.txt", "k")
	writeFile(t, ctx, "skip.log", "s")

	r, errc := tarFileStream(".", ctx, []string{"*.log"}, false)
	defer r.Close()

	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	saw := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		saw[filepath.Base(hdr.Name)] = true
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if saw["skip.log"] {
		t.Fatalf("ignored file made it into the tar: %v", saw)
	}
	if !saw["keep.txt"] {
		t.Fatalf("expected keep.txt to be present: %v", saw)
	}
}
