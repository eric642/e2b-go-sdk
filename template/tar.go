package template

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

// tarFileToTemp writes a gzipped tar archive of files matching src to a
// temporary file on disk, returning the open file positioned at offset 0, its
// exact byte size, and a cleanup callback that closes and deletes the temp
// file. Spooling to disk keeps memory bounded for large COPY contexts while
// giving presigned uploads an exact Content-Length and retry-safe GetBody.
func tarFileToTemp(src, contextDir string, ignorePatterns []string, resolveSymlinks bool) (*os.File, int64, func(), error) {
	tmp, err := os.CreateTemp("", "e2b-template-*.tar.gz")
	if err != nil {
		return nil, 0, nil, err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if err := writeTarToWriter(tmp, src, contextDir, ignorePatterns, resolveSymlinks); err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	info, err := tmp.Stat()
	if err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	return tmp, info.Size(), cleanup, nil
}

func writeTarToWriter(w io.Writer, src, contextDir string, ignorePatterns []string, resolveSymlinks bool) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	finalize := func(err error) error {
		twErr := tw.Close()
		gzErr := gz.Close()
		if err != nil {
			return err
		}
		if twErr != nil {
			return twErr
		}
		return gzErr
	}

	files, err := getAllFilesInPath(src, contextDir, ignorePatterns, true)
	if err != nil {
		return finalize(err)
	}
	absCtx, err := filepath.Abs(contextDir)
	if err != nil {
		return finalize(err)
	}
	for _, f := range files {
		if err := addTarEntry(tw, f, absCtx, resolveSymlinks); err != nil {
			return finalize(err)
		}
	}
	return finalize(nil)
}

func addTarEntry(tw *tar.Writer, abs, contextDir string, resolveSymlinks bool) error {
	rel, err := filepath.Rel(contextDir, abs)
	if err != nil {
		return err
	}
	lst, err := os.Lstat(abs)
	if err != nil {
		return err
	}

	if lst.Mode()&os.ModeSymlink != 0 {
		shouldFollow := resolveSymlinks
		if shouldFollow {
			if targetInfo, err := os.Stat(abs); err == nil {
				lst = targetInfo
			} else {
				// Broken symlink: fall back to preserving the symlink header
				// so tar packaging matches calculateFilesHash.
				shouldFollow = false
			}
		}
		if !shouldFollow {
			target, err := os.Readlink(abs)
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(lst, target)
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			return tw.WriteHeader(hdr)
		}
	}

	hdr, err := tar.FileInfoHeader(lst, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !lst.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(tw, f)
	return err
}
