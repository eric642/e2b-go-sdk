// Package volume provides a client for E2B persistent volumes.
//
// A Volume is independent of a specific sandbox: it can be mounted via
// e2b.VolumeMount during Sandbox.Create / CreateV1. Control-plane operations
// (/volumes) manage volume lifecycles and mint per-volume JWTs, while content
// operations (/volumecontent/{volumeID}/*) read, stream, write, list, and
// mutate files and directories inside the volume.
package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/volumeapi"
)

// EntryType classifies a filesystem entry inside a persistent volume.
type EntryType string

const (
	EntryTypeUnknown   EntryType = "unknown"
	EntryTypeFile      EntryType = "file"
	EntryTypeDirectory EntryType = "directory"
	EntryTypeSymlink   EntryType = "symlink"
)

// EntryStat describes a file, directory, or symlink inside a persistent volume.
type EntryStat struct {
	Name   string    `json:"name"`
	Type   EntryType `json:"type"`
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	Mode   uint32    `json:"mode"`
	UID    uint32    `json:"uid"`
	GID    uint32    `json:"gid"`
	ATime  time.Time `json:"atime"`
	MTime  time.Time `json:"mtime"`
	CTime  time.Time `json:"ctime"`
	Target string    `json:"target,omitempty"`
}

// MetadataOptions configures ownership and permission updates in UpdateMetadata.
type MetadataOptions struct {
	UID  *uint32
	GID  *uint32
	Mode *uint32
}

// ListOptions configures directory listing in (*Volume).List.
type ListOptions struct {
	// Depth is the number of directory levels to recurse (default: 1 when 0).
	Depth int
}

// MakeDirOptions configures directory creation in (*Volume).MakeDirWithOptions.
type MakeDirOptions struct {
	UID  *uint32
	GID  *uint32
	Mode *uint32
	// Force creates parent directories if they do not exist (wire query param `force`).
	Force *bool
	// Parents is an alias for Force when Force is nil.
	Parents *bool
}

// WriteOptions configures file creation/overwrite in (*Volume).WriteFileStream.
type WriteOptions struct {
	UID   *uint32
	GID   *uint32
	Mode  *uint32
	Force *bool
}

// Volume is a handle for one persistent volume.
type Volume struct {
	ID     string
	Name   string
	Domain string
	token  string

	cfg    e2b.Config
	apiCli *apiclient.Client
	volCli *volumeapi.Client
}

// Token returns the per-volume JWT bearer token authenticating content calls.
// Callers can persist it and later pass it to (*Client).ConnectWithToken or
// Connect.
func (v *Volume) Token() string { return v.token }

// GetInfo retrieves control-plane metadata, a freshly minted content JWT, and
// the optional BYOC cluster domain for this volume.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) GetInfo(ctx context.Context) (*Info, error) {
	return getVolumeInfo(ctx, v.apiCli, v.ID)
}

// Destroy permanently deletes this volume on the control plane, returning
// (false, nil) when the volume was already gone (HTTP 404).
//
// Endpoint: DELETE /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) Destroy(ctx context.Context) (bool, error) {
	return destroyVolume(ctx, v.apiCli, v.ID)
}

// Delete removes this volume permanently on the control plane. Deleting a
// volume that is already gone (HTTP 404) succeeds as a no-op; use
// (*Volume).Destroy or (*Client).Destroy to distinguish whether a volume existed.
//
// Endpoint: DELETE /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) Delete(ctx context.Context) error {
	_, err := v.Destroy(ctx)
	return err
}

// Stat returns metadata for the file, directory, or symlink at path.
// Returns an error wrapping *e2b.FileNotFoundError when path does not exist.
//
// Endpoint: GET /volumecontent/{volumeID}/path
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) Stat(ctx context.Context, path string) (*EntryStat, error) {
	if err := validateVolumePath("stat path", v.ID, path); err != nil {
		return nil, err
	}
	reqCtx, cancel := v.unaryContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	resp, err := v.volCli.GetVolumecontentVolumeIDPath(
		reqCtx, v.ID,
		&volumeapi.GetVolumecontentVolumeIDPathParams{Path: path},
	)
	if err != nil {
		return nil, wrapErr("stat path", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeContentErr("stat path", resp, path)
	}
	return decodeEntryStat("stat path", resp.Body, path)
}

// Exists reports whether a file, directory, or symlink exists at path.
// It returns (false, nil) on HTTP 404 and propagates any other error.
//
// Endpoint: GET /volumecontent/{volumeID}/path
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) Exists(ctx context.Context, path string) (bool, error) {
	_, err := v.Stat(ctx, path)
	if err == nil {
		return true, nil
	}
	if _, ok := errors.AsType[*e2b.FileNotFoundError](err); ok {
		return false, nil
	}
	return false, err
}

