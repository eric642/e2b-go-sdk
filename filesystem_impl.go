package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
)

// FsOptions holds per-call tweaks common across filesystem operations.
type FsOptions struct {
	User             string
	RequestTimeoutMs int
	// Depth limits how many directory levels Filesystem.List descends. 0 means
	// the default of 1 (immediate children only). Ignored by other operations.
	Depth int
}

// Stat fetches metadata for path.
func (f *Filesystem) Stat(ctx context.Context, path string, opts FsOptions) (*EntryInfo, error) {
	resp, err := f.sbx.envd.Filesystem.Stat(ctx, connect.NewRequest(&fspb.StatRequest{Path: path}))
	if err != nil {
		return nil, mapConnectErr(err)
	}
	return entryInfoFromPB(resp.Msg.GetEntry()), nil
}

// Exists reports whether path exists.
func (f *Filesystem) Exists(ctx context.Context, path string, opts FsOptions) (bool, error) {
	_, err := f.Stat(ctx, path, opts)
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*FileNotFoundError); ok {
		return false, nil
	}
	return false, err
}

// IsDir returns true when path is a directory.
func (f *Filesystem) IsDir(ctx context.Context, path string, opts FsOptions) (bool, error) {
	info, err := f.Stat(ctx, path, opts)
	if err != nil {
		return false, err
	}
	return info.Type == EntryTypeDirectory, nil
}

// List returns the entries of a directory. opts.Depth controls how many
// directory levels to descend; 0 means the default (1, immediate children).
// A negative depth is rejected, matching the reference SDKs' depth>=1 rule.
func (f *Filesystem) List(ctx context.Context, path string, opts FsOptions) ([]EntryInfo, error) {
	depth := opts.Depth
	if depth == 0 {
		depth = 1
	} else if depth < 1 {
		return nil, &InvalidArgumentError{Message: "depth should be at least 1"}
	}
	resp, err := f.sbx.envd.Filesystem.ListDir(ctx, connect.NewRequest(&fspb.ListDirRequest{Path: path, Depth: uint32(depth)}))
	if err != nil {
		return nil, mapConnectErr(err)
	}
	entries := resp.Msg.GetEntries()
	out := make([]EntryInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, *entryInfoFromPB(e))
	}
	return out, nil
}

// MakeDir creates a directory at path (including parents).
func (f *Filesystem) MakeDir(ctx context.Context, path string, opts FsOptions) error {
	_, err := f.sbx.envd.Filesystem.MakeDir(ctx, connect.NewRequest(&fspb.MakeDirRequest{Path: path}))
	return mapConnectErr(err)
}

// Remove deletes a file or directory.
func (f *Filesystem) Remove(ctx context.Context, path string, opts FsOptions) error {
	_, err := f.sbx.envd.Filesystem.Remove(ctx, connect.NewRequest(&fspb.RemoveRequest{Path: path}))
	return mapConnectErr(err)
}

// Move renames or moves a path.
func (f *Filesystem) Move(ctx context.Context, from, to string, opts FsOptions) error {
	_, err := f.sbx.envd.Filesystem.Move(ctx, connect.NewRequest(&fspb.MoveRequest{Source: from, Destination: to}))
	return mapConnectErr(err)
}

// Read returns the entire file content as []byte.
func (f *Filesystem) Read(ctx context.Context, path string, opts FsOptions) ([]byte, error) {
	rc, err := f.ReadStream(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ReadStream returns a streaming reader for path.
func (f *Filesystem) ReadStream(ctx context.Context, path string, opts FsOptions) (io.ReadCloser, error) {
	u, err := f.sbx.buildFileURL(path, SignatureRead, SignatureOptions{User: opts.User}, false)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, newSandboxError("build file request", err)
	}
	if f.sbx.EnvdAccessToken != "" {
		req.Header.Set("X-Access-Token", f.sbx.EnvdAccessToken)
	}
	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, mapEnvdFileErr(resp, path)
	}
	return resp.Body, nil
}

// Write creates or overwrites a file with the contents of r. Returns the
// final EntryInfo of the written file.
//
// The upload encoding is gated on the envd version: envd >= 0.5.7 accepts a
// raw application/octet-stream body, while older envd requires
// multipart/form-data. See uploadOne.
func (f *Filesystem) Write(ctx context.Context, path string, r io.Reader, opts FsOptions) (*WriteInfo, error) {
	return f.uploadOne(ctx, path, r, opts)
}

