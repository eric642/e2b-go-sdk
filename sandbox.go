package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// Defaults mirrored from the Python/JS SDKs.
const (
	DefaultSandboxTimeout = 300 * time.Second
	DefaultTemplate       = "base"
)

// Sandbox is a handle to a running (or paused) E2B sandbox.
//
// It exposes four per-sandbox modules:
//   - Commands: exec, signals, streaming stdin/stdout/stderr
//   - Pty: interactive pseudo-terminals
//   - Files: read/write/watch/compose
//   - Git: git operations running inside the sandbox
//
// Methods on *Sandbox are safe for concurrent use. ID is immutable; Domain,
// EnvdVersion, EnvdAccessToken, and TrafficAccessToken hold the initial
// snapshot populated by Create/Connect/Resume and are updated in place by
// Sandbox.Connect, Sandbox.ConnectV1, and Sandbox.Resume. Callers reading
// those values concurrently with Connect/Resume should use CurrentDomain,
// CurrentEnvdVersion, CurrentEnvdAccessToken, and CurrentTrafficAccessToken.
type Sandbox struct {
	ID string

	// Exported snapshots refreshed by Sandbox.Connect, ConnectV1, and Resume.
	// Use CurrentDomain, CurrentEnvdVersion, CurrentEnvdAccessToken, and
	// CurrentTrafficAccessToken when reading concurrently with reconnects.
	Domain             string
	EnvdVersion        string
	EnvdAccessToken    string
	TrafficAccessToken string

	// Per-sandbox sub-clients (non-nil after Create/Connect).
	Commands *Commands
	Pty      *Pty
	Files    *Filesystem
	Git      *Git

	cfg     Config
	apiCli  *apiclient.Client
	envd    *transport.EnvdClients
	httpCli *http.Client

	mu         sync.RWMutex
	autoResume bool
	mcpToken   string
}

// CreateOptions captures every parameter accepted by Client.Create and
// Client.CreateV1. All fields are optional except — implicitly — an API key or
// access token reachable via Config/env.
type CreateOptions struct {
	Config Config

	// Template ID or alias to build the sandbox from. Defaults to "base" (or
	// "mcp-gateway" when Mcp is non-nil).
	Template string
	// Timeout is the sandbox lifetime. Defaults to 5 minutes when 0; sub-second
	// positive durations round up to the next whole second, and negative or
	// out-of-range durations return *InvalidArgumentError.
	Timeout time.Duration
	// IdempotencyKey sets the Idempotency-Key header on Create (POST /v2/sandboxes)
	// using 1..255 lowercase ASCII letters, digits, or hyphens.
	IdempotencyKey string
	// CPUCount overrides the initial online vCPU count (1..template maxCpuCount).
	CPUCount *int32

	// Metadata attaches free-form key/value pairs.
	Metadata map[string]string
	// Envs sets environment variables inside the sandbox.
	Envs map[string]string

	// Secure enables authenticated envd access on legacy POST /sandboxes.
	//
	// Deprecated: Create (/v2/sandboxes) is always secured, and CreateV1
	// (/sandboxes) defaults to secure=true unless Insecure is true.
	Secure bool
	// Insecure explicitly sends secure=false on CreateV1 (POST /sandboxes) for
	// legacy self-hosted templates (envd < 0.2.0) that do not support envd
	// access tokens. Ignored by Create (/v2/sandboxes).
	Insecure bool

	// AllowInternetAccess enables outbound internet. Defaults to true.
	// Use AllowInternetAccessSet=true to send an explicit value.
	AllowInternetAccess    bool
	AllowInternetAccessSet bool

	// Network, Lifecycle, Iam, and VolumeMounts pass through to the API.
	Network      *NetworkOptions
	Lifecycle    *LifecycleOptions
	Iam          *IamOptions
	VolumeMounts []VolumeMount

	// Mcp configures and starts the in-sandbox MCP gateway (defaults Template
	// to "mcp-gateway" when Template is empty).
	Mcp map[string]any
}

// ConnectOptions configures (re)connecting to an existing sandbox.
type ConnectOptions struct {
	Config Config
	// Timeout sets the sandbox TTL. On Connect (/v2/sandboxes/{id}/connect),
	// 0 omits timeout so the server default (5 minutes) applies; on ConnectV1
	// (/sandboxes/{id}/connect), 0 sends DefaultSandboxTimeout. Sub-second
	// positive durations round up to the next whole second; negative or
	// out-of-range durations return *InvalidArgumentError.
	Timeout time.Duration
	// OnResume controls how a paused sandbox comes back: "restore" (restore
	// memory snapshot, memory=true) or "reboot" (cold-boot from disk,
	// memory=false). Empty omits memory so the server default applies.
	OnResume string
}

// ResumeOptions configures Resume and Client.ResumeV1 (POST /sandboxes/{sandboxID}/resume).
type ResumeOptions struct {
	Config Config
	// Timeout sets the resumed sandbox lifetime. 0 omits timeout so the server
	// default applies. Sub-second positive durations round up to the next whole
	// second; negative or out-of-range durations return *InvalidArgumentError.
	Timeout time.Duration
	// AutoPause overrides whether the resumed sandbox auto-pauses on timeout.
	AutoPause *bool
	// OnResume controls how the paused sandbox resumes: "restore" (memory=true)
	// or "reboot" (memory=false). Empty omits memory.
	OnResume string
	// CPUCount overrides the online vCPU target for the resumed sandbox.
	CPUCount *int32
}

