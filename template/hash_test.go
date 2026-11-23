package template

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestCalculateFilesHash(t *testing.T) {
	ctxDir := filepath.Join("testdata", "hash", "ignored")

	t.Run("deterministic", func(t *testing.T) {
		a, err := calculateFilesHash(".", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := calculateFilesHash(".", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("non-deterministic: %s vs %s", a, b)
		}
	})

	t.Run("src affects hash", func(t *testing.T) {
		a, err := calculateFilesHash(".", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := calculateFilesHash("keep.txt", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatal("different src produced identical hash")
		}
	})

	t.Run("dest affects hash", func(t *testing.T) {
		a, err := calculateFilesHash(".", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := calculateFilesHash(".", "/opt/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatal("different dest produced identical hash")
		}
	})

	t.Run("ignore pattern affects hash", func(t *testing.T) {
		unfiltered, err := calculateFilesHash(".", "/work/", ctxDir, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		filtered, err := calculateFilesHash(".", "/work/", ctxDir, []string{"*.log"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if unfiltered == filtered {
			t.Fatal("*.log ignore pattern had no effect on hash")
		}
	})

	t.Run("content change affects hash", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "app.txt", "hello")
		before, err := calculateFilesHash("app.txt", "/app/", tmp, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		after, err := calculateFilesHash("app.txt", "/app/", tmp, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if before == after {
			t.Fatal("content mutation did not change hash")
		}
	})
}

func TestCalculateFilesHash_UpstreamGoldenParity(t *testing.T) {
	// Expected SHA-256 digests verified directly against upstream E2B Python
	// (utils.py::calculate_files_hash) and JS (dockerfile-utils-js PatternMatcher)
	// with normalized 0755/0644 permissions.
	cases := []struct {
		name  string
		src   string
		dest  string
		want  string
		files map[string]string
	}{
		{
			name: "single",
			src:  "app.txt",
			dest: "/app/",
			want: "0486b3f93fcb6311a89d3a0adffdee4eee2f39f8bdf3d8e064e4cfae4e974c94",
		},
		{
			name: "nested",
			src:  "a",
			dest: "/opt/",
			want: "a60acebbc529f06f600aa7d9728fbad622de8adcb5c33c3295e58961168f5530",
		},
		{
			name: "ignored",
			src:  ".",
			dest: "/work/",
			want: "a84bccacc0b23209a16ff4162264b7039259da162a62286862f3859b55837a75",
		},
		{
			name: "dockerignore",
			src:  ".",
			dest: "/work/",
			want: "9ab0e444d92029f39b4aa2d6088d10d0fb739739022943926e8c96ddd1fd5756",
			files: map[string]string{
				".dockerignore":             "node_modules\ndist/\n/secret.txt\n!dist/keep.txt\n",
				"keep.txt":                  "keep",
				"secret.txt":                "secret",
				"dist/bundle.js":            "bundle",
				"dist/keep.txt":             "re-included",
				"node_modules/pkg/index.js": "pkg",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Normalize 0755/0644 permissions in a temp dir so host git umask
			// differences cannot make mode bits diverge.
			var dst string
			if tc.files != nil {
				dst = writeFixtureNormalized(t, tc.files)
			} else {
				dst = copyFixtureNormalized(t, filepath.Join("testdata", "hash", tc.name))
			}
			pats := readDockerignore(dst)
			got, err := calculateFilesHash(tc.src, tc.dest, dst, pats, false)
			if err != nil {
				t.Fatalf("calculateFilesHash: %v", err)
			}
			if got != tc.want {
				t.Fatalf("fixture %s hash mismatch:\n got  %s\n want %s", tc.name, got, tc.want)
			}
		})
	}
}

func writeFixtureNormalized(t *testing.T, files map[string]string) string {
	t.Helper()
	dstRoot := t.TempDir()
	for rel, content := range files {
		target := filepath.Join(dstRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := filepath.WalkDir(dstRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return os.Chmod(p, 0o755)
		}
		return os.Chmod(p, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dstRoot
}

func copyFixtureNormalized(t *testing.T, srcRoot string) string {
	t.Helper()
	dstRoot := t.TempDir()
	if err := os.Chmod(dstRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			return os.Chmod(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		return os.Chmod(target, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dstRoot
}

type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

func TestWriteStats_OmitsDirectorySize(t *testing.T) {
	var apfs, ext4 bytes.Buffer
	if err := writeStats(&apfs, fakeFileInfo{name: "dir", size: 64, mode: fs.ModeDir | 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := writeStats(&ext4, fakeFileInfo{name: "dir", size: 4096, mode: fs.ModeDir | 0o755}); err != nil {
		t.Fatal(err)
	}
	if apfs.String() != ext4.String() {
		t.Fatalf("directory size must not affect writeStats: apfs=%q ext4=%q", apfs.String(), ext4.String())
	}

	var f1, f2 bytes.Buffer
	if err := writeStats(&f1, fakeFileInfo{name: "a.txt", size: 10, mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if err := writeStats(&f2, fakeFileInfo{name: "a.txt", size: 20, mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if f1.String() == f2.String() {
		t.Fatalf("regular file size must affect writeStats: both=%q", f1.String())
	}
}

func TestCalculateFilesHash_EmptyMatchErrorsAsE2BError(t *testing.T) {
	_, err := calculateFilesHash("no-such", "/x", t.TempDir(), nil, false)
	if err == nil {
		t.Fatal("expected error for empty match")
	}
	if _, ok := errors.AsType[e2b.Error](err); !ok {
		t.Fatalf("empty match error must satisfy e2b.Error, got %T: %v", err, err)
	}
	if _, ok := errors.AsType[*BuildError](err); !ok {
		t.Fatalf("empty match error should be *BuildError, got %T: %v", err, err)
	}
}
