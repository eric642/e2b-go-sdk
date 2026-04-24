package e2b

import (
	"net/url"
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
	SandboxID           string
	Alias               string
	Domain              string
	TemplateID          string
	State               SandboxState
	CPUCount            int32
	MemoryMB            int32
	DiskSizeMB          int32
	StartedAt           time.Time
	EndAt               time.Time
	Metadata            map[string]string
	EnvdVersion         string
	EnvdAccessToken     string
	AllowInternetAccess *bool
	Network             *NetworkOptions
	Lifecycle           *LifecycleOptions
	VolumeMounts        []VolumeMount
}

// SandboxMetric is one sample of resource usage.
type SandboxMetric struct {
	CPUCount      int32
	CPUUsedPct    float32
	DiskTotal     int64
	DiskUsed      int64
	MemTotal      int64
	MemUsed       int64
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

// EntryInfo is the metadata for a file or directory.
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
}

// EntryType distinguishes file entries from directories.
type EntryType int

const (
	EntryTypeUnspecified EntryType = iota
	EntryTypeFile
	EntryTypeDirectory
)

// FilesystemEvent is one event emitted by Filesystem.Watch.
type FilesystemEvent struct {
	Name string
	Type FilesystemEventType
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

// WriteInfo is returned by Filesystem.Write.
type WriteInfo struct {
	Path string
	Name string
	Type EntryType
}

// sandboxInfoFromAPI converts the generated type into the public type.
func sandboxInfoFromAPI(d *apiclient.SandboxDetail) *SandboxInfo {
	info := &SandboxInfo{
		SandboxID:   d.SandboxID,
		TemplateID:  d.TemplateID,
		State:       SandboxState(d.State),
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
	if d.AllowInternetAccess != nil {
		info.AllowInternetAccess = d.AllowInternetAccess
	}
	if d.Metadata != nil {
		info.Metadata = map[string]string(*d.Metadata)
	}
	if d.Network != nil {
		info.Network = &NetworkOptions{}
		if d.Network.AllowOut != nil {
			info.Network.AllowOut = *d.Network.AllowOut
		}
		if d.Network.DenyOut != nil {
			info.Network.DenyOut = *d.Network.DenyOut
		}
		if d.Network.AllowPublicTraffic != nil {
			info.Network.AllowPublicTraffic = *d.Network.AllowPublicTraffic
		}
		if d.Network.MaskRequestHost != nil {
			info.Network.MaskRequestHost = *d.Network.MaskRequestHost
		}
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

func urlEscape(s string) string { return url.QueryEscape(s) }
