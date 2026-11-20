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

// TagInfo is returned from Client.AssignTags and Client.AssignTagsWithOptions.
type TagInfo struct {
	BuildID string
	Tags    []string
}

// AssignTagsOptions configures optional parameters on Client.AssignTagsWithOptions.
type AssignTagsOptions struct {
	// IdempotencyKey is an optional team-scoped key (1..255 lowercase ASCII
	// letters, digits, or hyphens) sent as the Idempotency-Key request header.
	IdempotencyKey string
}

// TemplateTag is one row returned from Client.GetTags.
type TemplateTag struct {
	Tag       string
	BuildID   string
	CreatedAt time.Time
}

// TagExistsResult is returned from Client.TagExists.
type TagExistsResult struct {
	Exists        bool
	NormalizedTag string
}

// TagAssignment represents a single ready tag-assignment event for a template.
type TagAssignment struct {
	AssignmentID    string
	BuildID         string
	AssignedAt      time.Time
	BuildCreatedAt  time.Time
	BuildFinishedAt *time.Time
}

// TagGroup represents one distinct template tag together with its most recent
// ready assignment events, as returned by Client.ListTagGroups.
type TagGroup struct {
	Tag         string
	Assignments []TagAssignment
	HasMore     bool
}

// TagGroupsSort specifies the sort order for Client.ListTagGroups.
type TagGroupsSort string

const (
	TagGroupsSortLatestDesc TagGroupsSort = "latest_desc"
	TagGroupsSortLatestAsc  TagGroupsSort = "latest_asc"
	TagGroupsSortNameAsc    TagGroupsSort = "name_asc"
	TagGroupsSortNameDesc   TagGroupsSort = "name_desc"
)

// TagGroupsOptions filters and paginates Client.ListTagGroups.
type TagGroupsOptions struct {
	// AssignmentLimit caps ready assignment rows returned per tag. 0 uses server default.
	AssignmentLimit int32
	// TagsLimit caps distinct tags returned per page. 0 uses server default.
	TagsLimit int32
	// TagsCursor resumes from the NextCursor of a previous TagGroupsPage.
	TagsCursor string
	// Search filters tag names by substring (allowed chars: a-z, 0-9, ., _, -).
	Search string
	// Sort selects the tag group ordering. Empty uses server default.
	Sort TagGroupsSort
}

// TagGroupsPage is one keyset-paginated page returned by Client.ListTagGroups.
type TagGroupsPage struct {
	Tags       []TagGroup
	NextCursor string
}

// TagAssignmentsOptions paginates Client.GetTagAssignments.
type TagAssignmentsOptions struct {
	// Cursor resumes from the NextCursor of a previous TagAssignmentsPage.
	Cursor string
	// Limit caps assignment rows returned per page. 0 uses server default.
	Limit int32
}

// TagAssignmentsPage is one keyset-paginated page returned by Client.GetTagAssignments.
type TagAssignmentsPage struct {
	Data       []TagAssignment
	NextCursor string
}

// Template is a single row returned from Client.List, Client.ListV1,
// Client.ListV2, Client.ListAllV2, and Client.ListDefault. Mirrors the
// upstream Template schema while keeping internal API types private.
type Template struct {
	TemplateID    string
	BuildID       string
	Names         []string
	Aliases       []string
	Description   string
	Public        bool
	IsDefault     bool
	CPUCount      int32
	MaxCPUCount   *int32
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
	MaxCPUCount *int32
	MemoryMB    int32
	DiskSizeMB  *int32
	EnvdVersion string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
}

// TemplateDetail is the response from Client.Get: template summary metadata
// plus one page of its build history and the x-next-token pagination cursor.
type TemplateDetail struct {
	TemplateID    string
	BuildID       string
	Names         []string
	Aliases       []string
	Description   string
	Public        bool
	IsDefault     bool
	CPUCount      int32
	MaxCPUCount   *int32
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
	Builds        []TemplateBuild
	NextToken     string
}

// ListOptions filters Client.List / Client.ListV1 results. Zero value lists
// every template visible to the caller.
type ListOptions struct {
	// TeamID restricts results to a specific team. Empty means no filter.
	TeamID string
}

// ListV2Options filters and paginates Client.ListV2 and Client.ListAllV2.
type ListV2Options struct {
	// TeamID restricts results to a specific team. Empty means no filter.
	TeamID string
	// Names filters returned templates to those matching any of the given
	// template names, aliases, or IDs (both "alias" and "namespace/alias"
	// forms are matched). Empty means no filter.
	Names []string
	// Limit caps the number of templates fetched per server page. 0 uses the
	// server default (100).
	Limit int32
	// NextToken resumes pagination from a previous page's x-next-token cursor.
	NextToken string
}

// GetOptions paginates the build history returned by Client.Get. Zero value
// uses server defaults.
type GetOptions struct {
	NextToken string
	Limit     int32
}

// BuildStatusOptions configures Client.GetBuildStatusWithOptions.
type BuildStatusOptions struct {
	// LogsOffset is the starting build log index. 0 starts from the beginning.
	LogsOffset int
	// Limit caps the number of log entries returned (0–100). 0 uses server default.
	Limit int32
	// Level filters out entries below this level. Empty means no filter.
	Level LogLevel
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
	// Source selects the log store ("persistent" or "temporary"). Empty uses server default.
	Source string
}
