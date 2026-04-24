package template

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

// tarFileStream pipes a gzip-compressed tar of the file set matched by src
// (relative to contextDir) through an io.Pipe. The caller PUTs the reader
// directly to an HTTP endpoint. The returned error channel is closed once
// the producer goroutine finishes — the caller should receive from it to
// collect any producer error.
func tarFileStream(src, contextDir string, ignorePatterns []string, resolveSymlinks bool) (io.ReadCloser, <-chan error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- writeTar(pw, src, contextDir, ignorePatterns, resolveSymlinks)
		close(errc)
	}()
	return pr, errc
}

func writeTar(pw *io.PipeWriter, src, contextDir string, ignorePatterns []string, resolveSymlinks bool) error {
	gz := gzip.NewWriter(pw)
	tw := tar.NewWriter(gz)

	finalize := func(err error) error {
		twErr := tw.Close()
		gzErr := gz.Close()
		pwErr := pw.CloseWithError(err)
		if err != nil {
			return err
		}
		for _, e := range []error{twErr, gzErr, pwErr} {
			if e != nil {
				return e
			}
		}
		return nil
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