// UpdateMetadata updates uid, gid, and/or mode on path and returns the updated
// EntryStat.
//
// Endpoint: PATCH /volumecontent/{volumeID}/path
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) UpdateMetadata(ctx context.Context, path string, opts MetadataOptions) (*EntryStat, error) {
	if err := validateVolumePath("update metadata", v.ID, path); err != nil {
		return nil, err
	}
	reqCtx, cancel := v.unaryContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	body := volumeapi.PatchVolumecontentVolumeIDPathJSONRequestBody{
		Uid:  opts.UID,
		Gid:  opts.GID,
		Mode: opts.Mode,
	}
	resp, err := v.volCli.PatchVolumecontentVolumeIDPath(
		reqCtx, v.ID,
		&volumeapi.PatchVolumecontentVolumeIDPathParams{Path: path},
		body,
	)
	if err != nil {
		return nil, wrapErr("update metadata", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeContentErr("update metadata", resp, path)
	}
	return decodeEntryStat("update metadata", resp.Body, path)
}

// List returns the entries inside the directory at path. Pass ListOptions{Depth}
// to recurse multiple levels (default depth on the server is 1).
//
// Endpoint: GET /volumecontent/{volumeID}/dir
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) List(ctx context.Context, path string, opts ...ListOptions) ([]EntryStat, error) {
	if err := validateVolumePath("list directory", v.ID, path); err != nil {
		return nil, err
	}
	params := &volumeapi.GetVolumecontentVolumeIDDirParams{Path: path}
	if len(opts) > 0 {
		if opts[0].Depth < 0 || int64(opts[0].Depth) > math.MaxUint32 {
			inv := &e2b.InvalidArgumentError{Message: "depth must be between 0 and math.MaxUint32"}
			return nil, &e2b.VolumeError{Message: "list directory", Cause: inv}
		}
		if opts[0].Depth > 0 {
			d := uint32(opts[0].Depth)
			params.Depth = &d
		}
	}
	reqCtx, cancel := v.unaryContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	resp, err := v.volCli.GetVolumecontentVolumeIDDir(reqCtx, v.ID, params)
	if err != nil {
		return nil, wrapErr("list directory", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeContentErr("list directory", resp, path)
	}
	var raw []volumeapi.VolumeEntryStat
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return []EntryStat{}, nil
		}
		return nil, &e2b.VolumeError{Message: "decode directory listing", Cause: err}
	}
	out := make([]EntryStat, 0, len(raw))
	for _, item := range raw {
		out = append(out, toEntryStat(item))
	}
	return out, nil
}

// MakeDir creates a directory at path. Use MakeDirWithOptions to configure
// uid, gid, mode, or parent-directory creation (`Force` / `Parents`) and
// inspect the resulting EntryStat.
//
// Endpoint: POST /volumecontent/{volumeID}/dir
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) MakeDir(ctx context.Context, path string) error {
	_, err := v.MakeDirWithOptions(ctx, path, MakeDirOptions{})
	return err
}

// MakeDirWithOptions creates a directory at path with optional ownership,
// permissions, and parent creation (`Force` or `Parents`), returning its
// EntryStat.
//
// Endpoint: POST /volumecontent/{volumeID}/dir
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) MakeDirWithOptions(ctx context.Context, path string, opts MakeDirOptions) (*EntryStat, error) {
	if err := validateVolumePath("make directory", v.ID, path); err != nil {
		return nil, err
	}
	reqCtx, cancel := v.unaryContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	force := opts.Force
	if force == nil {
		force = opts.Parents
	}
	params := &volumeapi.PostVolumecontentVolumeIDDirParams{
		Path:  path,
		Uid:   opts.UID,
		Gid:   opts.GID,
		Mode:  opts.Mode,
		Force: force,
	}
	resp, err := v.volCli.PostVolumecontentVolumeIDDir(reqCtx, v.ID, params)
	if err != nil {
		return nil, wrapErr("make directory", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeContentErr("make directory", resp, path)
	}
	return decodeEntryStat("make directory", resp.Body, path)
}

// Remove deletes a file or directory at path from the volume.
//
// Endpoint: DELETE /volumecontent/{volumeID}/path
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) Remove(ctx context.Context, path string) error {
	if err := validateVolumePath("remove path", v.ID, path); err != nil {
		return err
	}
	reqCtx, cancel := v.unaryContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	resp, err := v.volCli.DeleteVolumecontentVolumeIDPath(
		reqCtx, v.ID,
		&volumeapi.DeleteVolumecontentVolumeIDPathParams{Path: path},
	)
	if err != nil {
		return wrapErr("remove path", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return volumeContentErr("remove path", resp, path)
	}
	return nil
}

// ReadFile downloads the file at path into memory. When ctx has no deadline, a
// 1-hour transfer timeout (DefaultFileTransferTimeout) is applied.
//
// Endpoint: GET /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) ReadFile(ctx context.Context, path string) ([]byte, error) {
	rc, err := v.ReadFileStream(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, wrapErr("read file", err)
	}
	return data, nil
}