// PauseOptions configures Pause (POST /sandboxes/{sandboxID}/pause). Leave
// Memory and RequireHealthyGuest nil to send an empty body compatible with all
// server versions.
type PauseOptions struct {
	Config Config
	// Memory controls whether a full memory snapshot (true, default) or a
	// filesystem-only snapshot (false; requires envd >= v0.5.0) is captured.
	Memory *bool
	// RequireHealthyGuest, when true, refuses the pause with HTTP 503 if envd
	// is not responding instead of taking an unresponsive memory snapshot.
	RequireHealthyGuest *bool
}

// Create provisions a new sandbox via POST /v2/sandboxes and returns a *Sandbox
// wired up with all sub-clients. For self-hosted deployments (runtime <= 2026.30),
// use CreateV1 (or NewClient(cfg).CreateV1).
//
// Deprecated: prefer NewClient(cfg).Create, which reuses one control-plane
// REST client across calls.
//
// Endpoint: POST /v2/sandboxes
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30; use CreateV1).
// Envd: >= v0.1.0
func Create(ctx context.Context, opts ...CreateOptions) (*Sandbox, error) {
	var o CreateOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return nil, err
	}
	return c.Create(ctx, o)
}

// CreateV1 provisions a new sandbox via POST /sandboxes using a one-shot Client
// built from opts.Config. Although soft-deprecated in OpenAPI in favor of
// Create (POST /v2/sandboxes), POST /sandboxes remains actively served (not
// retired) on both E2B Cloud and all self-hosted runtime <= 2026.30 deployments,
// and is the primary create endpoint on self-hosted runtime <= 2026.30.
//
// Deprecated: prefer NewClient(cfg).CreateV1, which reuses one control-plane
// REST client across calls.
//
// Endpoint: POST /sandboxes
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; primary create endpoint on self-hosted, soft-deprecated in OpenAPI in favor of Create on E2B Cloud).
// Envd: >= v0.2.0 with default secure=true (>= v0.1.0 when Insecure is true)
func CreateV1(ctx context.Context, opts ...CreateOptions) (*Sandbox, error) {
	var o CreateOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return nil, err
	}
	return c.CreateV1(ctx, o)
}

// Connect attaches to an existing sandbox (running or paused) via
// POST /v2/sandboxes/{sandboxID}/connect. For self-hosted deployments, use
// ConnectV1 or Resume.
//
// Deprecated: prefer NewClient(cfg).Connect (see Create).
//
// Endpoint: POST /v2/sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30; use ConnectV1 or Resume).
// Envd: >= v0.1.0
func Connect(ctx context.Context, sandboxID string, opts ...ConnectOptions) (*Sandbox, error) {
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return nil, err
	}
	return c.Connect(ctx, sandboxID, o)
}

// ConnectV1 attaches to an existing sandbox (running or paused) via
// POST /sandboxes/{sandboxID}/connect using a one-shot Client built from
// opts.Config. Soft-deprecated in OpenAPI in favor of Connect
// (POST /v2/sandboxes/{sandboxID}/connect), it remains actively served (not
// retired) on E2B Cloud and self-hosted runtime 2025.44..2026.30 and is the
// primary connect/wake endpoint on self-hosted 2025.44..2026.30 (use Resume on
// < 2025.44).
//
// Deprecated: prefer NewClient(cfg).ConnectV1 (see Create).
//
// Endpoint: POST /sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud and self-hosted runtime 2025.44..2026.30 (actively served; primary connect/wake endpoint on self-hosted 2025.44..2026.30; use Resume on < 2025.44).
// Envd: >= v0.1.0
func ConnectV1(ctx context.Context, sandboxID string, opts ...ConnectOptions) (*Sandbox, error) {
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return nil, err
	}
	return c.ConnectV1(ctx, sandboxID, o)
}

// Resume wakes a paused sandbox via POST /sandboxes/{sandboxID}/resume using a
// one-shot Client built from opts.Config.
//
// Soft-deprecated in OpenAPI since runtime 2025.44 introduced idempotent
// /connect (ConnectV1, whereas /resume returns *ConflictError / HTTP 409 if
// already running), POST /sandboxes/{sandboxID}/resume remains actively served
// (not retired) on both E2B Cloud and all self-hosted runtime <= 2026.30. It is
// required on self-hosted runtime < 2025.44 (where /connect does not exist),
// and useful on newer servers when overriding CPUCount or AutoPause on wake.
//
// Deprecated: prefer NewClient(cfg).Resume (see Create).
//
// Endpoint: POST /sandboxes/{sandboxID}/resume
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.44; soft-deprecated in OpenAPI in favor of Connect / ConnectV1).
// Envd: >= v0.1.0
func Resume(ctx context.Context, sandboxID string, opts ...ResumeOptions) (*Sandbox, error) {
	var o ResumeOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return nil, err
	}
	return c.Resume(ctx, sandboxID, o)
}

// Pause pauses a sandbox by ID using a one-shot Client built from opts.Config.
// Returns true on success, or false (nil error) if the sandbox was already
// paused (HTTP 409).
//
// Deprecated: prefer NewClient(cfg).Pause (see Create).
//
// Endpoint: POST /sandboxes/{sandboxID}/pause
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (PauseOptions body requires E2B Cloud or runtime >= 2026.25).
// Envd: >= v0.5.0 when PauseOptions.Memory is false
func Pause(ctx context.Context, sandboxID string, opts ...PauseOptions) (bool, error) {
	var o PauseOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return false, err
	}
	return c.Pause(ctx, sandboxID, opts...)
}

