package template

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

const (
	defaultPollInterval = 200 * time.Millisecond
	buildEventBuffer    = 16
	logTailLimit        = 20
)

// BuildOptions captures every caller-tunable parameter of a template build.
type BuildOptions struct {
	// Name is the template name (or "name:tag" on Build v3). Required.
	Name string
	// Tags assigns additional tags during Build v3 (unsupported on BuildV2).
	Tags []string
	// CPUCount sets the sandbox vCPU count. Leave 0 to use the server/tier default.
	CPUCount int32
	// MaxCPUCount sets the maximum vCPU count for resizable templates (Build v3 only).
	MaxCPUCount *int32
	// MemoryMB sets the sandbox RAM in MiB. Leave 0 to use the server/tier default.
	MemoryMB int32
	// MinFreeDiskMB requests minimum free disk space in MiB after build steps (Build v3 only).
	MinFreeDiskMB *int32
	// SkipCache forces the entire template build to run without cache.
	SkipCache bool
	// PollInterval sets how frequently Build / BuildStream poll build status.
	// Defaults to 200ms when <= 0.
	PollInterval time.Duration
	// IdempotencyKey is an optional key (1..255 lowercase ASCII letters, digits,
	// or hyphens) sent as the Idempotency-Key header on POST /v3/templates and
	// POST /v2/templates/{id}/builds/{buildID} (unless TriggerIdempotencyKey
	// overrides the build-trigger step).
	IdempotencyKey string
	// TriggerIdempotencyKey optionally overrides the Idempotency-Key header sent
	// specifically on POST /v2/templates/{id}/builds/{buildID} when a deployment
	// enforces team-scoped idempotency across both build-create and build-start.
	TriggerIdempotencyKey string
	// OnBuildLogs, when non-nil, is invoked sequentially for every build log
	// entry observed while polling in Build, BuildStream, BuildV2, or BuildStreamV2.
	OnBuildLogs func(LogEntry)
}

// Build runs a server-side v3 template build to completion. When
// opts.OnBuildLogs is set, each live log entry is forwarded to it before Build
// returns; for channel-based streaming use BuildStream.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildV2 on < 2025.44).
func (c *Client) Build(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	ch, err := c.BuildStream(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return awaitBuildStream(ctx, ch)
}

// BuildStream kicks off a v3 template build and returns a channel of
// BuildEvents. The channel closes after emitting a terminal Done or Err event.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildStreamV2 on < 2025.44).
func (c *Client) BuildStream(ctx context.Context, b *Builder, opts BuildOptions) (<-chan BuildEvent, error) {
	info, err := c.startBuild(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return c.streamBuild(ctx, info, opts), nil
}

// BuildInBackground kicks off a v3 template build without polling for
// completion. Poll progress via GetBuildStatus using the returned BuildInfo.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildInBackgroundV2 on < 2025.44).
func (c *Client) BuildInBackground(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	return c.startBuild(ctx, b, opts)
}

// BuildV2 is the POST /v2/templates counterpart of Build. Tags, MaxCPUCount,
// and MinFreeDiskMB are not supported by the v2 request schema and must be unset.
//
// Unlike soft-deprecated v1 routes that remain active, POST /v2/templates is
// retired (returns 410 Gone) on E2B Cloud, while still actively served on
// self-hosted runtime <= 2026.30. Use BuildV2 on self-hosted runtime < 2025.44
// (where POST /v3/templates does not exist); use Build on E2B Cloud and
// self-hosted runtime 2025.44..2026.30. Never falls back to POST /v3/templates.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use Build instead.
func (c *Client) BuildV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	ch, err := c.BuildStreamV2(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return awaitBuildStream(ctx, ch)
}

// BuildStreamV2 is the POST /v2/templates counterpart of BuildStream.
// POST /v2/templates is retired (returns 410 Gone) on E2B Cloud, while still
// actively served on self-hosted runtime <= 2026.30 (and required on
// self-hosted runtime < 2025.44 where POST /v3/templates does not exist).
// Never falls back to POST /v3/templates.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use BuildStream instead.
func (c *Client) BuildStreamV2(ctx context.Context, b *Builder, opts BuildOptions) (<-chan BuildEvent, error) {
	info, err := c.startBuildV2(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return c.streamBuild(ctx, info, opts), nil
}

// BuildInBackgroundV2 is the POST /v2/templates counterpart of
// BuildInBackground. POST /v2/templates is retired (returns 410 Gone) on
// E2B Cloud, while still actively served on self-hosted runtime <= 2026.30
// (and required on self-hosted runtime < 2025.44 where POST /v3/templates does
// not exist). Never falls back to POST /v3/templates.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use BuildInBackground instead.
func (c *Client) BuildInBackgroundV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	return c.startBuildV2(ctx, b, opts)
}

func awaitBuildStream(ctx context.Context, ch <-chan BuildEvent) (*BuildInfo, error) {
	var done *BuildInfo
	for ev := range ch {
		if ev.Err != nil {
			return nil, ev.Err
		}
		if ev.Done != nil {
			done = ev.Done
		}
	}
	if done == nil {
		if ctx.Err() != nil {
			return nil, mapTransportErr(ctx.Err())
		}
		return nil, &BuildError{Op: "poll", Err: errors.New("channel closed without terminal event")}
	}
	return done, nil
}

func (c *Client) streamBuild(ctx context.Context, info *BuildInfo, opts BuildOptions) <-chan BuildEvent {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	events := make(chan BuildEvent, buildEventBuffer)
	go c.pollUntilDone(ctx, info, interval, opts.OnBuildLogs, events)
	return events
}

// GetBuildStatus fetches the current status and incremental log entries of an
// in-flight or completed template build.
//
// Endpoint: GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) GetBuildStatus(ctx context.Context, info BuildInfo, logsOffset int) (*BuildStatus, error) {
	return c.GetBuildStatusWithOptions(ctx, info, BuildStatusOptions{LogsOffset: logsOffset})
}

// GetBuildStatusWithOptions fetches the current status of a template build
// with optional log offset, limit, and minimum severity level filters.
//
// Endpoint: GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) GetBuildStatusWithOptions(ctx context.Context, info BuildInfo, opts BuildStatusOptions) (*BuildStatus, error) {
	return c.getBuildStatusWithOptions(ctx, info.TemplateID, info.BuildID, opts)
}

