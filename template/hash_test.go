package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalculateFilesHash_Golden(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		dest    string
		ignores []string
	}{
		{name: "single", src: "app.txt", dest: "/app/"},
		{name: "nested", src: "a", dest: "/opt/"},
		{name: "ignored", src: ".", dest: "/work/"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			ctxDir := filepath.Join("testdata", "hash", c.name)
			ignores := append([]string{}, c.ignores...)
			ignores = append(ignores, readDockerignore(ctxDir)...)
			got, err := calculateFilesHash(c.src, c.dest, ctxDir, ignores, false)
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
