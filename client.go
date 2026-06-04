package e2b

import (
	"context"
	"maps"
	"net/http"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// Client is the unified entry point to the E2B control plane. It resolves a
// Config once and constructs a single shared HTTP client and control-plane
// REST client, both reused across every call and by every Sandbox it creates.
//
// Prefer holding one *Client for the lifetime of your program (or per credential
// set) over the package-level Create/Connect/Kill helpers, which build a
// throwaway Client per call and so cannot reuse the REST client across calls.
//
// A *Client is safe for concurrent use.
type Client struct {
	cfg     Config            // already resolved
	apiCli  *apiclient.Client // control-plane REST client, built once
	httpCli *http.Client      // shared HTTP client, built once
}

// NewClient resolves cfg (applying environment-variable fallbacks and
// defaults) and builds the shared HTTP and control-plane REST clients. Every
// Sandbox created through the returned Client reuses them.
//
// A zero Config is valid; credentials and domain fall back to the environment.
func NewClient(cfg Config) (*Client, error) {
	resolved := cfg.resolve()
	hc := resolved.httpClient()
	auth := transport.Auth{APIKey: resolved.APIKey, AccessToken: resolved.AccessToken, Headers: resolved.Headers}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, hc, auth)
	if err != nil {
		return nil, newSandboxError("init api client", err)
	}
	return &Client{cfg: resolved, apiCli: apiCli, httpCli: hc}, nil
}

// Config returns a copy of the resolved Config backing this Client. The map
// fields (Headers, ExtraSandboxHeaders) are deep-copied so mutating the
// returned Config cannot alter this Client's live request headers or race with
// concurrent requests.
func (c *Client) Config() Config {
	out := c.cfg
	if c.cfg.Headers != nil {
		out.Headers = maps.Clone(c.cfg.Headers)
	}
	if c.cfg.ExtraSandboxHeaders != nil {
		out.ExtraSandboxHeaders = maps.Clone(c.cfg.ExtraSandboxHeaders)
	}
	return out
}

// Create provisions a new sandbox and returns a *Sandbox wired up with all
// sub-clients, sharing this Client's HTTP and REST clients. The caller owns
// the returned sandbox and should call Kill (directly or via defer) to release
// it.
//
// opts.Config is ignored: the Client already owns the resolved configuration.
// Use a separate Client (via NewClient) for a different credential set.
func (c *Client) Create(ctx context.Context, opts CreateOptions) (*Sandbox, error) {
	body := buildNewSandbox(opts)
	resp, err := c.apiCli.PostSandboxes(ctx, body)
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
	return c.newSandbox(created)
}

// Connect attaches to an existing sandbox (running or paused). If paused, the
// server resumes it; the call sets the sandbox timeout to opts.Timeout
// (default 5 minutes).
//
// opts.Config is ignored (see Create).
func (c *Client) Connect(ctx context.Context, sandboxID string, opts ConnectOptions) (*Sandbox, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSandboxTimeout
	}
	body := apiclient.ConnectSandbox{Timeout: int32(timeout / time.Second)}
	resp, err := c.apiCli.PostSandboxesSandboxIDConnect(ctx, sandboxID, body)
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
	var connected *apiclient.Sandbox
	if parsed.JSON201 != nil {
		connected = parsed.JSON201
	} else if parsed.JSON200 != nil {
		connected = parsed.JSON200
	}
	if connected == nil {
		return nil, newSandboxError("empty connect response body", nil)
	}
	return c.newSandbox(connected)
}

// Kill terminates a sandbox by ID. Returns false (nil error) if the sandbox
// was already gone.
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
		return false, nil
	}
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return false, err
	}
	return true, nil
}