// Build delegates a finalized template build to c.Build without requiring a
// manual f.Builder() unwrap.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildV2 on < 2025.44).
func (f *FinalBuilder) Build(ctx context.Context, c *Client, opts BuildOptions) (*BuildInfo, error) {
	return c.Build(ctx, f.b, opts)
}

// BuildStream delegates a finalized streaming template build to c.BuildStream.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildStreamV2 on < 2025.44).
func (f *FinalBuilder) BuildStream(ctx context.Context, c *Client, opts BuildOptions) (<-chan BuildEvent, error) {
	return c.BuildStream(ctx, f.b, opts)
}

// BuildInBackground delegates a finalized background template build to c.BuildInBackground.
//
// Endpoint: POST /v3/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}
// Compatibility: E2B Cloud; self-hosted runtime 2025.44..2026.30 (use BuildInBackgroundV2 on < 2025.44).
func (f *FinalBuilder) BuildInBackground(ctx context.Context, c *Client, opts BuildOptions) (*BuildInfo, error) {
	return c.BuildInBackground(ctx, f.b, opts)
}

// BuildV2 delegates a finalized v2 template build to c.BuildV2.
// POST /v2/templates is actively served on self-hosted runtime <= 2026.30
// (required on < 2025.44) and retired (410 Gone) on E2B Cloud.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use Build instead.
func (f *FinalBuilder) BuildV2(ctx context.Context, c *Client, opts BuildOptions) (*BuildInfo, error) {
	return c.BuildV2(ctx, f.b, opts)
}

// BuildStreamV2 delegates a finalized v2 streaming template build to c.BuildStreamV2.
// POST /v2/templates is actively served on self-hosted runtime <= 2026.30
// (required on < 2025.44) and retired (410 Gone) on E2B Cloud.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}, GET /templates/{templateID}/builds/{buildID}/status
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use BuildStream instead.
func (f *FinalBuilder) BuildStreamV2(ctx context.Context, c *Client, opts BuildOptions) (<-chan BuildEvent, error) {
	return c.BuildStreamV2(ctx, f.b, opts)
}

// BuildInBackgroundV2 delegates a finalized v2 background build to c.BuildInBackgroundV2.
// POST /v2/templates is actively served on self-hosted runtime <= 2026.30
// (required on < 2025.44) and retired (410 Gone) on E2B Cloud.
//
// Endpoint: POST /v2/templates, GET /templates/{templateID}/files/{hash}, PUT <presigned-url>, POST /v2/templates/{templateID}/builds/{buildID}
// Compatibility: actively served on self-hosted runtime <= 2026.30 (required on < 2025.44); retired (410 Gone) on E2B Cloud — use BuildInBackground instead.
func (f *FinalBuilder) BuildInBackgroundV2(ctx context.Context, c *Client, opts BuildOptions) (*BuildInfo, error) {
	return c.BuildInBackgroundV2(ctx, f.b, opts)
}