// uploadOne POSTs a single file to envd's /files endpoint, choosing the body
// encoding based on the envd version (see envdOctetStreamUpload). It is shared
// by Write and WriteFiles.
func (f *Filesystem) uploadOne(ctx context.Context, path string, r io.Reader, opts FsOptions) (*WriteInfo, error) {
	u, err := f.sbx.buildFileURL(path, SignatureWrite, SignatureOptions{User: opts.User}, true)
	if err != nil {
		return nil, err
	}

	body := r
	contentType := "application/octet-stream"
	if compareEnvdVersions(f.sbx.envdVersionForGating(), envdOctetStreamUpload) < 0 {
		// Pre-0.5.7 envd expects multipart/form-data with the file in a "file"
		// field whose filename carries the destination path. Stream the body
		// through an io.Pipe so memory stays bounded and a cancelled context is
		// observed promptly (the transport closes the reader, which surfaces as
		// a write error to the goroutine).
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		contentType = mw.FormDataContentType()
		go func() {
			part, err := mw.CreateFormFile("file", path)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, r); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			// mw.Close writes the trailing boundary; CloseWithError(nil) closes
			// the pipe cleanly so the reader sees io.EOF.
			_ = pw.CloseWithError(mw.Close())
		}()
		body = pr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, newSandboxError("build file request", err)
	}
	if f.sbx.EnvdAccessToken != "" {
		req.Header.Set("X-Access-Token", f.sbx.EnvdAccessToken)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, mapEnvdFileErr(resp, path)
	}
	// envd returns the written entry/entries as a JSON array (UploadSuccess).
	// Populate Name/Type from it so callers see real metadata; fall back to the
	// requested path if the body is empty or unparseable.
	info := &WriteInfo{Path: path}
	var entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err == nil && len(entries) > 0 {
		e := entries[0]
		if e.Path != "" {
			info.Path = e.Path
		}
		info.Name = e.Name
		if e.Type == "directory" {
			info.Type = EntryTypeDirectory
		} else {
			info.Type = EntryTypeFile
		}
	}
	return info, nil
}

// WriteString is a convenience helper for small text payloads.
func (f *Filesystem) WriteString(ctx context.Context, path, data string, opts FsOptions) (*WriteInfo, error) {
	return f.Write(ctx, path, bytes.NewReader([]byte(data)), opts)
}

// WriteFiles writes multiple files in one call, creating parent directories
// and overwriting existing files as needed. Each entry is uploaded with the
// envd-version-appropriate encoding (see Write). It returns the WriteInfo for
// every entry in order; on the first failure it returns the results gathered so
// far and the error.
func (f *Filesystem) WriteFiles(ctx context.Context, entries []WriteEntry, opts FsOptions) ([]WriteInfo, error) {
	out := make([]WriteInfo, 0, len(entries))
	for _, e := range entries {
		info, err := f.uploadOne(ctx, e.Path, bytes.NewReader(e.Data), opts)
		if err != nil {
			return out, err
		}
		out = append(out, *info)
	}
	return out, nil
}

// Watch starts watching a directory for filesystem events. The returned
// handle fans events onto a channel; Stop() cancels the stream.
//
// Recursive watching requires envd >= 0.1.4; requesting it on an older build
// returns an error up front instead of failing opaquely at the RPC layer.
func (f *Filesystem) Watch(ctx context.Context, path string, recursive bool) (*WatchHandle, error) {
	if recursive && compareEnvdVersions(f.sbx.envdVersionForGating(), envdRecursiveWatch) < 0 {
		return nil, &TemplateError{Message: fmt.Sprintf(
			"recursive directory watching requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdRecursiveWatch, f.sbx.EnvdVersion)}
	}
	ctx, cancel := context.WithCancel(ctx)
	stream, err := f.sbx.envd.Filesystem.WatchDir(ctx, connect.NewRequest(&fspb.WatchDirRequest{Path: path, Recursive: recursive}))
	if err != nil {
		cancel()
		return nil, mapConnectErr(err)
	}
	h := &WatchHandle{
		events: make(chan FilesystemEvent, 32),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go h.consume(stream)
	return h, nil
}

func entryInfoFromPB(e *fspb.EntryInfo) *EntryInfo {
	if e == nil {
		return &EntryInfo{}
	}
	var mt = e.GetModifiedTime().AsTime()
	info := &EntryInfo{
		Name:         e.GetName(),
		Path:         e.GetPath(),
		Size:         e.GetSize(),
		Mode:         e.GetMode(),
		Permissions:  e.GetPermissions(),
		Owner:        e.GetOwner(),
		Group:        e.GetGroup(),
		ModifiedTime: mt,
	}
	switch e.GetType() {
	case fspb.FileType_FILE_TYPE_FILE:
		info.Type = EntryTypeFile
	case fspb.FileType_FILE_TYPE_DIRECTORY:
		info.Type = EntryTypeDirectory
	}
	if e.SymlinkTarget != nil {
		info.SymlinkTarget = *e.SymlinkTarget
	}
	return info
}
