package e2b

import (
	"maps"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// SandboxState reports whether a sandbox is running or paused.
type SandboxState string

const (
	SandboxStateRunning SandboxState = "running"
	SandboxStatePaused  SandboxState = "paused"
)

// SandboxInfo holds REST-reported metadata about a sandbox.
type SandboxInfo struct {
	SandboxID               string
	Alias                   string
	Domain                  string
	TemplateID              string
	State                   SandboxState
	PauseMode               string
	EnvdUnresponsiveAtPause bool
	CPUCount                int32
	MemoryMB                int32
	DiskSizeMB              int32
	StartedAt               time.Time
	EndAt                   time.Time
	Metadata                map[string]string
	EnvdVersion             string
	EnvdAccessToken         string
	AllowInternetAccess     *bool
	Network                 *SandboxNetworkInfo
	Lifecycle               *LifecycleOptions
	VolumeMounts            []VolumeMount
}

// MetricsOptions filters Sandbox.GetMetrics to a time window. Both bounds are
// optional; a zero time means "unbounded on that side".
type MetricsOptions struct {
	// Start, if non-zero, returns only samples at or after this time.
	Start time.Time
	// End, if non-zero, returns only samples at or before this time.
	End time.Time
}

// SandboxMetric is one sample of resource usage.
type SandboxMetric struct {
	CPUCount      int32
	CPUUsedPct    float32
	DiskTotal     int64
	DiskUsed      int64
	MemTotal      int64
	MemUsed       int64
	MemCache      int64
	Timestamp     time.Time
	TimestampUnix int64
}

// SnapshotInfo references a persisted sandbox snapshot.
type SnapshotInfo struct {
	SnapshotID string
	Names      []string
}

// ProcessInfo describes a running command or PTY inside the sandbox.
type ProcessInfo struct {
	PID  uint32
	Cmd  string
	Args []string
	Envs map[string]string
	Cwd  string
	Tag  string
}

// EntryInfo is the metadata for a file, directory, or symlink.
type EntryInfo struct {
	Name          string
	Path          string
	Type          EntryType
	Size          int64
	Mode          uint32
	Permissions   string
	Owner         string
	Group         string
	ModifiedTime  time.Time
	SymlinkTarget string
	Metadata      map[string]string
}

// EntryType distinguishes file, directory, and symlink entries.
type EntryType int

const (
	EntryTypeUnspecified EntryType = iota
	EntryTypeFile
	EntryTypeDirectory
	EntryTypeSymlink
)

// String returns the canonical string name of the entry type ("file",
// "directory", "symlink", or "" when unspecified).
func (t EntryType) String() string {
	switch t {
	case EntryTypeFile:
		return "file"
	case EntryTypeDirectory:
		return "directory"
	case EntryTypeSymlink:
		return "symlink"
	default:
		return ""
	}
}

// FilesystemEvent is one event emitted by Filesystem.Watch or
// Filesystem.WatchWithOptions.
type FilesystemEvent struct {
	Name string
	Type FilesystemEventType
	// Entry carries metadata for the affected path when WatchOptions.IncludeEntry
	// is true (envd >= v0.6.3) and the entry still exists on disk.
	Entry *EntryInfo
}

// FilesystemEventType enumerates filesystem event kinds.
type FilesystemEventType int

const (
	FsEventUnspecified FilesystemEventType = iota
	FsEventCreate
	FsEventWrite
	FsEventRemove
	FsEventRename
	FsEventChmod
)

// WriteInfo is returned by Filesystem.Write, WriteBytes, WriteString, and
// WriteFiles.
type WriteInfo struct {
	Path     string
	Name     string
	Type     EntryType
	Metadata map[string]string
}

// WriteEntry is one file to write via Filesystem.WriteFiles.
type WriteEntry struct {
	// Path is the destination path inside the sandbox.
	Path string
	// Data is the file content.
	Data []byte
}

// sandboxInfoFromAPI converts the generated type into the public type.
// Legacy servers (< 2025.31) returned the RunningSandbox schema without a
// state field; an empty state on a 200 OK response is normalized to running.
func sandboxInfoFromAPI(d *apiclient.SandboxDetail) *SandboxInfo {
	state := SandboxState(d.State)
	if state == "" {
		state = SandboxStateRunning
	}
	info := &SandboxInfo{
		SandboxID:   d.SandboxID,
		TemplateID:  d.TemplateID,
		State:       state,
		CPUCount:    int32(d.CpuCount),
		MemoryMB:    int32(d.MemoryMB),
		DiskSizeMB:  int32(d.DiskSizeMB),
		StartedAt:   d.StartedAt,
		EndAt:       d.EndAt,
		EnvdVersion: d.EnvdVersion,
	}
	if d.Alias != nil {
		info.Alias = *d.Alias
	}
	if d.Domain != nil {
		info.Domain = *d.Domain
	}
	if d.EnvdAccessToken != nil {
		info.EnvdAccessToken = *d.EnvdAccessToken
	}
	if d.PauseMode != nil {
		info.PauseMode = string(*d.PauseMode)
	}
	if d.EnvdUnresponsiveAtPause != nil {
		info.EnvdUnresponsiveAtPause = *d.EnvdUnresponsiveAtPause
	}
	if d.AllowInternetAccess != nil {
		allow := *d.AllowInternetAccess
		info.AllowInternetAccess = &allow
	}
	if d.Metadata != nil {
		info.Metadata = maps.Clone(map[string]string(*d.Metadata))
	}
	if d.Network != nil {
		info.Network = networkInfoFromAPI(d.Network)
	}
	if d.Lifecycle != nil {
		info.Lifecycle = &LifecycleOptions{
			OnTimeout:  string(d.Lifecycle.OnTimeout),
			AutoResume: d.Lifecycle.AutoResume,
		}
	}
	if d.VolumeMounts != nil {
		for _, m := range *d.VolumeMounts {
			info.VolumeMounts = append(info.VolumeMounts, VolumeMount{Name: m.Name, Path: m.Path})
		}
	}
	return info
}

// sandboxInfoFromListed converts a ListedSandbox (returned by the list
// endpoint) into the public type. ListedSandbox carries fewer fields than
// SandboxDetail, so Domain, EnvdAccessToken, AllowInternetAccess, Network,
// PauseMode, and Lifecycle are left zero — call GetInfo for the full record.
// Legacy v1 GET /sandboxes responses omit state (always running), so empty
// state is normalized to SandboxStateRunning.
func sandboxInfoFromListed(d *apiclient.ListedSandbox) *SandboxInfo {
	state := SandboxState(d.State)
	if state == "" {
		state = SandboxStateRunning
	}
	info := &SandboxInfo{
		SandboxID:   d.SandboxID,
		TemplateID:  d.TemplateID,
		State:       state,
		CPUCount:    int32(d.CpuCount),
		MemoryMB:    int32(d.MemoryMB),
		DiskSizeMB:  int32(d.DiskSizeMB),
		StartedAt:   d.StartedAt,
		EndAt:       d.EndAt,
		EnvdVersion: d.EnvdVersion,
	}
	if d.Alias != nil {
		info.Alias = *d.Alias
	}
	if d.EnvdUnresponsiveAtPause != nil {
		info.EnvdUnresponsiveAtPause = *d.EnvdUnresponsiveAtPause
	}
	if d.Metadata != nil {
		info.Metadata = maps.Clone(map[string]string(*d.Metadata))
	}
	if d.VolumeMounts != nil {
		for _, m := range *d.VolumeMounts {
			info.VolumeMounts = append(info.VolumeMounts, VolumeMount{Name: m.Name, Path: m.Path})
		}
	}
	return info
}
