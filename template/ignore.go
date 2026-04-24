package template

import (
	"os"
	"path/filepath"
	"strings"
)

// readDockerignore returns the non-comment, non-empty lines of
// <contextDir>/.dockerignore. A missing file yields an empty slice.
func readDockerignore(contextDir string) []string {
	data, err := os.ReadFile(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
