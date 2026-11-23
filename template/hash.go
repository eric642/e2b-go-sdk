package template

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	e2b "github.com/eric642/e2b-go-sdk"
)

const hashChunkSize = 256 << 10

// calculateFilesHash produces a SHA-256 hex digest over the file set referenced
// by src (a glob relative to contextDir) using the same algorithm as the
// Python SDK's calculate_files_hash. Keeping the byte stream identical is
// required for cross-SDK cache hits.
func calculateFilesHash(src, dest, contextDir string, ignorePatterns []string, resolveSymlinks bool) (string, error) {
	h := sha256.New()
	if _, err := io.WriteString(h, fmt.Sprintf("COPY %s %s", src, dest)); err != nil {
		return "", &BuildError{Op: "hash", Err: err}
	}

	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return "", &BuildError{Op: "hash", Err: err}
	}

	files, err := getAllFilesInPath(src, contextDir, ignorePatterns, true)
	if err != nil {
		if _, ok := errors.AsType[e2b.Error](err); ok {
			return "", err
		}
		return "", &BuildError{Op: "hash", Err: err}
	}
	if len(files) == 0 {
		return "", &BuildError{
			Op:  "hash",
			Err: fmt.Errorf("no files found in %s", filepath.Join(contextDir, src)),
		}
	}

	buf := make([]byte, hashChunkSize)
	for _, f := range files {
		rel, err := filepath.Rel(absCtx, f)
		if err != nil {
			return "", &BuildError{Op: "hash", Err: err}
		}
		if _, err := io.WriteString(h, filepath.ToSlash(rel)); err != nil {
			return "", &BuildError{Op: "hash", Err: err}
		}

		lst, err := os.Lstat(f)
		if err != nil {
			return "", &BuildError{Op: "hash", Err: err}
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
					return "", &BuildError{Op: "hash", Err: err}
				}
				target, err := os.Readlink(f)
				if err != nil {
					return "", &BuildError{Op: "hash", Err: err}
				}
				if _, err := io.WriteString(h, target); err != nil {
					return "", &BuildError{Op: "hash", Err: err}
				}
				continue
			}
		}
		st, err := os.Stat(f)
		if err != nil {
			return "", &BuildError{Op: "hash", Err: err}
		}
		if err := writeStats(h, st); err != nil {
			return "", &BuildError{Op: "hash", Err: err}
		}
		if st.Mode().IsRegular() {
			if err := hashFileContent(h, f, buf); err != nil {
				return "", &BuildError{Op: "hash", Err: err}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFileContent(w io.Writer, path string, buf []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.CopyBuffer(w, f, buf)
	return err
}

func writeStats(w io.Writer, info os.FileInfo) error {
	mode := goModeToPosix(info.Mode())
	if _, err := io.WriteString(w, strconv.FormatUint(uint64(mode), 10)); err != nil {
		return err
	}
	// A directory's size depends on the filesystem (e.g. APFS vs ext4), not on
	// the copied files; upstream Python/JS SDKs omit it from the hash stream.
	if !info.IsDir() {
		if _, err := io.WriteString(w, strconv.FormatInt(info.Size(), 10)); err != nil {
			return err
		}
	}
	return nil
}
