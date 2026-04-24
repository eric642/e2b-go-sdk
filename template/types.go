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
