package e2b

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

const metadataHeaderPrefix = "X-Metadata-"

// FsOptions holds per-call options across filesystem operations.
type FsOptions struct {
	User           string
	RequestTimeout time.Duration
	// RequestTimeoutMs is preserved for backwards compatibility when
	// RequestTimeout is unset (0).
	RequestTimeoutMs int
	// Depth limits how many directory levels Filesystem.List descends. 0 means
	// the default of 1 (immediate children only). Ignored by other operations.
	Depth int
	// Gzip sends Accept-Encoding: gzip on GET /files and transparently
	// decompresses responses carrying Content-Encoding: gzip.
	Gzip bool
	// Metadata attaches user-defined extended attributes via X-Metadata-<key>
	// headers on file uploads. Requires envd >= v0.6.2; returns *TemplateError
	// on older envd builds.
	Metadata map[string]string
	// ForceMultipart forces multipart/form-data upload encoding even when
	// envd >= v0.5.7 supports application/octet-stream.
	ForceMultipart bool
}

func (o FsOptions) effectiveTimeout() time.Duration {
	if o.RequestTimeout > 0 {
		return o.RequestTimeout
	}
	if o.RequestTimeoutMs > 0 {
		return time.Duration(o.RequestTimeoutMs) * time.Millisecond
	}
	return 0
}

// WatchOptions configures Filesystem.WatchWithOptions.
type WatchOptions struct {
	Recursive          bool
	IncludeEntry       bool
	AllowNetworkMounts bool
	User               string
	TimeoutMs          int
	RequestTimeout     time.Duration
}

func (f *Filesystem) applyHeaders(req *http.Request, resolvedUser string) {
	_, accessToken, _, envdCli := f.sbx.envdSnapshot()
	var auth transport.EnvdAuth
	if envdCli != nil {
		auth = envdCli.Auth
	} else {
		auth = transport.EnvdAuth{
			Token:     accessToken,
			SandboxID: f.sbx.ID,
			Headers:   f.sbx.cfg.envdHeaders(),
		}
	}
	if accessToken != "" {
		auth.Token = accessToken
	}
	if auth.SandboxID == "" && f.sbx.ID != "" {
		auth.SandboxID = f.sbx.ID
	}
	transport.ApplyEnvdHeaders(req, auth, resolvedUser)
}

// Stat fetches metadata for filePath.
//
// Endpoint: filesystem.Filesystem/Stat (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) Stat(ctx context.Context, filePath string, opts FsOptions) (*EntryInfo, error) {
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	resp, err := f.sbx.envdClients().Filesystem.Stat(callCtx, connect.NewRequest(&fspb.StatRequest{Path: filePath}))
	if err != nil {
		return nil, mapConnectErr(err)
	}
	return entryInfoFromPB(resp.Msg.GetEntry()), nil
}

// GetInfo is an alias for Stat, matching the getInfo / get_info naming in the
// JS and Python SDKs.
//
// Endpoint: filesystem.Filesystem/Stat (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) GetInfo(ctx context.Context, filePath string, opts FsOptions) (*EntryInfo, error) {
	return f.Stat(ctx, filePath, opts)
}

// Exists reports whether filePath exists inside the sandbox.
//
// Endpoint: filesystem.Filesystem/Stat (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) Exists(ctx context.Context, filePath string, opts FsOptions) (bool, error) {
	_, err := f.Stat(ctx, filePath, opts)
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*FileNotFoundError); ok {
		return false, nil
	}
	return false, err
}

// IsDir reports whether filePath exists and is a directory.
//
// Endpoint: filesystem.Filesystem/Stat (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) IsDir(ctx context.Context, filePath string, opts FsOptions) (bool, error) {
	info, err := f.Stat(ctx, filePath, opts)
	if err != nil {
		return false, err
	}
	return info.Type == EntryTypeDirectory, nil
}

// List returns the entries of a directory. opts.Depth controls how many
// directory levels to descend; 0 means the default (1, immediate children).
// A negative depth is rejected with *InvalidArgumentError, matching the
// reference SDKs' depth>=1 rule.
//
// Endpoint: filesystem.Filesystem/ListDir (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) List(ctx context.Context, dirPath string, opts FsOptions) ([]EntryInfo, error) {
	depth := opts.Depth
	if depth == 0 {
		depth = 1
	} else if depth < 1 {
		return nil, &InvalidArgumentError{Message: "depth should be at least 1"}
	}

	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	resp, err := f.sbx.envdClients().Filesystem.ListDir(callCtx, connect.NewRequest(&fspb.ListDirRequest{Path: dirPath, Depth: uint32(depth)}))
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

