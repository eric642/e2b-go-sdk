package template

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	e2b "github.com/eric642/e2b-go-sdk"
)

// readDockerignore returns the non-comment, non-empty lines of
// <contextDir>/.dockerignore with any leading UTF-8 BOM stripped. A missing
// file yields an empty slice.
func readDockerignore(contextDir string) []string {
	data, err := os.ReadFile(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		return nil
	}
	content := strings.TrimPrefix(string(data), "\uFEFF")
	var out []string
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

type ignoreRule struct {
	exclusion bool
	dirs      []string
	match     func(string) bool
}

// ignoreMatcher evaluates .dockerignore rules against slash-separated paths
// relative to the context root, matching upstream PatternMatcher / BuildKit
// semantics: leading and trailing slashes are normalized, a rule matching a
// directory excludes all descendants, and '!' rules re-include paths (last
// matching rule wins).
type ignoreMatcher struct {
	rules        []ignoreRule
	hasExclusion bool
}

func newIgnoreMatcher(patterns []string, absContextDir string) (*ignoreMatcher, error) {
	backslashIsSeparator := os.PathSeparator == '\\'
	m := &ignoreMatcher{
		rules: make([]ignoreRule, 0, len(patterns)),
	}
	for _, original := range patterns {
		if strings.HasPrefix(original, "#") {
			continue
		}
		p := strings.TrimSpace(original)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		exclusion := strings.HasPrefix(p, "!")
		if exclusion {
			p = strings.TrimSpace(p[1:])
			if p == "" {
				return nil, &e2b.InvalidArgumentError{
					Message: fmt.Sprintf("invalid ignore pattern %q: illegal exclusion pattern", original),
				}
			}
		}
		// Absolute patterns pointing inside the context directory are made
		// relative to it; other patterns are anchored at the context root.
		if absContextDir != "" && filepath.IsAbs(p) {
			if rel, err := filepath.Rel(absContextDir, p); err == nil &&
				rel != "." && rel != ".." &&
				!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
				!filepath.IsAbs(rel) {
				p = filepath.ToSlash(rel)
			}
		}
		if backslashIsSeparator {
			p = strings.ReplaceAll(p, "\\", "/")
		}
		p = path.Clean(p)
		if len(p) > 1 && strings.HasPrefix(p, "/") {
			p = p[1:]
		}
		fn, err := compileIgnorePattern(p, !backslashIsSeparator)
		if err != nil {
			return nil, &e2b.InvalidArgumentError{
				Message: fmt.Sprintf("invalid ignore pattern %q: %v", original, err),
			}
		}
		if exclusion {
			m.hasExclusion = true
		}
		m.rules = append(m.rules, ignoreRule{
			exclusion: exclusion,
			dirs:      strings.Split(p, "/"),
			match:     fn,
		})
	}
	return m, nil
}

// matches reports whether slash-separated path rel (relative to context root)
// is excluded. Patterns are evaluated on each parent directory prefix first so
// a rule matching a directory also matches descendants unless overridden by a
// later '!' rule.
func (m *ignoreMatcher) matches(rel string) bool {
	if len(m.rules) == 0 || rel == "" || rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	parentMatched := make([]bool, len(m.rules))
	matched := false
	for depth := 1; depth <= len(segments); depth++ {
		current := strings.Join(segments[:depth], "/")
		matched = false
		for i, r := range m.rules {
			match := parentMatched[i]
			if !match {
				if r.exclusion != matched {
					continue
				}
				match = r.match(current)
				parentMatched[i] = match
			}
			if match {
				matched = !r.exclusion
			}
		}
	}
	return matched
}

// mayMatchUnder reports whether any '!' exclusion rule could re-include a path
// under dirRel. When false for an ignored directory, WalkDir can return
// fs.SkipDir immediately.
func (m *ignoreMatcher) mayMatchUnder(dirRel string) bool {
	if !m.hasExclusion {
		return false
	}
	dirSegments := strings.Split(dirRel, "/")
	for _, r := range m.rules {
		if r.exclusion && patternMayMatchUnder(r.dirs, dirSegments) {
			return true
		}
	}
	return false
}

func patternMayMatchUnder(patternDirs, dirSegments []string) bool {
	for i, ds := range dirSegments {
		if i >= len(patternDirs) {
			return false
		}
		seg := patternDirs[i]
		if strings.Contains(seg, "**") {
			return true
		}
		if !strings.ContainsAny(seg, `*?[\`) && seg != ds {
			return false
		}
	}
	return len(patternDirs) > len(dirSegments)
}

type ignoreMatchKind uint8

const (
	matchExact ignoreMatchKind = iota
	matchPrefix
	matchSuffix
	matchRegex
)

func validateIgnorePattern(pattern string, backslashIsEscape bool) error {
	n := len(pattern)
	i := 0
	for i < n {
		ch := pattern[i]
		i++
		if ch == '\\' && backslashIsEscape {
			if i >= n {
				return fmt.Errorf("syntax error in pattern")
			}
			_, sz := utf8.DecodeRuneInString(pattern[i:])
			i += sz
		} else if ch == '[' {
			if i < n && pattern[i] == '^' {
				i++
			}
			ranges := 0
			for ranges == 0 || i >= n || pattern[i] != ']' {
				var err error
				i, err = scanClassChar(pattern, i, backslashIsEscape)
				if err != nil {
					return err
				}
				if i < n && pattern[i] == '-' {
					i, err = scanClassChar(pattern, i+1, backslashIsEscape)
					if err != nil {
						return err
					}
				}
				ranges++
			}
			i++
		}
	}
	return nil
}

func scanClassChar(pattern string, i int, backslashIsEscape bool) (int, error) {
	n := len(pattern)
	if i >= n || pattern[i] == '-' || pattern[i] == ']' {
		return 0, fmt.Errorf("syntax error in pattern")
	}
	if pattern[i] == '\\' && backslashIsEscape {
		i++
		if i >= n {
			return 0, fmt.Errorf("syntax error in pattern")
		}
	}
	_, sz := utf8.DecodeRuneInString(pattern[i:])
	i += sz
	if i >= n {
		return 0, fmt.Errorf("syntax error in pattern")
	}
	return i, nil
}

func compileIgnorePattern(pattern string, backslashIsEscape bool) (func(string) bool, error) {
	if err := validateIgnorePattern(pattern, backslashIsEscape); err != nil {
		return nil, err
	}
	var reBuf strings.Builder
	reBuf.WriteByte('^')
	kind := matchExact
	n := len(pattern)
	i := 0
	for i < n {
		first := i == 0
		ch := pattern[i]
		i++
		switch {
		case ch == '*':
			if i < n && pattern[i] == '*' {
				i++
				if i < n && pattern[i] == '/' {
					i++
				}
				if i >= n {
					reBuf.WriteString(".*")
					if kind == matchExact {
						kind = matchPrefix
					} else {
						kind = matchRegex
					}
				} else {
					reBuf.WriteString("(.*/)?")
					kind = matchRegex
				}
				if first {
					kind = matchSuffix
				}
			} else {
				reBuf.WriteString("[^/]*")
				kind = matchRegex
			}
		case ch == '?':
			reBuf.WriteString("[^/]")
			kind = matchRegex
		case ch == '[':
			j := i
			if j < n && pattern[j] == '^' {
				j++
			}
			for j < n && pattern[j] != ']' {
				if pattern[j] == '\\' && backslashIsEscape && j+1 < n {
					j++
				}
				j++
			}
			reBuf.WriteByte('[')
			reBuf.WriteString(pattern[i:j])
			if j < n {
				reBuf.WriteByte(']')
			}
			i = j + 1
			kind = matchRegex
		case ch == ']':
			reBuf.WriteString(`\]`)
			kind = matchRegex
		case strings.ContainsRune(".+()|{}$^", rune(ch)):
			reBuf.WriteByte('\\')
			reBuf.WriteByte(ch)
		case ch == '\\' && backslashIsEscape:
			if i < n {
				r, sz := utf8.DecodeRuneInString(pattern[i:])
				reBuf.WriteString(regexp.QuoteMeta(string(r)))
				i += sz
				kind = matchRegex
			} else {
				reBuf.WriteString(`\\`)
			}
		default:
			reBuf.WriteByte(ch)
		}
	}

	switch kind {
	case matchExact:
		return func(p string) bool { return p == pattern }, nil
	case matchPrefix:
		prefix := pattern[:len(pattern)-2]
		return func(p string) bool { return strings.HasPrefix(p, prefix) }, nil
	case matchSuffix:
		suffix := pattern[2:]
		return func(p string) bool {
			return strings.HasSuffix(p, suffix) || (strings.HasPrefix(suffix, "/") && p == suffix[1:])
		}, nil
	default:
		reBuf.WriteByte('$')
		re, err := regexp.Compile(reBuf.String())
		if err != nil {
			return nil, err
		}
		return re.MatchString, nil
	}
}
