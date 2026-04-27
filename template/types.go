package template

import "time"

// BuildStatusValue enumerates the possible states of a running template
// build, mirroring the upstream API enum.
type BuildStatusValue string

const (
	BuildStatusBuilding BuildStatusValue = "building"
	BuildStatusWaiting  BuildStatusValue = "waiting"
	BuildStatusReady    BuildStatusValue = "ready"
	BuildStatusError    BuildStatusValue = "error"
)

// BuildInfo identifies a completed or in-flight template build. Returned
// from Client.Build and Client.BuildInBackground.
type BuildInfo struct {
	TemplateID string
	BuildID    string
	Name       string
	Tags       []string
}

// BuildEvent is one emission from Client.BuildStream. Exactly one of Log,
// Done, or Err is non-nil at a time. The channel closes after emitting a
// terminal Done or Err.
type BuildEvent struct {
	Log  *LogEntry
	Done *BuildInfo
	Err  error
}

// BuildStatus is the server-reported state of a template build.
type BuildStatus struct {
	TemplateID string
	BuildID    string
	Status     BuildStatusValue
	Logs       []LogEntry
	Reason     *BuildReason
}

// BuildReason describes why a build ended in the error state.
type BuildReason struct {
	Step    string
	Message string
	Logs    []LogEntry
}

// TagInfo is returned from Client.AssignTags.
type TagInfo struct {
	BuildID string
	Tags    []string
}

// TemplateTag is one row returned from Client.GetTags.
type TemplateTag struct {
	Tag       string
	BuildID   string
	CreatedAt time.Time
}

// Template is a single row returned from Client.List and the summary portion
// of Client.Get. Mirrors the upstream Template schema but keeps internal API
// types private.
type Template struct {
	TemplateID    string
	BuildID       string
	Names         []string
	Public        bool
	CPUCount      int32
	MemoryMB      int32
	DiskSizeMB    int32
	EnvdVersion   string
	BuildStatus   BuildStatusValue
	BuildCount    int32
	SpawnCount    int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSpawnedAt *time.Time
	CreatedBy     *TemplateUser
}

// TemplateUser identifies the creator of a template.
type TemplateUser struct {
	ID    string
	Email string
}

// TemplateBuild is one historical build entry returned inside TemplateDetail.
type TemplateBuild struct {
	BuildID     string
	Status      BuildStatusValue
	CPUCount    int32
	MemoryMB    int32
	DiskSizeMB  *int32
	EnvdVersion string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
}

// TemplateDetail is the response from Client.Get: template identity plus one
// page of its build history. Per-build resource settings live on each
// TemplateBuild entry because they can differ across builds.
type TemplateDetail struct {
	TemplateID    string
	Names         []string
	Public        bool
	SpawnCount    int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSpawnedAt *time.Time
	Builds        []TemplateBuild
}

// ListOptions filters Client.List results. Zero value lists every template
// visible to the caller.
type ListOptions struct {
	// TeamID restricts results to a specific team. Empty means no filter.
	TeamID string
}

// GetOptions paginates the build history returned by Client.Get. Zero value
// uses server defaults.
type GetOptions struct {
	NextToken string
	Limit     int32
}

// BuildLogsOptions filters Client.GetBuildLogs.
type BuildLogsOptions struct {
	// CursorMs is the starting timestamp in milliseconds. 0 means server default.
	CursorMs int64
	// Limit caps the number of log entries returned (0–1000). 0 means server default.
	Limit int32
	// Direction is "asc" or "desc". Empty means server default.
	Direction string
	// Level filters out entries below this level. Empty means no filter.
	Level LogLevel
}