func (c *Client) startBuild(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	if b == nil {
		return nil, &e2b.InvalidArgumentError{Message: "Builder must not be nil"}
	}
	if err := validateBuildOpts(opts); err != nil {
		return nil, err
	}
	steps, ignores, body, err := prepareBuild(b, opts.SkipCache)
	if err != nil {
		return nil, err
	}
	reqResp, err := c.requestBuildWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}
	var tagsOut []string
	if reqResp.Tags != nil {
		tagsOut = append([]string(nil), reqResp.Tags...)
	}
	return c.finishStartBuild(ctx, b, opts, steps, ignores, body, reqResp.TemplateID, reqResp.BuildID, tagsOut)
}

func (c *Client) startBuildV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	if b == nil {
		return nil, &e2b.InvalidArgumentError{Message: "Builder must not be nil"}
	}
	if len(opts.Tags) > 0 {
		return nil, &e2b.InvalidArgumentError{
			Message: "BuildOptions.Tags is not supported by POST /v2/templates; use Build (v3) or drop Tags",
		}
	}
	if opts.MaxCPUCount != nil {
		return nil, &e2b.InvalidArgumentError{
			Message: "BuildOptions.MaxCPUCount is not supported by POST /v2/templates; use Build (v3) or drop MaxCPUCount",
		}
	}
	if opts.MinFreeDiskMB != nil {
		return nil, &e2b.InvalidArgumentError{
			Message: "BuildOptions.MinFreeDiskMB is not supported by POST /v2/templates; use Build (v3) or drop MinFreeDiskMB",
		}
	}
	if err := validateBuildOpts(opts); err != nil {
		return nil, err
	}
	steps, ignores, body, err := prepareBuild(b, opts.SkipCache)
	if err != nil {
		return nil, err
	}
	reqResp, err := c.requestBuildV2(ctx, opts.Name, opts.CPUCount, opts.MemoryMB)
	if err != nil {
		return nil, err
	}
	return c.finishStartBuild(ctx, b, opts, steps, ignores, body, reqResp.TemplateID, reqResp.BuildID, nil)
}

func prepareBuild(b *Builder, skipCache bool) ([]instruction, []string, *apiclient.TemplateBuildStartV2, error) {
	if b.err != nil {
		return nil, nil, nil, b.err
	}
	ignores := b.combinedIgnores()
	steps, err := b.instructionsWithHashesWithIgnores(ignores)
	if err != nil {
		return nil, nil, nil, err
	}
	body, err := b.serializeSteps(steps, skipCache)
	if err != nil {
		return nil, nil, nil, err
	}
	return steps, ignores, body, nil
}

