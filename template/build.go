package template

import (
	"context"
	"errors"
	"fmt"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

const (
	defaultPollInterval = 200 * time.Millisecond
	defaultCPU          = 2
	defaultMemoryMB     = 1024
	buildEventBuffer    = 16
	logTailLimit        = 20
)

// BuildOptions captures every caller-tunable parameter of a template build.
type BuildOptions struct {
	Name         string
	Tags         []string
	CPUCount     int32
	MemoryMB     int32
	SkipCache    bool
	PollInterval time.Duration
}

// Build runs a server-side template build to completion. Logs are discarded;
// for streaming use BuildStream. Returns the completed BuildInfo or the
// first error that terminates the build.
func (c *Client) Build(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	ch, err := c.BuildStream(ctx, b, opts)
	if err != nil {
		return nil, err
	}
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
		return nil, &BuildError{Op: "poll", Err: errors.New("channel closed without terminal event")}
	}
	return done, nil
}

// BuildStream kicks off a build and returns a channel of BuildEvents. The
// channel closes after emitting a terminal Done or Err event.
func (c *Client) BuildStream(ctx context.Context, b *Builder, opts BuildOptions) (<-chan BuildEvent, error) {
	info, _, err := c.startBuild(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return c.streamBuild(ctx, info, opts), nil
}

// BuildInBackground kicks off a build without polling for completion. The
// caller can poll via GetBuildStatus using the returned BuildInfo.
func (c *Client) BuildInBackground(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	info, _, err := c.startBuild(ctx, b, opts)
	return info, err
}

// BuildV2 is the POST /v2/templates counterpart of Build, intended for
// self-hosted E2B deployments that predate /v3/templates (≤ 2.1.x). The v2
// request body has no tags field; BuildOptions.Tags must be empty.
func (c *Client) BuildV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	ch, err := c.BuildStreamV2(ctx, b, opts)
	if err != nil {
		return nil, err
	}
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
		return nil, &BuildError{Op: "poll", Err: errors.New("channel closed without terminal event")}
	}
	return done, nil
}

// BuildStreamV2 is the POST /v2/templates counterpart of BuildStream.
func (c *Client) BuildStreamV2(ctx context.Context, b *Builder, opts BuildOptions) (<-chan BuildEvent, error) {
	info, _, err := c.startBuildV2(ctx, b, opts)
	if err != nil {
		return nil, err
	}
	return c.streamBuild(ctx, info, opts), nil
}

// BuildInBackgroundV2 is the POST /v2/templates counterpart of
// BuildInBackground.
func (c *Client) BuildInBackgroundV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, error) {
	info, _, err := c.startBuildV2(ctx, b, opts)
	return info, err
}

func (c *Client) streamBuild(ctx context.Context, info *BuildInfo, opts BuildOptions) <-chan BuildEvent {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	events := make(chan BuildEvent, buildEventBuffer)
	go c.pollUntilDone(ctx, info, interval, events)
	return events
}

// GetBuildStatus fetches the current status of an in-flight build.
func (c *Client) GetBuildStatus(ctx context.Context, info BuildInfo, logsOffset int) (*BuildStatus, error) {
	return c.getBuildStatus(ctx, info.TemplateID, info.BuildID, logsOffset)
}

// startBuild performs validation, POST /v3/templates, file uploads, and the
// trigger request. It returns once the build is accepted by the server.
func (c *Client) startBuild(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, *apiclient.TemplateBuildStartV2, error) {
	cpu, mem, err := validateBuildOpts(opts)
	if err != nil {
		return nil, nil, err
	}
	reqResp, err := c.requestBuild(ctx, opts.Name, opts.Tags, cpu, mem)
	if err != nil {
		return nil, nil, err
	}
	tagsOut := []string(nil)
	if reqResp.Tags != nil {
		tagsOut = append([]string{}, reqResp.Tags...)
	}
	return c.finishStartBuild(ctx, b, opts, reqResp.TemplateID, reqResp.BuildID, tagsOut)
}

// startBuildV2 is the POST /v2/templates counterpart of startBuild. Tags are
// rejected — the v2 schema has no tags field.
func (c *Client) startBuildV2(ctx context.Context, b *Builder, opts BuildOptions) (*BuildInfo, *apiclient.TemplateBuildStartV2, error) {
	if len(opts.Tags) > 0 {
		return nil, nil, &e2b.InvalidArgumentError{
			Message: "BuildOptions.Tags is not supported by POST /v2/templates; use Build (v3) or drop Tags",
		}
	}
	cpu, mem, err := validateBuildOpts(opts)
	if err != nil {
		return nil, nil, err
	}
	reqResp, err := c.requestBuildV2(ctx, opts.Name, cpu, mem)
	if err != nil {
		return nil, nil, err
	}
	return c.finishStartBuild(ctx, b, opts, reqResp.TemplateID, reqResp.BuildID, nil)
}

