package template

import (
	"net/http"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// Client is a server-side template builder/inspector. Methods on Client
// dispatch to the E2B template HTTP API. Safe for concurrent use.
type Client struct {
	cfg     e2b.Config
	apiCli  *apiclient.Client
	httpCli *http.Client
}

// NewClient constructs a Client from a Config, applying environment-variable
// fallbacks. Fails only when the API URL is malformed.
func NewClient(cfg e2b.Config) (*Client, error) {
	resolved := cfg.Resolve()
	hc := resolved.ResolvedHTTPClient()
	auth := transport.Auth{
		APIKey:      resolved.APIKey,
		AccessToken: resolved.AccessToken,
		Headers:     resolved.Headers,
	}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, hc, auth)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: resolved, apiCli: apiCli, httpCli: hc}, nil
}
