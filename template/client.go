package template

import (
	"io"
	"net/http"
	"sync/atomic"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

type fallbackOp uint32

const (
	fallbackOpList fallbackOp = 1 << iota
	fallbackOpGet
	fallbackOpSetPublicV1
	fallbackOpDelete
	fallbackOpUploadLink
	fallbackOpBuildStatus
)

const maxFallbackDrainBytes = 64 << 10

// Client is a server-side template builder and management client. Methods on
// Client dispatch to the E2B template HTTP API. Safe for concurrent use.
type Client struct {
	cfg               e2b.Config
	apiCli            *apiclient.Client
	tokenAPICli       *apiclient.Client
	uploadCli         *http.Client
	tokenFallbackMask atomic.Uint32
}

// NewClient constructs a Client from cfg, applying environment-variable
// fallbacks via cfg.Resolve(), wiring ControlPlaneHTTPClient() (with retries
// and RequestTimeout) for control-plane REST calls, and wiring a dedicated
// DataPlaneHTTPClient() (no client-wide timeout or control-plane retry wrapper)
// for presigned layer uploads.
//
// When both APIKey and AccessToken are present on the resolved Config,
// NewClient keeps X-API-Key on the primary REST client and also prepares a
// Bearer-only secondary client (never sending both headers on the same
// request) so legacy self-hosted E2B deployments that require AccessTokenAuth
// on GET /templates, PATCH /templates/{id}, and DELETE /templates/{id} work
// seamlessly alongside X-API-Key template builds on a single Client.
func NewClient(cfg e2b.Config) (*Client, error) {
	resolved := cfg.Resolve()
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	ctrlHTTP := resolved.ControlPlaneHTTPClient()
	auth := transport.Auth{
		APIKey:      resolved.APIKey,
		AccessToken: resolved.AccessToken,
		Headers:     resolved.Headers,
	}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, ctrlHTTP, auth)
	if err != nil {
		return nil, &e2b.SandboxError{Message: "init template api client", Cause: err}
	}

	var tokenAPICli *apiclient.Client
	if resolved.APIKey != "" && resolved.AccessToken != "" {
		tokenAuth := transport.Auth{
			AccessToken: resolved.AccessToken,
			Headers:     resolved.Headers,
		}
		tokenAPICli, err = transport.NewAPIClient(resolved.APIURL, ctrlHTTP, tokenAuth)
		if err != nil {
			return nil, &e2b.SandboxError{Message: "init template token api client", Cause: err}
		}
	}

	return &Client{
		cfg:         resolved,
		apiCli:      apiCli,
		tokenAPICli: tokenAPICli,
		uploadCli:   resolved.DataPlaneHTTPClient(),
	}, nil
}

// doWithTokenFallback executes fn against c.apiCli (X-API-Key when APIKey is
// set). If both APIKey and AccessToken were configured and an older
// self-hosted server rejects X-API-Key with 401 Unauthorized on an
// AccessToken-scoped template endpoint, the 401 body is drained (up to 64 KiB)
// and closed, the endpoint is retried once with c.tokenAPICli (Bearer only),
// and confirmed per-route switches are cached in c.tokenFallbackMask.
func (c *Client) doWithTokenFallback(op fallbackOp, fn func(*apiclient.Client) (*http.Response, error)) (*http.Response, error) {
	if c.tokenAPICli != nil && c.tokenFallbackMask.Load()&uint32(op) != 0 {
		return fn(c.tokenAPICli)
	}
	resp, err := fn(c.apiCli)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || c.tokenAPICli == nil {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxFallbackDrainBytes))
	_ = resp.Body.Close()
	retryResp, retryErr := fn(c.tokenAPICli)
	if retryErr == nil && retryResp.StatusCode != http.StatusUnauthorized {
		c.tokenFallbackMask.Or(uint32(op))
	}
	return retryResp, retryErr
}

// Config returns a copy of the resolved Config backing this Client. Map and
// pointer fields are deep-copied so mutating the returned Config cannot race
// with concurrent requests.
func (c *Client) Config() e2b.Config {
	return c.cfg.Clone()
}
