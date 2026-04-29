package e2b

import (
	"context"
	"net/http"
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
// It exposes three per-sandbox modules:
//   - Commands: exec, PTY, signal
//   - Files: read/write/watch
//   - Git: git operations running inside the sandbox
//
// A *Sandbox is safe for concurrent use.
type Sandbox struct {
	ID                 string
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
}

// CreateOptions captures every parameter accepted by e2b.Create.
// Mirrors the Python Sandbox.create(...) signature. All fields are optional
// except — implicitly — an API key or access token reachable via Config/env.
type CreateOptions struct {
	Config Config

	// Template ID or alias to build the sandbox from. Defaults to "base".
	Template string
	// Timeout is the sandbox lifetime. Defaults to 5 minutes.
	Timeout time.Duration

	// Metadata attaches free-form key/value pairs.
	Metadata map[string]string
	// Envs sets environment variables inside the sandbox.
	Envs map[string]string

	// Secure enables authenticated envd access (recommended).
	Secure bool
	// AllowInternetAccess enables outbound internet. Defaults to true.
	// Use AllowInternetAccessSet=true to send a false value.
	AllowInternetAccess    bool
	AllowInternetAccessSet bool

	// Network, Lifecycle, and VolumeMounts pass through to the API.
	Network      *NetworkOptions
	Lifecycle    *LifecycleOptions
	VolumeMounts []VolumeMount

	// Mcp is an opaque MCP configuration object (see upstream docs).
	Mcp map[string]any
}

// ConnectOptions configures (Re)connecting to an existing sandbox.
type ConnectOptions struct {
	Config  Config
	Timeout time.Duration
}

// Create provisions a new sandbox and returns a *Sandbox wired up with all
// sub-clients. The caller owns the returned sandbox and should call Kill
// (directly or via defer) to release it.
func Create(ctx context.Context, opts CreateOptions) (*Sandbox, error) {
	cfg := opts.Config.resolve()
	hc := cfg.httpClient()
	auth := transport.Auth{APIKey: cfg.APIKey, AccessToken: cfg.AccessToken, Headers: cfg.Headers}
	apiCli, err := transport.NewAPIClient(cfg.APIURL, hc, auth)
	if err != nil {
		return nil, newSandboxError("init api client", err)
	}

	body := buildNewSandbox(opts)
	resp, err := apiCli.PostSandboxes(ctx, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse create response", err)
	}
	created := parsed.JSON201
	if created == nil {
		return nil, newSandboxError("empty create response body", nil)
	}
	return newSandbox(cfg, apiCli, hc, created)
}

// Connect attaches to an existing sandbox (running or paused). If paused, the
// server resumes it; the call sets the sandbox timeout to opts.Timeout
// (default 5 minutes).
func Connect(ctx context.Context, sandboxID string, opts ConnectOptions) (*Sandbox, error) {
	cfg := opts.Config.resolve()
	hc := cfg.httpClient()
	auth := transport.Auth{APIKey: cfg.APIKey, AccessToken: cfg.AccessToken, Headers: cfg.Headers}
	apiCli, err := transport.NewAPIClient(cfg.APIURL, hc, auth)
	if err != nil {
		return nil, newSandboxError("init api client", err)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSandboxTimeout
	}
	body := apiclient.ConnectSandbox{Timeout: int32(timeout / time.Second)}
	resp, err := apiCli.PostSandboxesSandboxIDConnect(ctx, sandboxID, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesSandboxIDConnectResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse connect response", err)
	}
	if parsed.JSON201 == nil {
		return nil, newSandboxError("empty connect response body", nil)
	}
	return newSandbox(cfg, apiCli, hc, parsed.JSON201)
}

// Kill terminates a sandbox by ID. Returns false (nil error) if the sandbox
// was already gone.
func Kill(ctx context.Context, sandboxID string, opts ConnectOptions) (bool, error) {
	cfg := opts.Config.resolve()
	hc := cfg.httpClient()
	if cfg.Debug {
		return true, nil
	}
	auth := transport.Auth{APIKey: cfg.APIKey, AccessToken: cfg.AccessToken, Headers: cfg.Headers}
	apiCli, err := transport.NewAPIClient(cfg.APIURL, hc, auth)
	if err != nil {
		return false, newSandboxError("init api client", err)
	}
	resp, err := apiCli.DeleteSandboxesSandboxID(ctx, sandboxID)
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return false, err
	}
	return true, nil
}