// MakeDir creates a directory at dirPath (including missing parent directories).
//
// Endpoint: filesystem.Filesystem/MakeDir (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) MakeDir(ctx context.Context, dirPath string, opts FsOptions) error {
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	_, err := f.sbx.envdClients().Filesystem.MakeDir(callCtx, connect.NewRequest(&fspb.MakeDirRequest{Path: dirPath}))
	return mapConnectErr(err)
}

// Remove deletes a file or directory recursively.
//
// Endpoint: filesystem.Filesystem/Remove (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) Remove(ctx context.Context, targetPath string, opts FsOptions) error {
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	_, err := f.sbx.envdClients().Filesystem.Remove(callCtx, connect.NewRequest(&fspb.RemoveRequest{Path: targetPath}))
	return mapConnectErr(err)
}

// Move renames or moves a file or directory from source to destination.
//
// Endpoint: filesystem.Filesystem/Move (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) Move(ctx context.Context, from, to string, opts FsOptions) error {
	_, err := f.Rename(ctx, from, to, opts)
	return err
}

// Rename moves or renames oldPath to newPath and returns the EntryInfo of the
// moved filesystem entry.
//
// Endpoint: filesystem.Filesystem/Move (envd Connect-RPC)
// Envd: >= v0.1.0
func (f *Filesystem) Rename(ctx context.Context, oldPath, newPath string, opts FsOptions) (*EntryInfo, error) {
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	resp, err := f.sbx.envdClients().Filesystem.Move(callCtx, connect.NewRequest(&fspb.MoveRequest{Source: oldPath, Destination: newPath}))
	if err != nil {
		return nil, mapConnectErr(err)
	}
	if entry := resp.Msg.GetEntry(); entry != nil {
		return entryInfoFromPB(entry), nil
	}
	return &EntryInfo{
		Path: newPath,
		Name: path.Base(newPath),
	}, nil
}

// Read downloads and returns the entire file content as []byte.
//
// Endpoint: GET /files (envd)
// Envd: >= v0.1.0
func (f *Filesystem) Read(ctx context.Context, filePath string, opts FsOptions) ([]byte, error) {
	readCtx := ctx
	if timeout := unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()); timeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	rc, err := f.ReadStream(readCtx, filePath, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	return data, nil
}

// ReadBytes is an explicit byte-slice alias for Read.
//
// Endpoint: GET /files (envd)
// Envd: >= v0.1.0
func (f *Filesystem) ReadBytes(ctx context.Context, filePath string, opts FsOptions) ([]byte, error) {
	return f.Read(ctx, filePath, opts)
}

// ReadString downloads and returns the entire file content as a string.
//
// Endpoint: GET /files (envd)
// Envd: >= v0.1.0
func (f *Filesystem) ReadString(ctx context.Context, filePath string, opts FsOptions) (string, error) {
	b, err := f.Read(ctx, filePath, opts)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadStream opens a streaming reader for filePath. The caller must Close the
// returned io.ReadCloser when done. When opts.Gzip is true, Accept-Encoding:
// gzip is sent and Content-Encoding: gzip responses are transparently gunzipped.
//
// Endpoint: GET /files (envd)
// Envd: >= v0.1.0
func (f *Filesystem) ReadStream(ctx context.Context, filePath string, opts FsOptions) (io.ReadCloser, error) {
	u, err := f.sbx.buildFileURL(filePath, SignatureRead, SignatureOptions{User: opts.User}, false)
	if err != nil {
		return nil, err
	}
	resolvedUser := resolveEnvdUser(f.sbx.envdVersionForGating(), opts.User)
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, opts.effectiveTimeout())

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, newSandboxError("build file request", err)
	}
	f.applyHeaders(req, resolvedUser)
	if opts.Gzip {
		req.Header.Set("Accept-Encoding", "gzip")
	}

	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		cancel()
		return nil, mapHTTPOrCtx(err)
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		defer cancel()
		return nil, mapEnvdFileErr(resp, filePath)
	}

	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, newSandboxError("decompress gzip file response", err)
		}
		return &gzipStreamReadCloser{gz: gz, body: resp.Body, cancel: cancel}, nil
	}
	return &streamReadCloser{ReadCloser: resp.Body, cancel: cancel}, nil
}

type streamReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (s *streamReadCloser) Close() error {
	err := s.ReadCloser.Close()
	if s.cancel != nil {
		s.cancel()
	}
	return err
}

type gzipStreamReadCloser struct {
	gz     *gzip.Reader
	body   io.ReadCloser
	cancel context.CancelFunc
}

func (g *gzipStreamReadCloser) Read(p []byte) (int, error) {
	return g.gz.Read(p)
}

func (g *gzipStreamReadCloser) Close() error {
	gzErr := g.gz.Close()
	bodyErr := g.body.Close()
	if g.cancel != nil {
		g.cancel()
	}
	if gzErr != nil {
		return gzErr
	}
	return bodyErr
}

// Write creates or overwrites a file with the contents of r and returns the
// resulting WriteInfo.
//
// The upload encoding is gated on the envd version unless opts.ForceMultipart
// is set: envd >= v0.5.7 accepts a raw application/octet-stream body, while
// older envd builds use streaming multipart/form-data.
//
// Endpoint: POST /files (envd)
// Envd: >= v0.1.0 (octet-stream >= v0.5.7; metadata >= v0.6.2)
func (f *Filesystem) Write(ctx context.Context, filePath string, r io.Reader, opts FsOptions) (*WriteInfo, error) {
	return f.uploadOne(ctx, filePath, r, opts)
}

// WriteBytes creates or overwrites a file with data.
//
// Endpoint: POST /files (envd)
// Envd: >= v0.1.0 (octet-stream >= v0.5.7; metadata >= v0.6.2)
func (f *Filesystem) WriteBytes(ctx context.Context, filePath string, data []byte, opts FsOptions) (*WriteInfo, error) {
	return f.Write(ctx, filePath, bytes.NewReader(data), opts)
}

// WriteString creates or overwrites a file with string data.
//
// Endpoint: POST /files (envd)
// Envd: >= v0.1.0 (octet-stream >= v0.5.7; metadata >= v0.6.2)
func (f *Filesystem) WriteString(ctx context.Context, filePath, data string, opts FsOptions) (*WriteInfo, error) {
	return f.Write(ctx, filePath, strings.NewReader(data), opts)
}

// WriteFiles writes multiple files sequentially, creating parent directories
// and overwriting existing files as needed. Each entry is uploaded with the
// envd-version-appropriate encoding (see Write). It returns the WriteInfo for
// every entry in order; on the first failure it returns the results gathered so
// far and the error.
//
// Endpoint: POST /files (envd)
// Envd: >= v0.1.0 (octet-stream >= v0.5.7; metadata >= v0.6.2)
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

// Compose concatenates multiple existing regular files inside the sandbox into
// destination using zero-copy kernel file assembly, deleting the source files
// once composition succeeds.
//
// Requires envd >= v0.5.5; returns *TemplateError on older envd builds.
//
// Endpoint: POST /files/compose (envd)
// Envd: >= v0.5.5
func (f *Filesystem) Compose(ctx context.Context, sources []string, destination string, opts FsOptions) (*EntryInfo, error) {
	domain, _, gatingVer, _ := f.sbx.envdSnapshot()
	if compareEnvdVersions(gatingVer, envdFileCompose) < 0 {
		return nil, &TemplateError{Message: fmt.Sprintf(
			"composing files requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdFileCompose, f.sbx.envdVersionRaw())}
	}
	if len(sources) == 0 {
		return nil, &InvalidArgumentError{Message: "sources must not be empty"}
	}
	if destination == "" {
		return nil, &InvalidArgumentError{Message: "destination is required"}
	}

	resolvedUser := resolveEnvdUser(gatingVer, opts.User)
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, unaryEnvdTimeout(f.sbx, opts.effectiveTimeout()))
	defer cancel()

	payload, err := json.Marshal(struct {
		SourcePaths []string `json:"source_paths"`
		Destination string   `json:"destination"`
		Username    string   `json:"username,omitempty"`
	}{
		SourcePaths: sources,
		Destination: destination,
		Username:    resolvedUser,
	})
	if err != nil {
		return nil, newSandboxError("marshal compose request", err)
	}

	base := strings.TrimRight(f.sbx.cfg.sandboxURL(f.sbx.ID, domain), "/")
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/files/compose", bytes.NewReader(payload))
	if err != nil {
		return nil, newSandboxError("build compose request", err)
	}
	f.applyHeaders(req, resolvedUser)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, mapEnvdFileErr(resp, destination)
	}

	var raw httpFileEntry
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, newSandboxError("decode compose response", err)
	}
	info := &EntryInfo{
		Path: destination,
		Name: path.Base(destination),
		Type: EntryTypeFile,
	}
	if raw.Path != "" {
		info.Path = raw.Path
	}
	if raw.Name != "" {
		info.Name = raw.Name
	}
	if t := entryTypeFromString(raw.Type); t != EntryTypeUnspecified {
		info.Type = t
	}
	if len(raw.Metadata) > 0 {
		info.Metadata = maps.Clone(raw.Metadata)
	}
	return info, nil
}