// Kill terminates a sandbox by ID. Returns false (nil error) if the sandbox
// was already gone.
//
// Deprecated: prefer NewClient(cfg).Kill (see Create).
//
// Endpoint: DELETE /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func Kill(ctx context.Context, sandboxID string, opts ...ConnectOptions) (bool, error) {
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := NewClient(o.Config)
	if err != nil {
		return false, err
	}
	return c.Kill(ctx, sandboxID)
}

// Connect reconnects to this sandbox (resuming it if paused) via
// POST /v2/sandboxes/{sandboxID}/connect and refreshes Domain, EnvdAccessToken,
// TrafficAccessToken, and EnvdVersion when returned by the server.
//
// Endpoint: POST /v2/sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30; use ConnectV1).
// Envd: >= v0.1.0
func (s *Sandbox) Connect(ctx context.Context, opts ...ConnectOptions) error {
	if s.cfg.Debug {
		return nil
	}
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	connected, err := connectSandboxV2(ctx, s.apiCli, s.ID, o)
	if err != nil {
		return err
	}
	return s.applyConnectedSandbox(connected)
}

// ConnectV1 reconnects to this sandbox (resuming it if paused) via
// POST /sandboxes/{sandboxID}/connect and refreshes Domain, EnvdAccessToken,
// TrafficAccessToken, and EnvdVersion when returned by the server.
//
// Marked soft-deprecated in OpenAPI in favor of Connect
// (POST /v2/sandboxes/{sandboxID}/connect), this endpoint remains actively
// served (not retired) on both E2B Cloud and self-hosted runtime 2025.44..2026.30.
// Choose ConnectV1 on self-hosted runtime 2025.44..2026.30; prefer Connect on
// E2B Cloud, and use Resume on self-hosted runtime < 2025.44.
//
// Endpoint: POST /sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud and self-hosted runtime 2025.44..2026.30 (actively served; primary connect/wake endpoint on self-hosted 2025.44..2026.30; use Resume on < 2025.44).
// Envd: >= v0.1.0
func (s *Sandbox) ConnectV1(ctx context.Context, opts ...ConnectOptions) error {
	if s.cfg.Debug {
		return nil
	}
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	connected, err := connectSandboxV1(ctx, s.apiCli, s.ID, o)
	if err != nil {
		return err
	}
	return s.applyConnectedSandbox(connected)
}

// Kill terminates this sandbox.
//
// Endpoint: DELETE /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (s *Sandbox) Kill(ctx context.Context) error {
	if s.cfg.Debug {
		return nil
	}
	resp, err := s.apiCli.DeleteSandboxesSandboxID(ctx, s.ID)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		drainResponseBody(resp.Body)
		return nil
	}
	return finishNoContent(resp, s.ID)
}

// SetTimeout updates the sandbox's inactivity timeout.
//
// Endpoint: POST /sandboxes/{sandboxID}/timeout
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (s *Sandbox) SetTimeout(ctx context.Context, d time.Duration) error {
	if s.cfg.Debug {
		return nil
	}
	return setSandboxTimeout(ctx, s.apiCli, s.ID, d)
}

// RefreshTimeout extends this sandbox's TTL monotonically via
// POST /sandboxes/{sandboxID}/refreshes: shorter durations never shrink an
// existing longer expiration, unlike SetTimeout which overwrites the TTL
// relative to the current server time.
//
// Remains actively served on both E2B Cloud and all self-hosted runtime <= 2026.30
// deployments. Choose RefreshTimeout for monotonic keepalive heartbeats; choose
// SetTimeout when setting an exact new TTL (including shortening it).
//
// Endpoint: POST /sandboxes/{sandboxID}/refreshes
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (actively served).
func (s *Sandbox) RefreshTimeout(ctx context.Context, duration time.Duration) error {
	if s.cfg.Debug {
		return nil
	}
	return refreshSandboxTimeout(ctx, s.apiCli, s.ID, duration)
}

// RefreshTimeoutV1 is a version-suffixed alias for RefreshTimeout
// (POST /sandboxes/{sandboxID}/refreshes). See RefreshTimeout for details.
//
// Endpoint: POST /sandboxes/{sandboxID}/refreshes
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (actively served).
func (s *Sandbox) RefreshTimeoutV1(ctx context.Context, duration time.Duration) error {
	return s.RefreshTimeout(ctx, duration)
}

