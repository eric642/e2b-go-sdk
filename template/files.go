package template

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

// getAllFilesInPath returns absolute paths of entries matching `src` (a glob
// relative to contextDir), applying ignorePatterns and recursing into
// directories. Results are sorted ascending by absolute path — this matches
// Python's sorted(files) behavior and is required for byte-identical
// filesHash output across SDKs.
func getAllFilesInPath(src, contextDir string, ignorePatterns []string, includeDirs bool) ([]string, error) {
	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	fsys := os.DirFS(absCtx)
	matches, err := doublestar.Glob(fsys, filepath.ToSlash(src))
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}
	for _, m := range matches {
		if isIgnored(m, ignorePatterns) {
			continue
		}
		abs := filepath.Join(absCtx, m)
		info, err := os.Lstat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if includeDirs {
				out[abs] = struct{}{}
			}
			sub, err := doublestar.Glob(fsys, filepath.ToSlash(m)+"/**/*")
			if err != nil {
				return nil, err
			}
			for _, s := range sub {
				if isIgnored(s, ignorePatterns) {
					continue
				}
				out[filepath.Join(absCtx, s)] = struct{}{}
			}
		} else {
			out[abs] = struct{}{}
		}
	}

	result := make([]string, 0, len(out))
	for p := range out {
		result = append(result, p)
	}
	sort.Strings(result)
	return result, nil
}

// isIgnored reports whether `rel` matches any ignore pattern.
func isIgnored(rel string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := doublestar.Match(p, rel); err == nil && ok {
			return true
		}
	}
	return false
}