func validateBuildOpts(opts BuildOptions) (cpu, mem int32, err error) {
	if opts.Name == "" {
		return 0, 0, &e2b.InvalidArgumentError{Message: "BuildOptions.Name is required"}
	}
	cpu = opts.CPUCount
	if cpu == 0 {
		cpu = defaultCPU
	}
	mem = opts.MemoryMB
	if mem == 0 {
		mem = defaultMemoryMB
	}
	return cpu, mem, nil
}

// finishStartBuild runs the version-agnostic tail of startBuild*: per-COPY
// cache probes and uploads, serialize the step list, and trigger the build
// via POST /v2/templates/{id}/builds/{buildID}.
func (c *Client) finishStartBuild(ctx context.Context, b *Builder, opts BuildOptions, templateID, buildID string, tags []string) (*BuildInfo, *apiclient.TemplateBuildStartV2, error) {
	steps, err := b.instructionsWithHashes()
	if err != nil {
		return nil, nil, err
	}

	// Per-COPY: check cache, upload if miss/forceUpload.
	for _, s := range steps {
		if s.Type != instTypeCopy {
			continue
		}
		link, err := c.getFileUploadLink(ctx, templateID, s.FilesHash)
		if err != nil {
			return nil, nil, err
		}
		needsUpload := !link.Present || (s.HasForceUpload && s.ForceUpload)
		if needsUpload && link.Url == nil {
			return nil, nil, &UploadError{
				Src:  s.Args[0],
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
		ignores := append([]string(nil), b.ignorePatterns...)
		if b.contextDir != "" {
			ignores = append(ignores, readDockerignore(b.contextDir)...)
		}
		src := s.Args[0]
		tarR, errc := tarFileStream(src, b.contextDir, ignores, resolve)
		if err := c.uploadFile(ctx, *link.Url, tarR); err != nil {
			tarR.Close()
			// Drain producer before returning so the goroutine exits.
			<-errc
			return nil, nil, &UploadError{Src: src, Hash: s.FilesHash, Err: err}
		}
		tarR.Close()
		if err := <-errc; err != nil {
			return nil, nil, &UploadError{Src: src, Hash: s.FilesHash, Err: err}
		}
	}

	body, err := b.serialize(opts.SkipCache)
	if err != nil {
		return nil, nil, err
	}
	if err := c.triggerBuild(ctx, templateID, buildID, *body); err != nil {
		return nil, nil, err
	}

	return &BuildInfo{
		TemplateID: templateID,
		BuildID:    buildID,
		Name:       opts.Name,
		Tags:       tags,
	}, body, nil
}

// pollUntilDone polls /builds/{id}/status until the build reaches a terminal
// state, forwarding logs and the final outcome into `out`.
func (c *Client) pollUntilDone(ctx context.Context, info *BuildInfo, interval time.Duration, out chan<- BuildEvent) {
	defer close(out)
	var (
		logsOffset int
		tail       = make([]LogEntry, 0, logTailLimit)
	)
	emitLog := func(le LogEntry) {
		if len(tail) == logTailLimit {
			tail = tail[1:]
		}
		tail = append(tail, le)
		out <- BuildEvent{Log: &le}
	}
	for {
		select {
		case <-ctx.Done():
			out <- BuildEvent{Err: ctx.Err()}
			return
		default:
		}
		status, err := c.getBuildStatus(ctx, info.TemplateID, info.BuildID, logsOffset)
		if err != nil {
			out <- BuildEvent{Err: err}
			return
		}
		logsOffset += len(status.Logs)
		for _, le := range status.Logs {
			emitLog(le)
		}
		switch status.Status {
		case BuildStatusReady:
			out <- BuildEvent{Done: info}
			return
		case BuildStatusError:
			msg, step := "", ""
			if status.Reason != nil {
				msg = status.Reason.Message
				step = status.Reason.Step
			}
			out <- BuildEvent{Err: &BuildError{
				Op:         "poll",
				TemplateID: info.TemplateID,
				BuildID:    info.BuildID,
				Step:       step,
				Message:    msg,
				LogTail:    append([]LogEntry(nil), tail...),
			}}
			return
		}
		select {
		case <-ctx.Done():
			out <- BuildEvent{Err: ctx.Err()}
			return
		case <-time.After(interval):
		}
	}
}
