package template

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

func tarFileBytes(src, contextDir string, ignorePatterns []string, resolveSymlinks bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeTarToWriter(&buf, src, contextDir, ignorePatterns, resolveSymlinks); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func tarFileStream(src, contextDir string, ignorePatterns []string, resolveSymlinks bool) (io.ReadCloser, <-chan error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := writeTarToWriter(pw, src, contextDir, ignorePatterns, resolveSymlinks)
		_ = pw.CloseWithError(err)
		errc <- err
		close(errc)
	}()
	return pr, errc
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
		if !resolveSymlinks {
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
		lst, err = os.Stat(abs)
		if err != nil {
			return err
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
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
