package e2b

import (
	"context"
	"io"
	"net/http"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// Client is the unified entry point to the E2B control plane. It resolves a
// Config once and constructs shared control-plane (with retries and
// RequestTimeout) and data-plane (no retries, no client-wide timeout) HTTP
// clients reused across every call and by every Sandbox it creates.
//
// Prefer holding one *Client for the lifetime of your program (or per credential
// set) over the package-level Create/Connect/Kill helpers, which build a
// throwaway Client per call and so cannot reuse the REST client across calls.
//
// A *Client is safe for concurrent use.
type Client struct {
	cfg     Config
	apiCli  *apiclient.Client
	httpCli *http.Client
	dataCli *http.Client
}

// NewClient resolves cfg (applying environment-variable fallbacks and
// defaults) and builds the shared control-plane and data-plane HTTP clients.
// Every Sandbox created through the returned Client reuses them.
//
// A zero Config is valid; credentials and domain fall back to the environment.
// It returns *InvalidArgumentError for an unusable Proxy or HTTPVersion.
func NewClient(cfg Config) (*Client, error) {
	resolved := cfg.resolve()
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	cpCli := resolved.ControlPlaneHTTPClient()
	dpCli := resolved.DataPlaneHTTPClient()
	auth := transport.Auth{APIKey: resolved.APIKey, AccessToken: resolved.AccessToken, Headers: resolved.Headers}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, cpCli, auth)
	if err != nil {
		return nil, newSandboxError("init api client", err)
	}
	return &Client{cfg: resolved, apiCli: apiCli, httpCli: cpCli, dataCli: dpCli}, nil
}

// Config returns a copy of the resolved Config backing this Client. The map
// fields (Headers, ExtraSandboxHeaders) and Retries pointer are deep-copied so
// mutating the returned Config cannot alter this Client's live request headers
// or race with concurrent requests.
func (c *Client) Config() Config {
	return c.cfg.Clone()
}

// HTTPClient returns the shared control-plane *http.Client backing this Client
// (configured with retries and RequestTimeout).
func (c *Client) HTTPClient() *http.Client {
	return c.httpCli
}

// Health probes the control-plane API readiness endpoint (GET /health),
// returning nil on any 2xx status or an Error when the service is unhealthy.
//
// Endpoint: GET /health
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) Health(ctx context.Context) error {
	resp, err := c.apiCli.GetHealth(ctx)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return err
	}
	drainResponseBody(resp.Body)
	return nil
}

// Create provisions a new sandbox via POST /v2/sandboxes and returns a *Sandbox
// wired up with all sub-clients, sharing this Client's HTTP and REST clients.
// It does not fall back to POST /sandboxes on self-hosted servers; use CreateV1
// on self-hosted deployments (runtime <= 2026.30).
//
// opts.Config is ignored: the Client already owns the resolved configuration.
//
// Endpoint: POST /v2/sandboxes
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30; use CreateV1).
// Envd: >= v0.1.0
func (c *Client) Create(ctx context.Context, opts ...CreateOptions) (*Sandbox, error) {
	var o CreateOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	params, body, err := buildNewSandboxV2(o)
	if err != nil {
		return nil, err
	}
	resp, err := c.apiCli.PostV2Sandboxes(ctx, params, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostV2SandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse create response", err)
	}
	created, err := parseCreatedSandboxResponse(parsed.JSON201, parsed.Body, resp.StatusCode)
	if err != nil {
		return nil, err
	}
	return c.finalizeCreatedSandbox(ctx, created, o)
}

// CreateV1 provisions a new sandbox via POST /sandboxes. By default it sends
// secure=true so self-hosted servers (< 2026.30) mint an EnvdAccessToken; set
// opts.Insecure = true only for legacy templates (envd < 0.2.0) that predate
// secured envd access.
//
// Although marked deprecated in newer OpenAPI specs in favor of Create
// (POST /v2/sandboxes), POST /sandboxes is soft-deprecated (not retired) and
// remains actively served on both E2B Cloud and all self-hosted runtime <= 2026.30
// deployments. Choose CreateV1 on all self-hosted runtime <= 2026.30 clusters
// (where /v2/sandboxes does not exist); prefer Create on E2B Cloud.
//
// opts.Config and opts.IdempotencyKey are ignored on v1.
//
// Endpoint: POST /sandboxes
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; primary create endpoint on self-hosted, soft-deprecated in OpenAPI in favor of Create on E2B Cloud).
// Envd: >= v0.2.0 with default secure=true (>= v0.1.0 when Insecure is true)
func (c *Client) CreateV1(ctx context.Context, opts ...CreateOptions) (*Sandbox, error) {
	var o CreateOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	body, err := buildNewSandboxV1(o)
	if err != nil {
		return nil, err
	}
	resp, err := c.apiCli.PostSandboxes(ctx, body) //nolint:staticcheck // v1 POST /sandboxes is required on all self-hosted runtime <= 2026.30 deployments.
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse v1 create response", err)
	}
	created, err := parseCreatedSandboxResponse(parsed.JSON201, parsed.Body, resp.StatusCode)
	if err != nil {
		return nil, err
	}
	return c.finalizeCreatedSandbox(ctx, created, o)
}

