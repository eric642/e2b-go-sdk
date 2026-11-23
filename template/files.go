package template

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	e2b "github.com/eric642/e2b-go-sdk"
)

// getAllFilesInPath returns absolute paths of entries matching `src` (a glob
// relative to contextDir), applying .dockerignore rules in ignorePatterns and
// recursing into matched directories via filepath.WalkDir. Results are sorted
// ascending by absolute path — this matches Python's sorted(files) behavior
// and is required for byte-identical filesHash output across SDKs.
func getAllFilesInPath(src, contextDir string, ignorePatterns []string, includeDirs bool) ([]string, error) {
	if err := validateRelativePath(src); err != nil {
		return nil, err
	}
	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, &BuildError{Op: "files", Err: err}
	}
	matcher, err := newIgnoreMatcher(ignorePatterns, absCtx)
	if err != nil {
		return nil, err
	}

	cleanSrc := path.Clean(filepath.ToSlash(src))
	fsys := os.DirFS(absCtx)
	rawMatches, err := doublestar.Glob(fsys, cleanSrc)
	if err != nil {
		if errors.Is(err, doublestar.ErrBadPattern) {
			return nil, &e2b.InvalidArgumentError{
				Message: fmt.Sprintf("invalid copy glob pattern %q: %v", src, err),
			}
		}
		return nil, &BuildError{Op: "files", Err: err}
	}

	type matchItem struct {
		abs string
		rel string
	}
	matches := make([]matchItem, 0, len(rawMatches))
	for _, m := range rawMatches {
		rel := path.Clean(filepath.ToSlash(m))
		abs := absCtx
		if rel != "." {
			abs = filepath.Join(absCtx, filepath.FromSlash(rel))
		}
		matches = append(matches, matchItem{abs: abs, rel: rel})
	}

	// Visit parents before children so paths under an already walked directory
	// are not walked a second time.
	slices.SortFunc(matches, func(a, b matchItem) int {
		da := relDepth(a.rel)
		db := relDepth(b.rel)
		if da != db {
			return da - db
		}
		return strings.Compare(a.rel, b.rel)
	})

	walkedDirs := map[string]struct{}{}
	isUnderWalked := func(rel string) bool {
		if _, ok := walkedDirs["."]; ok {
			return true
		}
		for parent := path.Dir(rel); parent != "." && parent != "/"; parent = path.Dir(parent) {
			if _, ok := walkedDirs[parent]; ok {
				return true
			}
		}
		return false
	}

	out := map[string]struct{}{}
	for _, m := range matches {
		if isUnderWalked(m.rel) {
			continue
		}
		info, err := os.Lstat(m.abs)
		if err != nil {
			continue
		}
		ignored := m.rel != "." && matcher.matches(m.rel)
		if info.IsDir() {
			if ignored && !matcher.mayMatchUnder(m.rel) {
				continue
			}
			walkErr := filepath.WalkDir(m.abs, func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if p == m.abs {
					if !ignored && includeDirs {
						out[p] = struct{}{}
					}
					return nil
				}
				relFS, err := filepath.Rel(absCtx, p)
				if err != nil {
					return err
				}
				relSlash := filepath.ToSlash(relFS)
				childIgnored := matcher.matches(relSlash)
				if d.IsDir() {
					if childIgnored {
						if !matcher.mayMatchUnder(relSlash) {
							return fs.SkipDir
						}
						return nil
					}
					if includeDirs {
						out[p] = struct{}{}
					}
					return nil
				}
				if !childIgnored {
					out[p] = struct{}{}
				}
				return nil
			})
			if walkErr != nil {
				return nil, &BuildError{Op: "files", Err: walkErr}
			}
			walkedDirs[m.rel] = struct{}{}
		} else if !ignored {
			out[m.abs] = struct{}{}
		}
	}

	result := make([]string, 0, len(out))
	for p := range out {
		result = append(result, p)
	}
	slices.Sort(result)
	return result, nil
}

func relDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}