// IsRunning reports whether this sandbox is currently alive and running.
//
// It adapts safely across E2B generations:
//   - In Debug mode it probes envd GET /health directly (200/204 -> true;
//     502/404/dial error -> false).
//   - When the sandbox was created with Lifecycle.AutoResume = true, it checks
//     control-plane GetInfo first so probing envd /health never wakes a paused
//     auto-resume sandbox.
//   - Otherwise it probes envd GET /health (200/204 -> true; 502 -> false),
//     falling back to control-plane GetInfo if a legacy proxy returns 404 or
//     when no envd listener is reachable in a control-plane-only test setup.
//
// Endpoint: GET /health (envd), with GET /sandboxes/{sandboxID} guard/fallback
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
// Envd: >= v0.1.0
func (s *Sandbox) IsRunning(ctx context.Context) (bool, error) {
	s.mu.RLock()
	autoResume := s.autoResume
	envd := s.envd
	domain := s.Domain
	envdTok := s.EnvdAccessToken
	envdVer := s.EnvdVersion
	s.mu.RUnlock()

	if !s.cfg.Debug {
		if autoResume && s.apiCli != nil {
			running, err := isSandboxRunningViaControlPlane(ctx, s.apiCli, s.ID)
			if err != nil || !running {
				return false, err
			}
		}
		if envd == nil && s.cfg.SandboxURL == "" {
			return isSandboxRunningViaControlPlane(ctx, s.apiCli, s.ID)
		}
	} else {
		envdVer = envdDebugFallback
	}

	reqCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		t := s.cfg.RequestTimeout
		if t == 0 && !s.cfg.RequestTimeoutDisabled {
			t = DefaultRequestTimeout
		}
		if t > 0 {
			var cancel context.CancelFunc
			reqCtx, cancel = context.WithTimeout(ctx, t)
			defer cancel()
		}
	}

	healthURL := strings.TrimRight(s.cfg.sandboxURL(s.ID, domain), "/") + "/health"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false, newSandboxError("build health request", err)
	}
	var auth transport.EnvdAuth
	if envd != nil {
		auth = envd.Auth
	} else {
		auth = transport.EnvdAuth{
			Token:     envdTok,
			User:      resolveEnvdUser(envdVer, ""),
			SandboxID: s.ID,
			Headers:   s.cfg.envdHeaders(),
		}
	}
	transport.ApplyEnvdHeaders(req, auth, resolveEnvdUser(envdVer, ""))

	hc := s.httpCli
	if hc == nil {
		hc = s.cfg.DataPlaneHTTPClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, contextErr(ctx.Err())
		}
		if s.cfg.Debug {
			return false, nil
		}
		if s.apiCli != nil {
			return isSandboxRunningViaControlPlane(ctx, s.apiCli, s.ID)
		}
		return false, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		drainResponseBody(resp.Body)
		return true, nil
	case resp.StatusCode == http.StatusBadGateway, resp.StatusCode == http.StatusNotFound && s.cfg.Debug:
		drainResponseBody(resp.Body)
		return false, nil
	case resp.StatusCode == http.StatusNotFound && s.apiCli != nil:
		drainResponseBody(resp.Body)
		return isSandboxRunningViaControlPlane(ctx, s.apiCli, s.ID)
	default:
		return false, mapHTTPErr(resp, s.ID)
	}
}

func isSandboxRunningViaControlPlane(ctx context.Context, apiCli *apiclient.Client, sandboxID string) (bool, error) {
	info, err := fetchSandboxInfo(ctx, apiCli, sandboxID)
	if err != nil {
		if _, ok := errors.AsType[*SandboxNotFoundError](err); ok {
			return false, nil
		}
		return false, err
	}
	return info.State == SandboxStateRunning, nil
}

// Pause pauses this sandbox. Returns true on success; false (nil error) if the
// sandbox was already paused (HTTP 409). When opts is omitted or zero, no JSON
// body is sent so legacy servers (< runtime 2026.25) accept the request.
//
// Endpoint: POST /sandboxes/{sandboxID}/pause
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (PauseOptions body requires E2B Cloud or runtime >= 2026.25).
// Envd: >= v0.5.0 when PauseOptions.Memory is false
func (s *Sandbox) Pause(ctx context.Context, opts ...PauseOptions) (bool, error) {
	return pauseSandbox(ctx, s.apiCli, s.ID, opts...)
}

// Resume wakes this paused sandbox via POST /sandboxes/{sandboxID}/resume and
// refreshes Domain, EnvdAccessToken, TrafficAccessToken, EnvdVersion, and the
// internal envd clients thread-safely when returned by the server.
//
//   - Why marked deprecated in OpenAPI: Soft-deprecated since runtime 2025.44
//     introduced idempotent /connect (ConnectV1, which attaches if running or
//     wakes if paused, whereas /resume returns *ConflictError / HTTP 409 if the
//     sandbox is already running).
//   - Availability: Remains actively served (not retired) on both E2B Cloud and
//     all self-hosted runtime <= 2026.30 deployments.
//   - When to use: Required on self-hosted runtime < 2025.44 (where /connect
//     does not exist) to wake paused sandboxes. On runtime >= 2025.44 or
//     E2B Cloud, prefer ConnectV1 / Connect for general reconnect/wake, and use
//     Resume when overriding CPUCount or AutoPause on wake or asserting that
//     the sandbox was paused (409 Conflict if running).
//
// Endpoint: POST /sandboxes/{sandboxID}/resume
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.44; soft-deprecated in OpenAPI in favor of Connect / ConnectV1).
// Envd: >= v0.1.0
func (s *Sandbox) Resume(ctx context.Context, opts ...ResumeOptions) error {
	if s.cfg.Debug {
		return nil
	}
	var o ResumeOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	resumed, err := resumeSandbox(ctx, s.apiCli, s.ID, o)
	if err != nil {
		return err
	}
	return s.applyConnectedSandbox(resumed)
}

// GetInfo fetches the sandbox metadata from the control plane. Legacy servers
// (< 2025.31) returning 200 OK with an empty state field are normalized to
// SandboxStateRunning.
//
// Endpoint: GET /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (s *Sandbox) GetInfo(ctx context.Context) (*SandboxInfo, error) {
	return fetchSandboxInfo(ctx, s.apiCli, s.ID)
}