func validateBuildOpts(opts BuildOptions) error {
	if opts.Name == "" {
		return &e2b.InvalidArgumentError{Message: "BuildOptions.Name is required"}
	}
	if opts.CPUCount < 0 {
		return &e2b.InvalidArgumentError{Message: "BuildOptions.CPUCount must be >= 0"}
	}
	if opts.MemoryMB < 0 {
		return &e2b.InvalidArgumentError{Message: "BuildOptions.MemoryMB must be >= 0"}
	}
	if opts.MaxCPUCount != nil {
		if *opts.MaxCPUCount <= 0 {
			return &e2b.InvalidArgumentError{Message: "BuildOptions.MaxCPUCount must be > 0"}
		}
		if opts.CPUCount > 0 && *opts.MaxCPUCount < opts.CPUCount {
			return &e2b.InvalidArgumentError{Message: "BuildOptions.MaxCPUCount must be >= CPUCount"}
		}
	}
	if opts.MinFreeDiskMB != nil && *opts.MinFreeDiskMB < 0 {
		return &e2b.InvalidArgumentError{Message: "BuildOptions.MinFreeDiskMB must be >= 0"}
	}
	if err := validateIdempotencyKey(opts.IdempotencyKey); err != nil {
		return err
	}
	if err := validateIdempotencyKey(opts.TriggerIdempotencyKey); err != nil {
		return err
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 255 {
		return &e2b.InvalidArgumentError{
			Message: fmt.Sprintf("invalid IdempotencyKey: expected 1..255 characters, got %d", len(key)),
		}
	}
	for i := range len(key) {
		c := key[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return &e2b.InvalidArgumentError{
				Message: "invalid IdempotencyKey: only lowercase ASCII letters (a-z), digits (0-9), and hyphens (-) are allowed",
			}
		}
	}
	return nil
}

func (c *Client) finishStartBuild(ctx context.Context, b *Builder, opts BuildOptions, steps []instruction, ignores []string, body *apiclient.TemplateBuildStartV2, templateID, buildID string, tags []string) (*BuildInfo, error) {
	for _, s := range steps {
		if s.Type != instTypeCopy {
			continue
		}
		src := s.Args[0]
		link, err := c.getFileUploadLink(ctx, templateID, s.FilesHash)
		if err != nil {
			if ue, ok := errors.AsType[*UploadError](err); ok && ue.Src == "" {
				ue.Src = src
			}
			return nil, err
		}
		needsUpload := !link.Present || (s.HasForceUpload && s.ForceUpload)
		if needsUpload && link.Url == nil {
			return nil, &UploadError{
				Src:  src,
				Hash: s.FilesHash,
				Err:  fmt.Errorf("server reported present=false but returned no upload URL"),
			}
		}
		if !needsUpload {
			continue
		}
		resolve := defaultResolveSymlinks
		if s.ResolveSymlinks != nil {
			resolve = *s.ResolveSymlinks
		}
		archiveBytes, err := tarFileBytes(src, b.contextDir, ignores, resolve)
		if err != nil {
			return nil, &UploadError{Src: src, Hash: s.FilesHash, Err: err}
		}
		var linkHeaders map[string]string
		if link.Headers != nil {
			linkHeaders = *link.Headers
		}
		if err := c.uploadFile(ctx, *link.Url, bytes.NewReader(archiveBytes), linkHeaders); err != nil {
			if ue, ok := errors.AsType[*UploadError](err); ok {
				ue.Src = src
				ue.Hash = s.FilesHash
				return nil, ue
			}
			return nil, &UploadError{Src: src, Hash: s.FilesHash, Err: err}
		}
	}

	triggerKey := opts.IdempotencyKey
	if opts.TriggerIdempotencyKey != "" {
		triggerKey = opts.TriggerIdempotencyKey
	}
	if err := c.triggerBuild(ctx, templateID, buildID, *body, triggerKey); err != nil {
		return nil, err
	}

	return &BuildInfo{
		TemplateID: templateID,
		BuildID:    buildID,
		Name:       opts.Name,
		Tags:       tags,
	}, nil
}

func (c *Client) pollUntilDone(ctx context.Context, info *BuildInfo, interval time.Duration, onBuildLogs func(LogEntry), out chan<- BuildEvent) {
	defer close(out)
	var (
		logsOffset int
		tail       = make([]LogEntry, 0, logTailLimit)
	)
	sendCancel := func() {
		select {
		case out <- BuildEvent{Err: mapTransportErr(ctx.Err())}:
		default:
		}
	}
	sendTerminal := func(ev BuildEvent) {
		select {
		case out <- ev:
		case <-ctx.Done():
			sendCancel()
		}
	}
	emitLog := func(le LogEntry) bool {
		if len(tail) == logTailLimit {
			tail = tail[1:]
		}
		tail = append(tail, le)
		if onBuildLogs != nil {
			onBuildLogs(le)
		}
		leCopy := le
		select {
		case out <- BuildEvent{Log: &leCopy}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		select {
		case <-ctx.Done():
			sendCancel()
			return
		default:
		}
		status, err := c.getBuildStatus(ctx, info.TemplateID, info.BuildID, logsOffset)
		if err != nil {
			sendTerminal(BuildEvent{Err: err})
			return
		}
		logsOffset += len(status.Logs)
		for _, le := range status.Logs {
			if !emitLog(le) {
				sendCancel()
				return
			}
		}
		switch status.Status {
		case BuildStatusReady:
			sendTerminal(BuildEvent{Done: info})
			return
		case BuildStatusError:
			msg, step := "", ""
			if status.Reason != nil {
				msg = status.Reason.Message
				step = status.Reason.Step
			}
			sendTerminal(BuildEvent{Err: &BuildError{
				Op:         "poll",
				TemplateID: info.TemplateID,
				BuildID:    info.BuildID,
				Step:       step,
				Message:    msg,
				LogTail:    append([]LogEntry(nil), tail...),
			}})
			return
		}
		select {
		case <-ctx.Done():
			sendCancel()
			return
		case <-time.After(interval):
		}
	}
}
