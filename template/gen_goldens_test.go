//go:build generate_goldens

package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateGoldens(t *testing.T) {
	cases := []struct {
		name, src, dest string
	}{
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