// GetMetrics fetches CPU, memory (including MemCache), and disk usage samples
// for the sandbox. With no opts it returns the most recent samples; pass a
// single MetricsOptions to bound the time window (opts.Start / opts.End).
// Returns *TemplateError when EnvdVersion is known and below 0.1.5.
//
// Endpoint: GET /sandboxes/{sandboxID}/metrics
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (MemCache requires E2B Cloud or runtime >= 2026.14).
// Envd: >= v0.1.5
func (s *Sandbox) GetMetrics(ctx context.Context, opts ...MetricsOptions) ([]SandboxMetric, error) {
	if s.cfg.Debug {
		return nil, nil
	}
	envdVer := s.envdVersionRaw()
	if envdVer != "" && compareEnvdVersions(envdVer, envdMetrics) < 0 {
		return nil, &TemplateError{
			Message: fmt.Sprintf("sandbox metrics require envd >= %s, but this sandbox runs %s; rebuild the template to use GetMetrics", envdMetrics, envdVer),
		}
	}
	return fetchSandboxMetrics(ctx, s.apiCli, s.ID, opts...)
}

// GetHost returns the external hostname (or localhost:<port> in Debug mode) for
// the given sandbox port.
func (s *Sandbox) GetHost(port int) string {
	return s.cfg.sandboxHost(s.ID, s.CurrentDomain(), port)
}

// CurrentDomain returns the sandbox's current domain safely under the
// sandbox's internal lock. Prefer this over reading Domain directly when
// Connect, ConnectV1, or Resume may run concurrently.
func (s *Sandbox) CurrentDomain() string {
	s.mu.RLock()
	v := s.Domain
	s.mu.RUnlock()
	return v
}

// CurrentEnvdVersion returns the sandbox's current envd version safely under
// the sandbox's internal lock. Prefer this over reading EnvdVersion directly
// when Connect, ConnectV1, or Resume may run concurrently.
func (s *Sandbox) CurrentEnvdVersion() string {
	s.mu.RLock()
	v := s.EnvdVersion
	s.mu.RUnlock()
	return v
}

// CurrentEnvdAccessToken returns the sandbox's current envd access token
// safely under the sandbox's internal lock. Prefer this over reading
// EnvdAccessToken directly when Connect, ConnectV1, or Resume may run
// concurrently.
func (s *Sandbox) CurrentEnvdAccessToken() string {
	s.mu.RLock()
	v := s.EnvdAccessToken
	s.mu.RUnlock()
	return v
}

// CurrentTrafficAccessToken returns the sandbox's current traffic access token
// safely under the sandbox's internal lock. Prefer this over reading
// TrafficAccessToken directly when Connect, ConnectV1, or Resume may run
// concurrently.
func (s *Sandbox) CurrentTrafficAccessToken() string {
	s.mu.RLock()
	v := s.TrafficAccessToken
	s.mu.RUnlock()
	return v
}

func (c *Client) newSandbox(created *apiclient.Sandbox) (*Sandbox, error) {
	return newSandboxWithDeps(c.cfg, c.apiCli, c.dataCli, created)
}

func newSandboxWithDeps(cfg Config, apiCli *apiclient.Client, dataCli *http.Client, created *apiclient.Sandbox) (*Sandbox, error) {
	if dataCli == nil {
		dataCli = cfg.DataPlaneHTTPClient()
	}
	sbx := &Sandbox{
		ID:          created.SandboxID,
		EnvdVersion: created.EnvdVersion,
		cfg:         cfg,
		apiCli:      apiCli,
		httpCli:     dataCli,
	}
	if created.Domain != nil {
		sbx.Domain = *created.Domain
	}
	if sbx.Domain == "" {
		sbx.Domain = cfg.Domain
	}
	if created.EnvdAccessToken != nil {
		sbx.EnvdAccessToken = *created.EnvdAccessToken
	}
	if created.TrafficAccessToken != nil {
		sbx.TrafficAccessToken = *created.TrafficAccessToken
	}

	envdBase := cfg.sandboxURL(sbx.ID, sbx.Domain)
	envdAuth := transport.EnvdAuth{
		Token:     sbx.EnvdAccessToken,
		User:      resolveEnvdUser(sbx.envdVersionForGating(), ""),
		SandboxID: sbx.ID,
		Headers:   cfg.envdHeaders(),
	}
	envd, err := transport.NewEnvdClients(envdBase, dataCli, envdAuth)
	if err != nil {
		return nil, newSandboxError("init envd client", err)
	}
	sbx.envd = envd
	sbx.Commands = &Commands{sbx: sbx}
	sbx.Pty = &Pty{sbx: sbx}
	sbx.Files = &Filesystem{sbx: sbx}
	sbx.Git = &Git{cmds: sbx.Commands}
	return sbx, nil
}

func (c *Client) finalizeCreatedSandbox(ctx context.Context, created *apiclient.Sandbox, opts CreateOptions) (*Sandbox, error) {
	sbx, err := c.newSandbox(created)
	if err != nil {
		return nil, err
	}
	if opts.Lifecycle != nil && opts.Lifecycle.AutoResume {
		sbx.autoResume = true
	}

	if !c.cfg.Debug && sbx.EnvdVersion != "" && compareEnvdVersions(sbx.envdVersionForGating(), "0.1.0") < 0 {
		c.killBestEffort(ctx, sbx.ID)
		return nil, &TemplateError{
			Message: fmt.Sprintf("template envd version %s is unsupported (requires envd >= 0.1.0); rebuild the template to use this SDK", sbx.EnvdVersion),
		}
	}

	if opts.Mcp != nil {
		if err := c.bootstrapMCP(ctx, sbx, opts.Mcp); err != nil {
			return nil, err
		}
	}
	return sbx, nil
}