// Kill terminates this sandbox.
func (s *Sandbox) Kill(ctx context.Context) error {
	if s.cfg.Debug {
		return nil
	}
	resp, err := s.apiCli.DeleteSandboxesSandboxID(ctx, s.ID)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return mapHTTPErr(resp, s.ID)
}

// SetTimeout updates the sandbox's inactivity timeout.
func (s *Sandbox) SetTimeout(ctx context.Context, d time.Duration) error {
	if s.cfg.Debug {
		return nil
	}
	body := apiclient.PostSandboxesSandboxIDTimeoutJSONRequestBody{Timeout: int32(d / time.Second)}
	resp, err := s.apiCli.PostSandboxesSandboxIDTimeout(ctx, s.ID, body)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	return mapHTTPErr(resp, s.ID)
}

// IsRunning reports whether the sandbox is still alive (REST GET /sandboxes/{id}).
func (s *Sandbox) IsRunning(ctx context.Context) (bool, error) {
	info, err := s.GetInfo(ctx)
	if err != nil {
		if _, ok := err.(*SandboxNotFoundError); ok {
			return false, nil
		}
		return false, err
	}
	return info.State == SandboxStateRunning, nil
}

// Pause pauses this sandbox. Returns true on success; false if the sandbox
// was already paused.
func (s *Sandbox) Pause(ctx context.Context) (bool, error) {
	resp, err := s.apiCli.PostSandboxesSandboxIDPause(ctx, s.ID)
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return false, nil
	}
	return true, mapHTTPErr(resp, s.ID)
}

// CreateSnapshot persists the sandbox's state as a snapshot.
func (s *Sandbox) CreateSnapshot(ctx context.Context) (*SnapshotInfo, error) {
	body := apiclient.PostSandboxesSandboxIDSnapshotsJSONRequestBody{}
	resp, err := s.apiCli.PostSandboxesSandboxIDSnapshots(ctx, s.ID, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, s.ID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSandboxesSandboxIDSnapshotsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse snapshot response", err)
	}
	if parsed.JSON201 == nil {
		return nil, newSandboxError("empty snapshot response body", nil)
	}
	return &SnapshotInfo{
		SnapshotID: parsed.JSON201.SnapshotID,
		Names:      parsed.JSON201.Names,
	}, nil
}

// GetInfo fetches the sandbox metadata.
func (s *Sandbox) GetInfo(ctx context.Context) (*SandboxInfo, error) {
	resp, err := s.apiCli.GetSandboxesSandboxID(ctx, s.ID)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, s.ID); err != nil {
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

// GetMetrics fetches instantaneous CPU/memory/disk usage.
func (s *Sandbox) GetMetrics(ctx context.Context) ([]SandboxMetric, error) {
	if s.cfg.Debug {
		return nil, nil
	}
	resp, err := s.apiCli.GetSandboxesSandboxIDMetrics(ctx, s.ID, nil)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, s.ID); err != nil {
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
		out = append(out, SandboxMetric{
			CPUCount:      m.CpuCount,
			CPUUsedPct:    m.CpuUsedPct,
			DiskTotal:     m.DiskTotal,
			DiskUsed:      m.DiskUsed,
			MemTotal:      m.MemTotal,
			MemUsed:       m.MemUsed,
			Timestamp:     m.Timestamp,
			TimestampUnix: m.TimestampUnix,
		})
	}
	return out, nil
}

// GetHost returns the external hostname for the given sandbox port.
func (s *Sandbox) GetHost(port int) string {
	return s.cfg.sandboxHost(s.ID, s.Domain, port)
}

// UploadURL returns an HTTP URL the caller can POST a file to.
func (s *Sandbox) UploadURL(path string, opts SignatureOptions) (string, error) {
	return s.buildFileURL(path, SignatureWrite, opts, true)
}

// DownloadURL returns an HTTP URL the caller can GET a file from.
func (s *Sandbox) DownloadURL(path string, opts SignatureOptions) (string, error) {
	return s.buildFileURL(path, SignatureRead, opts, false)
}