// Connect attaches to an existing sandbox (running or paused) via
// POST /v2/sandboxes/{sandboxID}/connect. If paused, the server resumes it.
// When opts.Timeout <= 0, timeout is omitted so the server default applies. It
// does not fall back to ConnectV1 or Resume on self-hosted servers.
//
// opts.Config is ignored (see Create).
//
// Endpoint: POST /v2/sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30; use ConnectV1 or Resume).
// Envd: >= v0.1.0
func (c *Client) Connect(ctx context.Context, sandboxID string, opts ...ConnectOptions) (*Sandbox, error) {
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	connected, err := connectSandboxV2(ctx, c.apiCli, sandboxID, o)
	if err != nil {
		return nil, err
	}
	return c.newSandbox(connected)
}

// ConnectV1 attaches to an existing sandbox (running or paused) via
// POST /sandboxes/{sandboxID}/connect, resuming it if paused. If opts.Timeout <= 0,
// DefaultSandboxTimeout (5 minutes) is sent.
//
// Marked soft-deprecated in newer OpenAPI specs in favor of Connect
// (POST /v2/sandboxes/{sandboxID}/connect), this endpoint remains actively
// served (not retired) on both E2B Cloud and self-hosted runtime 2025.44..2026.30.
// Choose ConnectV1 as the primary idempotent connect/wake endpoint on
// self-hosted runtime 2025.44..2026.30; prefer Connect on E2B Cloud, and use
// Resume on self-hosted runtime < 2025.44 (where /connect does not exist).
//
// opts.Config is ignored (see Create).
//
// Endpoint: POST /sandboxes/{sandboxID}/connect
// Compatibility: E2B Cloud and self-hosted runtime 2025.44..2026.30 (actively served; primary connect/wake endpoint on self-hosted 2025.44..2026.30; use Resume on < 2025.44).
// Envd: >= v0.1.0
func (c *Client) ConnectV1(ctx context.Context, sandboxID string, opts ...ConnectOptions) (*Sandbox, error) {
	var o ConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	connected, err := connectSandboxV1(ctx, c.apiCli, sandboxID, o)
	if err != nil {
		return nil, err
	}
	return c.newSandbox(connected)
}

// Resume wakes a paused sandbox via POST /sandboxes/{sandboxID}/resume.
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
// opts.Config is ignored (see Create).
//
// Endpoint: POST /sandboxes/{sandboxID}/resume
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.44; soft-deprecated in OpenAPI in favor of Connect / ConnectV1).
// Envd: >= v0.1.0
func (c *Client) Resume(ctx context.Context, sandboxID string, opts ...ResumeOptions) (*Sandbox, error) {
	var o ResumeOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	resumed, err := resumeSandbox(ctx, c.apiCli, sandboxID, o)
	if err != nil {
		return nil, err
	}
	return c.newSandbox(resumed)
}

// ResumeV1 is a version-suffixed alias for Resume (POST /sandboxes/{sandboxID}/resume).
// See Resume for availability across E2B Cloud and self-hosted runtime <= 2026.30,
// why OpenAPI soft-deprecated it in favor of Connect / ConnectV1, and when to choose it.
//
// Endpoint: POST /sandboxes/{sandboxID}/resume
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.44; soft-deprecated in OpenAPI in favor of Connect / ConnectV1).
// Envd: >= v0.1.0
func (c *Client) ResumeV1(ctx context.Context, sandboxID string, opts ResumeOptions) (*Sandbox, error) {
	return c.Resume(ctx, sandboxID, opts)
}