func (s *Sandbox) applyConnectedSandbox(connected *apiclient.Sandbox) error {
	if connected == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	domain := s.Domain
	envdTok := s.EnvdAccessToken
	trafficTok := s.TrafficAccessToken
	envdVer := s.EnvdVersion

	if connected.Domain != nil && *connected.Domain != "" {
		domain = *connected.Domain
	}
	if domain == "" {
		domain = s.cfg.Domain
	}
	if connected.EnvdAccessToken != nil {
		envdTok = *connected.EnvdAccessToken
	}
	if connected.TrafficAccessToken != nil {
		trafficTok = *connected.TrafficAccessToken
	}
	if connected.EnvdVersion != "" {
		envdVer = connected.EnvdVersion
	}

	var envd *transport.EnvdClients
	if s.httpCli != nil {
		gatingVer := envdVer
		if s.cfg.Debug {
			gatingVer = envdDebugFallback
		}
		envdBase := s.cfg.sandboxURL(s.ID, domain)
		envdAuth := transport.EnvdAuth{
			Token:     envdTok,
			User:      resolveEnvdUser(gatingVer, ""),
			SandboxID: s.ID,
			Headers:   s.cfg.envdHeaders(),
		}
		var err error
		envd, err = transport.NewEnvdClients(envdBase, s.httpCli, envdAuth)
		if err != nil {
			return newSandboxError("reinit envd client", err)
		}
	}

	s.Domain = domain
	s.EnvdAccessToken = envdTok
	s.TrafficAccessToken = trafficTok
	s.EnvdVersion = envdVer
	if envd != nil {
		s.envd = envd
	}
	return nil
}

func buildNewSandboxV2(opts CreateOptions) (*apiclient.PostV2SandboxesParams, apiclient.PostV2SandboxesJSONRequestBody, error) {
	var params *apiclient.PostV2SandboxesParams
	if opts.IdempotencyKey != "" {
		if err := validateIdempotencyKey(opts.IdempotencyKey); err != nil {
			return nil, apiclient.PostV2SandboxesJSONRequestBody{}, err
		}
		key := opts.IdempotencyKey
		params = &apiclient.PostV2SandboxesParams{IdempotencyKey: &key}
	}

	common, err := buildSandboxCommonFields(opts)
	if err != nil {
		return nil, apiclient.PostV2SandboxesJSONRequestBody{}, err
	}
	return params, common, nil
}

func buildNewSandboxV1(opts CreateOptions) (apiclient.NewSandbox, error) {
	common, err := buildSandboxCommonFields(opts)
	if err != nil {
		return apiclient.NewSandbox{}, err
	}
	secure := !opts.Insecure
	return apiclient.NewSandbox{
		AllowInternetAccess: common.AllowInternetAccess,
		AutoPause:           common.AutoPause,
		AutoPauseMemory:     common.AutoPauseMemory,
		AutoResume:          common.AutoResume,
		CpuCount:            common.CpuCount,
		EnvVars:             common.EnvVars,
		Iam:                 common.Iam,
		Mcp:                 common.Mcp,
		Metadata:            common.Metadata,
		Network:             common.Network,
		Secure:              &secure,
		TemplateID:          common.TemplateID,
		Timeout:             common.Timeout,
		VolumeMounts:        common.VolumeMounts,
	}, nil
}

func buildSandboxCommonFields(opts CreateOptions) (apiclient.NewSandboxV2, error) {
	template := opts.Template
	if template == "" {
		if opts.Mcp != nil {
			template = DefaultMCPTemplate
		} else {
			template = DefaultTemplate
		}
	}
	timeoutSec := int32(DefaultSandboxTimeout / time.Second)
	if opts.Timeout != 0 {
		var err error
		timeoutSec, err = ceilSecondsInt32("timeout", opts.Timeout)
		if err != nil {
			return apiclient.NewSandboxV2{}, err
		}
	}

	var cpuCount *int32
	if opts.CPUCount != nil {
		if *opts.CPUCount < 1 {
			return apiclient.NewSandboxV2{}, &InvalidArgumentError{Message: "CPUCount must be >= 1"}
		}
		v := *opts.CPUCount
		cpuCount = &v
	}

	netCfg, err := buildNetworkConfig(opts.Network)
	if err != nil {
		return apiclient.NewSandboxV2{}, err
	}
	iamCfg, err := buildSandboxIam(opts.Iam)
	if err != nil {
		return apiclient.NewSandboxV2{}, err
	}
	autoPause, autoPauseMemory, autoResume, err := applyLifecycleOptions(opts.Lifecycle)
	if err != nil {
		return apiclient.NewSandboxV2{}, err
	}

	body := apiclient.NewSandboxV2{
		TemplateID:      template,
		Timeout:         &timeoutSec,
		CpuCount:        cpuCount,
		Network:         netCfg,
		Iam:             iamCfg,
		AutoPause:       autoPause,
		AutoPauseMemory: autoPauseMemory,
		AutoResume:      autoResume,
	}
	if opts.AllowInternetAccessSet {
		allow := opts.AllowInternetAccess
		body.AllowInternetAccess = &allow
	}
	if len(opts.Metadata) > 0 {
		md := apiclient.SandboxMetadata(maps.Clone(opts.Metadata))
		body.Metadata = &md
	}
	if len(opts.Envs) > 0 {
		ev := apiclient.EnvVars(maps.Clone(opts.Envs))
		body.EnvVars = &ev
	}
	if opts.Mcp != nil {
		mcp := apiclient.Mcp(maps.Clone(opts.Mcp))
		body.Mcp = &mcp
	}
	if len(opts.VolumeMounts) > 0 {
		vs := make([]apiclient.SandboxVolumeMount, 0, len(opts.VolumeMounts))
		for _, m := range opts.VolumeMounts {
			vs = append(vs, apiclient.SandboxVolumeMount{Name: m.Name, Path: m.Path})
		}
		body.VolumeMounts = &vs
	}
	return body, nil
}

