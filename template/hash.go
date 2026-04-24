package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// calculateFilesHash produces a SHA-256 hex digest over the file set referenced
// by src (a glob relative to contextDir) using the same algorithm as the
// Python SDK's calculate_files_hash. Keeping the byte stream identical is
// required for cross-SDK cache hits.
func calculateFilesHash(src, dest, contextDir string, ignorePatterns []string, resolveSymlinks bool) (string, error) {
	h := sha256.New()
	if _, err := io.WriteString(h, fmt.Sprintf("COPY %s %s", src, dest)); err != nil {
		return "", err
	}

	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return "", err
	}

	files, err := getAllFilesInPath(src, contextDir, ignorePatterns, true)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no files found in %s", filepath.Join(contextDir, src))
	}

	for _, f := range files {
		rel, err := filepath.Rel(absCtx, f)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(h, filepath.ToSlash(rel)); err != nil {
			return "", err
		}

		lst, err := os.Lstat(f)
		if err != nil {
			return "", err
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			shouldFollow := resolveSymlinks
			if shouldFollow {
				if _, err := os.Stat(f); err != nil {
					shouldFollow = false
				}
			}
			if !shouldFollow {
				if err := writeStats(h, lst); err != nil {
					return "", err
				}
				target, err := os.Readlink(f)
				if err != nil {
					return "", err
				}
				if _, err := io.WriteString(h, target); err != nil {
					return "", err
				}
				continue
			}
		}
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		if err := writeStats(h, st); err != nil {
			return "", err
		}
		if st.Mode().IsRegular() {
			data, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			if _, err := h.Write(data); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeStats(w io.Writer, info os.FileInfo) error {
	mode := goModeToPosix(info.Mode())
	if _, err := io.WriteString(w, strconv.FormatUint(uint64(mode), 10)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, strconv.FormatInt(info.Size(), 10)); err != nil {
		return err
	}
	return nil
}
