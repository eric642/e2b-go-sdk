package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

const maxForkCount = 20

// ForkOptions configures Fork (POST /sandboxes/{sandboxID}/fork).
type ForkOptions struct {
	// Count is the number of forked sandboxes to create (1..20). Zero omits
	// count so the server default (1) applies.
	Count int
	// Timeout sets the lifetime of the forked sandboxes. 0 omits timeout so
	// the server default applies. Sub-second positive durations round up to
	// the next whole second; negative or out-of-range durations return
	// *InvalidArgumentError.
	Timeout time.Duration
	// Metadata attaches optional key/value pairs when supported by the server.
	Metadata map[string]string
	// Envs sets optional environment variables when supported by the server.
	Envs map[string]string
	// Lifecycle sets optional timeout/auto-resume policy when supported by the server.
	Lifecycle *LifecycleOptions
}

// ForkResult holds the outcome of one requested fork. Exactly one of Sandbox or
// Err is non-nil: Sandbox when that fork started, or Err (implementing
// e2b.Error) when that fork failed to start.
type ForkResult struct {
	Sandbox *Sandbox
	Err     error
}

type forkRequestPayload struct {
	Count           *int32                             `json:"count,omitempty"`
	Timeout         *int32                             `json:"timeout,omitempty"`
	Metadata        map[string]string                  `json:"metadata,omitempty"`
	EnvVars         map[string]string                  `json:"envVars,omitempty"`
	AutoPause       *bool                              `json:"autoPause,omitempty"`
	AutoPauseMemory *bool                              `json:"autoPauseMemory,omitempty"`
	AutoResume      *apiclient.SandboxAutoResumeConfig `json:"autoResume,omitempty"`
}

// Fork checkpoints a running sandbox in place (preserving its ID and
// expiration) and boots one or more new sandboxes from that checkpoint. Each
// requested fork succeeds or fails independently in the returned slice.
//
// Endpoint: POST /sandboxes/{sandboxID}/fork
// Compatibility: E2B Cloud; self-hosted runtime 2026.29..2026.30 (not in runtime < 2026.29).
// Envd: >= v0.5.0
func (c *Client) Fork(ctx context.Context, sandboxID string, opts ...ForkOptions) ([]ForkResult, error) {
	return forkSandbox(ctx, c.cfg, c.apiCli, c.dataCli, sandboxID, opts...)
}

// Fork checkpoints this running sandbox in place (preserving its ID and
// expiration) and boots one or more new sandboxes from that checkpoint. Each
// requested fork succeeds or fails independently in the returned slice.
//
// Endpoint: POST /sandboxes/{sandboxID}/fork
// Compatibility: E2B Cloud; self-hosted runtime 2026.29..2026.30 (not in runtime < 2026.29).
// Envd: >= v0.5.0
func (s *Sandbox) Fork(ctx context.Context, opts ...ForkOptions) ([]ForkResult, error) {
	return forkSandbox(ctx, s.cfg, s.apiCli, s.httpCli, s.ID, opts...)
}

func forkSandbox(
	ctx context.Context,
	cfg Config,
	apiCli *apiclient.Client,
	dataCli *http.Client,
	sandboxID string,
	opts ...ForkOptions,
) ([]ForkResult, error) {
	var o ForkOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Count < 0 || o.Count > maxForkCount {
		return nil, &InvalidArgumentError{
			Message: fmt.Sprintf("fork count must be between 1 and %d (or 0 for default), got %d", maxForkCount, o.Count),
		}
	}

	autoPause, autoPauseMemory, autoResume, err := applyLifecycleOptions(o.Lifecycle)
	if err != nil {
		return nil, err
	}

	payload := forkRequestPayload{
		AutoPause:       autoPause,
		AutoPauseMemory: autoPauseMemory,
		AutoResume:      autoResume,
	}
	if o.Count > 0 {
		cnt := int32(o.Count)
		payload.Count = &cnt
	}
	if o.Timeout != 0 {
		sec, err := ceilSecondsInt32("timeout", o.Timeout)
		if err != nil {
			return nil, err
		}
		payload.Timeout = &sec
	}
	if len(o.Metadata) > 0 {
		payload.Metadata = maps.Clone(o.Metadata)
	}
	if len(o.Envs) > 0 {
		payload.EnvVars = maps.Clone(o.Envs)
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return nil, newSandboxError("encode fork request", err)
	}

	resp, err := apiCli.PostSandboxesSandboxIDForkWithBody(ctx, sandboxID, "application/json", bytes.NewReader(rawBody))
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}

	parsed, err := apiclient.ParsePostSandboxesSandboxIDForkResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse fork response", err)
	}
	if parsed.JSON201 == nil {
		return nil, nil
	}

	items := *parsed.JSON201
	out := make([]ForkResult, 0, len(items))
	for i := range items {
		item := items[i]
		if item.Error != nil || item.Sandbox == nil {
			out = append(out, ForkResult{Err: decodeForkItemError(item.Error)})
			continue
		}
		forked, bErr := newSandboxWithDeps(cfg, apiCli, dataCli, item.Sandbox)
		if bErr != nil {
			out = append(out, ForkResult{Err: bErr})
			continue
		}
		if o.Lifecycle != nil && o.Lifecycle.AutoResume {
			forked.autoResume = true
		}
		out = append(out, ForkResult{Sandbox: forked})
	}
	return out, nil
}

func decodeForkItemError(apiErr *apiclient.Error) error {
	if apiErr == nil {
		return newSandboxError("failed to start forked sandbox", nil)
	}
	status := int(apiErr.Code)
	var code string
	if apiErr.ErrorCode != nil {
		code = *apiErr.ErrorCode
	}
	fallback := apiErr.Message
	if fallback == "" {
		fallback = fmt.Sprintf("HTTP %d", status)
	}
	return classifyStatusError(status, code, apiErr.Message, "", fallback)
}