// ReadFileString downloads the file at path as a UTF-8 string. When ctx has no
// deadline, a 1-hour transfer timeout (DefaultFileTransferTimeout) is applied.
//
// Endpoint: GET /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) ReadFileString(ctx context.Context, path string) (string, error) {
	data, err := v.ReadFile(ctx, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadFileStream opens a streaming download for the file at path. On success
// (HTTP 200), the caller owns the returned io.ReadCloser and MUST close it to
// release the underlying connection and transfer timer.
//
// Endpoint: GET /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) ReadFileStream(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := validateVolumePath("read file", v.ID, path); err != nil {
		return nil, err
	}
	reqCtx, cancel := v.transferContext(ctx)
	resp, err := v.volCli.GetVolumecontentVolumeIDFile(
		reqCtx, v.ID,
		&volumeapi.GetVolumecontentVolumeIDFileParams{Path: path},
	)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, wrapErr("read file", err)
	}
	if resp.StatusCode >= 300 {
		err := volumeContentErr("read file", resp, path)
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	if cancel == nil {
		return resp.Body, nil
	}
	return &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel}, nil
}

// WriteFile uploads content to path inside the volume. Use WriteFileWithOptions
// or WriteFileStream to set uid/gid/mode/force options or inspect the returned
// EntryStat.
//
// Endpoint: PUT /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) WriteFile(ctx context.Context, path string, content []byte) error {
	_, err := v.WriteFileStream(ctx, path, bytes.NewReader(content))
	return err
}

// WriteFileWithOptions uploads content to path inside the volume with optional
// uid, gid, mode, and force query parameters, returning the written EntryStat.
//
// Endpoint: PUT /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) WriteFileWithOptions(ctx context.Context, path string, content []byte, opts WriteOptions) (*EntryStat, error) {
	return v.WriteFileStream(ctx, path, bytes.NewReader(content), opts)
}

// WriteFileStream streams r (`application/octet-stream`) to path inside the
// volume with optional uid, gid, mode, and force query parameters, returning
// the written file's EntryStat. When ctx has no deadline, a 1-hour transfer
// timeout (DefaultFileTransferTimeout) is applied.
//
// Endpoint: PUT /volumecontent/{volumeID}/file
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (v *Volume) WriteFileStream(ctx context.Context, path string, r io.Reader, opts ...WriteOptions) (*EntryStat, error) {
	if err := validateVolumePath("write file", v.ID, path); err != nil {
		return nil, err
	}
	reqCtx, cancel := v.transferContext(ctx)
	if cancel != nil {
		defer cancel()
	}
	params := &volumeapi.PutVolumecontentVolumeIDFileParams{Path: path}
	if len(opts) > 0 {
		params.Uid = opts[0].UID
		params.Gid = opts[0].GID
		params.Mode = opts[0].Mode
		params.Force = opts[0].Force
	}
	resp, err := v.volCli.PutVolumecontentVolumeIDFileWithBody(
		reqCtx, v.ID,
		params,
		"application/octet-stream",
		r,
	)
	if err != nil {
		return nil, wrapErr("write file", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeContentErr("write file", resp, path)
	}
	return decodeEntryStat("write file", resp.Body, path)
}

func validateVolumePath(op, volumeID, path string) error {
	if err := validateVolumeID(op, volumeID); err != nil {
		return err
	}
	if path == "" {
		return &e2b.VolumeError{
			Message: op,
			Cause:   &e2b.InvalidArgumentError{Message: "path must not be empty"},
		}
	}
	return nil
}

func (v *Volume) unaryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, nil
	}
	if v.cfg.RequestTimeoutDisabled || v.cfg.RequestTimeout <= 0 {
		return ctx, nil
	}
	return context.WithTimeout(ctx, v.cfg.RequestTimeout)
}

func (v *Volume) transferContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, nil
	}
	if v.cfg.RequestTimeoutDisabled {
		return ctx, nil
	}
	return context.WithTimeout(ctx, DefaultFileTransferTimeout)
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnCloseReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func decodeEntryStat(op string, r io.Reader, fallbackPath string) (*EntryStat, error) {
	var raw volumeapi.VolumeEntryStat
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return &EntryStat{Path: fallbackPath}, nil
		}
		return nil, &e2b.VolumeError{Message: op + ": decode entry stat", Cause: err}
	}
	stat := toEntryStat(raw)
	return &stat, nil
}

func toEntryStat(in volumeapi.VolumeEntryStat) EntryStat {
	return EntryStat{
		Name:   in.Name,
		Type:   EntryType(in.Type),
		Path:   in.Path,
		Size:   in.Size,
		Mode:   in.Mode,
		UID:    in.Uid,
		GID:    in.Gid,
		ATime:  in.Atime,
		MTime:  in.Mtime,
		CTime:  in.Ctime,
		Target: derefString(in.Target),
	}
}