// Pause pauses a sandbox by ID. Returns true on success, or false (nil error)
// if the sandbox was already paused (HTTP 409). When opts is omitted or zero,
// no JSON body is sent so older servers (< runtime 2026.25) accept the call.
//
// Endpoint: POST /sandboxes/{sandboxID}/pause
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (PauseOptions body requires E2B Cloud or runtime >= 2026.25).
// Envd: >= v0.5.0 when PauseOptions.Memory is false
func (c *Client) Pause(ctx context.Context, sandboxID string, opts ...PauseOptions) (bool, error) {
	return pauseSandbox(ctx, c.apiCli, sandboxID, opts...)
}

// SetTimeout updates a sandbox's timeout by ID, overwriting its TTL relative to
// the current server time.
//
// Endpoint: POST /sandboxes/{sandboxID}/timeout
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error {
	if c.cfg.Debug {
		return nil
	}
	return setSandboxTimeout(ctx, c.apiCli, sandboxID, timeout)
}

// RefreshTimeout extends a sandbox's TTL monotonically via
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
func (c *Client) RefreshTimeout(ctx context.Context, sandboxID string, duration time.Duration) error {
	if c.cfg.Debug {
		return nil
	}
	return refreshSandboxTimeout(ctx, c.apiCli, sandboxID, duration)
}

// RefreshTimeoutV1 is a version-suffixed alias for RefreshTimeout
// (POST /sandboxes/{sandboxID}/refreshes). See RefreshTimeout for details.
//
// Endpoint: POST /sandboxes/{sandboxID}/refreshes
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (actively served).
func (c *Client) RefreshTimeoutV1(ctx context.Context, sandboxID string, duration time.Duration) error {
	return c.RefreshTimeout(ctx, sandboxID, duration)
}

// GetInfo fetches control-plane metadata for a sandbox by ID. Legacy servers
// (< 2025.31) that return 200 OK with an empty state field are normalized to
// SandboxStateRunning.
//
// Endpoint: GET /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) GetInfo(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	return fetchSandboxInfo(ctx, c.apiCli, sandboxID)
}

// IsRunning reports whether the sandbox identified by sandboxID is currently in
// the running state according to the control plane (GET /sandboxes/{sandboxID}).
// Returns (false, nil) if the sandbox is paused or no longer exists.
//
// Endpoint: GET /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) IsRunning(ctx context.Context, sandboxID string) (bool, error) {
	return isSandboxRunningViaControlPlane(ctx, c.apiCli, sandboxID)
}

// GetMetrics fetches CPU, memory (including MemCache), and disk usage samples
// for a sandbox by ID.
//
// Endpoint: GET /sandboxes/{sandboxID}/metrics
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30 (MemCache requires E2B Cloud or runtime >= 2026.14).
// Envd: >= v0.1.5
func (c *Client) GetMetrics(ctx context.Context, sandboxID string, opts ...MetricsOptions) ([]SandboxMetric, error) {
	if c.cfg.Debug {
		return nil, nil
	}
	return fetchSandboxMetrics(ctx, c.apiCli, sandboxID, opts...)
}

// GetBatchMetrics fetches the latest metric sample for multiple sandboxes in a
// single control-plane call, keyed by sandbox ID.
//
// Endpoint: GET /sandboxes/metrics
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
// Envd: >= v0.1.5
func (c *Client) GetBatchMetrics(ctx context.Context, sandboxIDs []string) (map[string]SandboxMetric, error) {
	if c.cfg.Debug || len(sandboxIDs) == 0 {
		return map[string]SandboxMetric{}, nil
	}
	params := &apiclient.GetSandboxesMetricsParams{SandboxIds: sandboxIDs}
	resp, err := c.apiCli.GetSandboxesMetrics(ctx, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSandboxesMetricsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse batch metrics response", err)
	}
	out := make(map[string]SandboxMetric)
	if parsed.JSON200 == nil {
		return out, nil
	}
	for id, m := range parsed.JSON200.Sandboxes {
		out[id] = sandboxMetricFromAPI(m)
	}
	return out, nil
}

// Kill terminates a sandbox by ID. Returns false (nil error) if the sandbox
// was already gone.
//
// Endpoint: DELETE /sandboxes/{sandboxID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) Kill(ctx context.Context, sandboxID string) (bool, error) {
	if c.cfg.Debug {
		return true, nil
	}
	resp, err := c.apiCli.DeleteSandboxesSandboxID(ctx, sandboxID)
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		drainResponseBody(resp.Body)
		return false, nil
	}
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return false, err
	}
	return true, nil
}

func drainResponseBody(body io.Reader) {
	if body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	}
}