type httpFileEntry struct {
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata"`
}

func entryTypeFromString(s string) EntryType {
	switch strings.ToLower(s) {
	case "file":
		return EntryTypeFile
	case "directory", "dir":
		return EntryTypeDirectory
	case "symlink":
		return EntryTypeSymlink
	default:
		return EntryTypeUnspecified
	}
}

func (f *Filesystem) uploadOne(ctx context.Context, filePath string, r io.Reader, opts FsOptions) (*WriteInfo, error) {
	gatingVer := f.sbx.envdVersionForGating()
	if len(opts.Metadata) > 0 {
		if err := validateFileMetadata(opts.Metadata); err != nil {
			return nil, err
		}
		if compareEnvdVersions(gatingVer, envdFileMetadata) < 0 {
			return nil, &TemplateError{Message: fmt.Sprintf(
				"file metadata requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
				envdFileMetadata, f.sbx.envdVersionRaw())}
		}
	}

	u, err := f.sbx.buildFileURL(filePath, SignatureWrite, SignatureOptions{User: opts.User}, true)
	if err != nil {
		return nil, err
	}

	resolvedUser := resolveEnvdUser(gatingVer, opts.User)
	callCtx, cancel := withEnvdCallOpts(ctx, f.sbx, opts.User, opts.effectiveTimeout())
	defer cancel()

	body := r
	contentType := "application/octet-stream"
	if opts.ForceMultipart || compareEnvdVersions(gatingVer, envdOctetStreamUpload) < 0 {
		// Pre-0.5.7 envd (or explicit ForceMultipart) expects multipart/form-data
		// with the file in a "file" field whose filename carries the destination
		// path. Stream the body through an io.Pipe so memory stays bounded and a
		// cancelled context is observed promptly.
		pr, pw := io.Pipe()
		defer func() { _ = pr.Close() }()
		mw := multipart.NewWriter(pw)
		contentType = mw.FormDataContentType()
		go func() {
			part, err := mw.CreateFormFile("file", filePath)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, r); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.CloseWithError(mw.Close())
		}()
		body = pr
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, u, body)
	if err != nil {
		return nil, newSandboxError("build file request", err)
	}
	f.applyHeaders(req, resolvedUser)
	req.Header.Set("Content-Type", contentType)
	for k, v := range opts.Metadata {
		req.Header.Set(metadataHeaderPrefix+k, v)
	}

	resp, err := f.sbx.httpCli.Do(req)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, mapEnvdFileErr(resp, filePath)
	}

	info := &WriteInfo{
		Path: filePath,
		Name: path.Base(filePath),
		Type: EntryTypeFile,
	}
	var entries []httpFileEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err == nil && len(entries) > 0 {
		e := entries[0]
		if e.Path != "" {
			info.Path = e.Path
		}
		if e.Name != "" {
			info.Name = e.Name
		}
		if t := entryTypeFromString(e.Type); t != EntryTypeUnspecified {
			info.Type = t
		}
		if len(e.Metadata) > 0 {
			info.Metadata = maps.Clone(e.Metadata)
		}
	}
	return info, nil
}

func validateFileMetadata(metadata map[string]string) error {
	for k, v := range metadata {
		if k == "" || !isValidHTTPToken(k) {
			return &InvalidArgumentError{
				Message: fmt.Sprintf("invalid metadata key %q: keys must be non-empty HTTP token characters", k),
			}
		}
		if !isPrintableASCII(v) {
			return &InvalidArgumentError{
				Message: fmt.Sprintf("invalid metadata value for key %q: values must be printable US-ASCII", k),
			}
		}
	}
	return nil
}

func isValidHTTPToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return len(s) > 0
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Watch starts watching a directory for filesystem events. The returned handle
// fans events onto a channel; Stop() cancels the stream.
//
// Recursive watching requires envd >= v0.1.4; requesting it on an older build
// returns *TemplateError up front instead of failing opaquely at the RPC layer.
//
// Endpoint: filesystem.Filesystem/WatchDir (envd Connect-RPC)
// Envd: >= v0.1.0 (recursive >= v0.1.4)
func (f *Filesystem) Watch(ctx context.Context, dirPath string, recursive bool) (*WatchHandle, error) {
	return f.WatchWithOptions(ctx, dirPath, WatchOptions{Recursive: recursive})
}

// WatchWithOptions starts watching a directory for filesystem events with
// extended options (per-call User, RequestTimeout, IncludeEntry, and
// AllowNetworkMounts).
//
// Version gates:
//   - Recursive requires envd >= v0.1.4 (*TemplateError below)
//   - IncludeEntry requires envd >= v0.6.3 (*TemplateError below)
//   - AllowNetworkMounts requires envd >= v0.6.4 (*TemplateError below)
//
// Endpoint: filesystem.Filesystem/WatchDir (envd Connect-RPC)
// Envd: >= v0.1.0 (recursive >= v0.1.4; includeEntry >= v0.6.3; allowNetworkMounts >= v0.6.4)
func (f *Filesystem) WatchWithOptions(ctx context.Context, dirPath string, opts WatchOptions) (*WatchHandle, error) {
	ver := f.sbx.envdVersionForGating()
	rawVer := f.sbx.envdVersionRaw()
	if opts.Recursive && compareEnvdVersions(ver, envdRecursiveWatch) < 0 {
		return nil, &TemplateError{Message: fmt.Sprintf(
			"recursive directory watching requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdRecursiveWatch, rawVer)}
	}
	if opts.IncludeEntry && compareEnvdVersions(ver, envdWatchIncludeEntry) < 0 {
		return nil, &TemplateError{Message: fmt.Sprintf(
			"including entry info in watch events requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdWatchIncludeEntry, rawVer)}
	}
	if opts.AllowNetworkMounts && compareEnvdVersions(ver, envdWatchNetworkMounts) < 0 {
		return nil, &TemplateError{Message: fmt.Sprintf(
			"watching network mounts requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdWatchNetworkMounts, rawVer)}
	}

	callCtx, optCancel := withEnvdCallOpts(ctx, f.sbx, opts.User, opts.RequestTimeout)
	streamCtx, streamCancel := context.WithCancel(callCtx)
	cancelAll := func() {
		streamCancel()
		optCancel()
	}

	req := connect.NewRequest(&fspb.WatchDirRequest{
		Path:               dirPath,
		Recursive:          opts.Recursive,
		IncludeEntry:       opts.IncludeEntry,
		AllowNetworkMounts: opts.AllowNetworkMounts,
	})
	if opts.TimeoutMs > 0 {
		req.Header().Set("Connect-Timeout-Ms", strconv.Itoa(opts.TimeoutMs))
	}

	stream, err := f.sbx.envdClients().Filesystem.WatchDir(streamCtx, req)
	if err != nil {
		cancelAll()
		return nil, mapConnectErr(err)
	}
	h := &WatchHandle{
		ctx:    streamCtx,
		events: make(chan FilesystemEvent, 32),
		done:   make(chan struct{}),
		cancel: cancelAll,
	}
	go h.consume(stream)
	return h, nil
}

func entryInfoFromPB(e *fspb.EntryInfo) *EntryInfo {
	if e == nil {
		return &EntryInfo{}
	}
	var mt time.Time
	if e.ModifiedTime != nil {
		mt = e.ModifiedTime.AsTime()
	}
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
	switch {
	case e.GetIsSymlink(), e.GetType() == fspb.FileType_FILE_TYPE_SYMLINK:
		info.Type = EntryTypeSymlink
	case e.GetType() == fspb.FileType_FILE_TYPE_FILE:
		info.Type = EntryTypeFile
	case e.GetType() == fspb.FileType_FILE_TYPE_DIRECTORY:
		info.Type = EntryTypeDirectory
	}
	if e.SymlinkTarget != nil {
		info.SymlinkTarget = *e.SymlinkTarget
	}
	if md := e.GetMetadata(); len(md) > 0 {
		info.Metadata = maps.Clone(md)
	}
	return info
}