func applyLifecycleOptions(lc *LifecycleOptions) (autoPause *bool, autoPauseMemory *bool, autoResume *apiclient.SandboxAutoResumeConfig, err error) {
	if lc == nil {
		return nil, nil, nil, nil
	}
	switch lc.OnTimeout {
	case "":
	case "pause":
		autoPause = new(true)
	case "kill":
		autoPause = new(false)
	default:
		return nil, nil, nil, &InvalidArgumentError{
			Message: fmt.Sprintf("lifecycle.OnTimeout must be \"pause\" or \"kill\", got %q", lc.OnTimeout),
		}
	}

	if lc.Mode != "" {
		if lc.OnTimeout != "pause" {
			return nil, nil, nil, &InvalidArgumentError{
				Message: "lifecycle.Mode is only allowed when lifecycle.OnTimeout is \"pause\"",
			}
		}
		mem, mErr := resolveSnapshotModeMemory("lifecycle.Mode", lc.Mode)
		if mErr != nil {
			return nil, nil, nil, mErr
		}
		autoPauseMemory = mem
	}

	if lc.AutoResume {
		if lc.OnTimeout == "kill" {
			return nil, nil, nil, &InvalidArgumentError{
				Message: "lifecycle.AutoResume cannot be true when lifecycle.OnTimeout is \"kill\"",
			}
		}
		if autoPauseMemory != nil && !*autoPauseMemory {
			return nil, nil, nil, &InvalidArgumentError{
				Message: "lifecycle.AutoResume cannot be combined with lifecycle.Mode \"filesystem\"",
			}
		}
		autoResume = &apiclient.SandboxAutoResumeConfig{Enabled: true}
	}
	return autoPause, autoPauseMemory, autoResume, nil
}

func resolveOnResumeMemory(onResume string) (*bool, error) {
	switch onResume {
	case "":
		return nil, nil
	case "restore":
		return new(true), nil
	case "reboot":
		return new(false), nil
	default:
		return nil, &InvalidArgumentError{
			Message: fmt.Sprintf("onResume must be \"restore\" or \"reboot\", got %q", onResume),
		}
	}
}

func validateIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 255 {
		return &InvalidArgumentError{
			Message: fmt.Sprintf("idempotencyKey must be 1..255 characters, got %d", len(key)),
		}
	}
	for i := range len(key) {
		ch := key[i]
		isLower := ch >= 'a' && ch <= 'z'
		isDigit := ch >= '0' && ch <= '9'
		if !isLower && !isDigit && ch != '-' {
			return &InvalidArgumentError{
				Message: "idempotencyKey may only contain lowercase ASCII letters, digits, and hyphens",
			}
		}
	}
	return nil
}

func parseCreatedSandboxResponse(json201 *apiclient.Sandbox, rawBody []byte, status int) (*apiclient.Sandbox, error) {
	if json201 != nil {
		return json201, nil
	}
	if status == http.StatusOK && len(rawBody) > 0 {
		var fallback apiclient.Sandbox
		if err := json.Unmarshal(rawBody, &fallback); err == nil && fallback.SandboxID != "" {
			return &fallback, nil
		}
	}
	return nil, newSandboxError("empty sandbox response body", nil)
}

func connectSandboxV2(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ConnectOptions) (*apiclient.Sandbox, error) {
	mem, err := resolveOnResumeMemory(opts.OnResume)
	if err != nil {
		return nil, err
	}
	body := apiclient.ConnectSandboxV2{Memory: mem}
	if opts.Timeout != 0 {
		sec, err := ceilSecondsInt32("timeout", opts.Timeout)
		if err != nil {
			return nil, err
		}
		body.Timeout = &sec
	}
	resp, err := apiCli.PostV2SandboxesSandboxIDConnect(ctx, sandboxID, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostV2SandboxesSandboxIDConnectResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse connect response", err)
	}
	if parsed.JSON201 != nil {
		return parsed.JSON201, nil
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200, nil
	}
	return nil, newSandboxError("empty connect response body", nil)
}

func connectSandboxV1(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ConnectOptions) (*apiclient.Sandbox, error) {
	mem, err := resolveOnResumeMemory(opts.OnResume)
	if err != nil {
		return nil, err
	}
	timeoutSec := int32(DefaultSandboxTimeout / time.Second)
	if opts.Timeout != 0 {
		timeoutSec, err = ceilSecondsInt32("timeout", opts.Timeout)
		if err != nil {
			return nil, err
		}
	}
	body := apiclient.ConnectSandbox{
		Timeout: timeoutSec,
		Memory:  mem,
	}
	resp, err := apiCli.PostSandboxesSandboxIDConnect(ctx, sandboxID, body) // v1 POST /sandboxes/{sandboxID}/connect is required on self-hosted runtime 2025.44..2026.30.
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesSandboxIDConnectResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse v1 connect response", err)
	}
	if parsed.JSON201 != nil {
		return parsed.JSON201, nil
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200, nil
	}
	return nil, newSandboxError("empty connect response body", nil)
}