// newSandbox builds the per-sandbox sub-clients from a freshly parsed
// REST response.
func newSandbox(cfg Config, apiCli *apiclient.Client, hc *http.Client, created *apiclient.Sandbox) (*Sandbox, error) {
	sbx := &Sandbox{
		ID:          created.SandboxID,
		EnvdVersion: created.EnvdVersion,
		cfg:         cfg,
		apiCli:      apiCli,
		httpCli:     hc,
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
		Token:   sbx.EnvdAccessToken,
		User:    defaultUser,
		Headers: mergeHeaders(cfg.Headers, cfg.ExtraSandboxHeaders),
	}
	envd, err := transport.NewEnvdClients(envdBase, hc, envdAuth)
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

// buildNewSandbox translates CreateOptions into the REST body.
func buildNewSandbox(opts CreateOptions) apiclient.PostSandboxesJSONRequestBody {
	template := opts.Template
	if template == "" {
		template = DefaultTemplate
	}
	timeoutSec := int32(DefaultSandboxTimeout / time.Second)
	if opts.Timeout > 0 {
		timeoutSec = int32(opts.Timeout / time.Second)
	}
	body := apiclient.PostSandboxesJSONRequestBody{
		TemplateID: template,
		Timeout:    &timeoutSec,
		Secure:     ptrBool(opts.Secure),
	}
	if opts.AllowInternetAccessSet {
		body.AllowInternetAccess = &opts.AllowInternetAccess
	}
	if len(opts.Metadata) > 0 {
		md := apiclient.SandboxMetadata(opts.Metadata)
		body.Metadata = &md
	}
	if len(opts.Envs) > 0 {
		ev := apiclient.EnvVars(opts.Envs)
		body.EnvVars = &ev
	}
	if opts.Mcp != nil {
		mcp := apiclient.Mcp(opts.Mcp)
		body.Mcp = &mcp
	}
	if opts.Network != nil {
		body.Network = &apiclient.SandboxNetworkConfig{
			AllowOut:           strSlicePtr(opts.Network.AllowOut),
			DenyOut:            strSlicePtr(opts.Network.DenyOut),
			AllowPublicTraffic: ptrBool(opts.Network.AllowPublicTraffic),
			MaskRequestHost:    strPtr(opts.Network.MaskRequestHost),
		}
	}
	if opts.Lifecycle != nil {
		switch opts.Lifecycle.OnTimeout {
		case "pause":
			body.AutoPause = ptrBool(true)
		case "kill":
			body.AutoPause = ptrBool(false)
		}
		if opts.Lifecycle.AutoResume {
			body.AutoResume = &apiclient.SandboxAutoResumeConfig{Enabled: true}
		}
	}
	if len(opts.VolumeMounts) > 0 {
		vs := make([]apiclient.SandboxVolumeMount, 0, len(opts.VolumeMounts))
		for _, m := range opts.VolumeMounts {
			vs = append(vs, apiclient.SandboxVolumeMount{Name: m.Name, Path: m.Path})
		}
		body.VolumeMounts = &vs
	}
	return body
}

// mapHTTPOrCtx converts low-level transport errors (context cancellation,
// network failures) into SDK errors.
func mapHTTPOrCtx(err error) error {
	if e := contextErr(err); e != nil {
		return e
	}
	return newSandboxError("http request failed", err)
}

// buildFileURL constructs the envd /files URL for upload/download, optionally
// appending a v1 signature. upload is true when the URL is for POST.
func (s *Sandbox) buildFileURL(path string, op SignatureOperation, opts SignatureOptions, _ bool) (string, error) {
	base := s.cfg.sandboxURL(s.ID, s.Domain)
	u := base + "/files?path=" + urlEscape(path)
	user := opts.User
	if user == "" {
		user = defaultUser
	}
	u += "&username=" + urlEscape(user)
	if s.EnvdAccessToken != "" {
		sig, err := GetSignature(path, op, s.EnvdAccessToken, SignatureOptions{User: opts.User, ExpirationInSeconds: opts.ExpirationInSeconds})
		if err != nil {
			return "", err
		}
		u += "&signature=" + urlEscape(sig.Value)
		if sig.Expiration != nil {
			u += "&signature_expiration=" + int64ToStr(*sig.Expiration)
		}
	}
	return u, nil
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func ptrBool(b bool) *bool { return &b }
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func strSlicePtr(s []string) *[]string {
	if len(s) == 0 {
		return nil
	}
	return &s
}
func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
