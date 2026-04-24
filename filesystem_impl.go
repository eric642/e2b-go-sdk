package e2b

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
)

// FsOptions holds per-call tweaks common across filesystem operations.
type FsOptions struct {
	User              string
	RequestTimeoutMs  int
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

// List returns the entries of a directory.
func (f *Filesystem) List(ctx context.Context, path string, opts FsOptions) ([]EntryInfo, error) {
	resp, err := f.sbx.envd.Filesystem.ListDir(ctx, connect.NewRequest(&fspb.ListDirRequest{Path: path, Depth: 1}))
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
		return nil, mapHTTPErr(resp, "")
	}
	return resp.Body, nil
}

// Write creates or overwrites a file with the contents of r. Returns the
// final EntryInfo of the written file.
func (f *Filesystem) Write(ctx context.Context, path string, r io.Reader, opts FsOptions) (*WriteInfo, error) {
	u, err := f.sbx.buildFileURL(path, SignatureWrite, SignatureOptions{User: opts.User}, true)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, r)
	if err != nil {
		return nil, newSandboxError("build file request", err)
	}
	if f.sbx.EnvdAccessToken != "" {
		req.Header.Set("X-Access-Token", f.sbx.EnvdAccessToken)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, mapHTTPErr(resp, "")
	}
	return &WriteInfo{Path: path}, nil
}

// WriteString is a convenience helper for small text payloads.
func (f *Filesystem) WriteString(ctx context.Context, path, data string, opts FsOptions) (*WriteInfo, error) {
	return f.Write(ctx, path, bytes.NewReader([]byte(data)), opts)
}

// Watch starts watching a directory for filesystem events. The returned
// handle fans events onto a channel; Stop() cancels the stream.
func (f *Filesystem) Watch(ctx context.Context, path string, recursive bool) (*WatchHandle, error) {
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
		Name:        e.GetName(),
		Path:        e.GetPath(),
		Size:        e.GetSize(),
		Mode:        e.GetMode(),
		Permissions: e.GetPermissions(),
		Owner:       e.GetOwner(),
		Group:       e.GetGroup(),
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