func resumeSandbox(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ResumeOptions) (*apiclient.Sandbox, error) {
	mem, err := resolveOnResumeMemory(opts.OnResume)
	if err != nil {
		return nil, err
	}
	body := apiclient.ResumedSandbox{Memory: mem}
	if opts.Timeout != 0 {
		sec, err := ceilSecondsInt32("timeout", opts.Timeout)
		if err != nil {
			return nil, err
		}
		body.Timeout = &sec
	}
	if opts.AutoPause != nil {
		ap := *opts.AutoPause
		body.AutoPause = &ap
	}
	if opts.CPUCount != nil {
		if *opts.CPUCount < 1 {
			return nil, &InvalidArgumentError{Message: "resume CPUCount must be >= 1"}
		}
		cpu := *opts.CPUCount
		body.CpuCount = &cpu
	}

	resp, err := apiCli.PostSandboxesSandboxIDResume(ctx, sandboxID, body) // v1 POST /sandboxes/{sandboxID}/resume is required on self-hosted runtime < 2025.44.
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesSandboxIDResumeResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse resume response", err)
	}
	return parseCreatedSandboxResponse(parsed.JSON201, parsed.Body, resp.StatusCode)
}

func pauseSandbox(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ...PauseOptions) (bool, error) {
	var resp *http.Response
	var err error
	if len(opts) > 0 && (opts[0].Memory != nil || opts[0].RequireHealthyGuest != nil) {
		body := apiclient.SandboxPauseRequest{}
		if opts[0].Memory != nil {
			m := *opts[0].Memory
			body.Memory = &m
		}
		if opts[0].RequireHealthyGuest != nil {
			r := *opts[0].RequireHealthyGuest
			body.RequireHealthyGuest = &r
		}
		resp, err = apiCli.PostSandboxesSandboxIDPause(ctx, sandboxID, body)
	} else {
		// Send no body (and no Content-Type) so older servers (< runtime 2026.25)
		// that predate the optional pause payload keep accepting the request.
		resp, err = apiCli.PostSandboxesSandboxIDPauseWithBody(ctx, sandboxID, "", nil,
			func(_ context.Context, req *http.Request) error {
				req.Header.Del("Content-Type")
				return nil
			})
	}
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		drainResponseBody(resp.Body)
		return false, nil
	}
	if err := finishNoContent(resp, sandboxID); err != nil {
		return false, err
	}
	return true, nil
}

func setSandboxTimeout(ctx context.Context, apiCli *apiclient.Client, sandboxID string, d time.Duration) error {
	sec, err := ceilSecondsInt32("timeout", d)
	if err != nil {
		return err
	}
	body := apiclient.PostSandboxesSandboxIDTimeoutJSONRequestBody{Timeout: sec}
	resp, err := apiCli.PostSandboxesSandboxIDTimeout(ctx, sandboxID, body)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return finishNoContent(resp, sandboxID)
}

func refreshSandboxTimeout(ctx context.Context, apiCli *apiclient.Client, sandboxID string, duration time.Duration) error {
	var body apiclient.PostSandboxesSandboxIDRefreshesJSONRequestBody
	if duration != 0 {
		sec, err := ceilSecondsInt32("duration", duration)
		if err != nil {
			return err
		}
		s := int(sec)
		body.Duration = &s
	}
	resp, err := apiCli.PostSandboxesSandboxIDRefreshes(ctx, sandboxID, body)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return finishNoContent(resp, sandboxID)
}

func fetchSandboxInfo(ctx context.Context, apiCli *apiclient.Client, sandboxID string) (*SandboxInfo, error) {
	resp, err := apiCli.GetSandboxesSandboxID(ctx, sandboxID)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSandboxesSandboxIDResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse info response", err)
	}
	d := parsed.JSON200
	if d == nil {
		return nil, newSandboxError("empty info response body", nil)
	}
	return sandboxInfoFromAPI(d), nil
}

func fetchSandboxMetrics(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ...MetricsOptions) ([]SandboxMetric, error) {
	var params *apiclient.GetSandboxesSandboxIDMetricsParams
	if len(opts) > 0 {
		o := opts[0]
		params = &apiclient.GetSandboxesSandboxIDMetricsParams{}
		if !o.Start.IsZero() {
			start := o.Start.Unix()
			params.Start = &start
		}
		if !o.End.IsZero() {
			end := o.End.Unix()
			params.End = &end
		}
	}
	resp, err := apiCli.GetSandboxesSandboxIDMetrics(ctx, sandboxID, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSandboxesSandboxIDMetricsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse metrics response", err)
	}
	if parsed.JSON200 == nil {
		return nil, nil
	}
	out := make([]SandboxMetric, 0, len(*parsed.JSON200))
	for _, m := range *parsed.JSON200 {
		out = append(out, sandboxMetricFromAPI(m))
	}
	return out, nil
}

func sandboxMetricFromAPI(m apiclient.SandboxMetric) SandboxMetric {
	return SandboxMetric{
		CPUCount:      m.CpuCount,
		CPUUsedPct:    m.CpuUsedPct,
		DiskTotal:     m.DiskTotal,
		DiskUsed:      m.DiskUsed,
		MemTotal:      m.MemTotal,
		MemUsed:       m.MemUsed,
		MemCache:      m.MemCache,
		Timestamp:     m.Timestamp,
		TimestampUnix: m.TimestampUnix,
	}
}

func mapHTTPOrCtx(err error) error {
	if e := contextErr(err); e != nil {
		return e
	}
	return newSandboxError("http request failed", err)
}
