package e2b

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// SecretInfo holds metadata about a project secret. Secret values are
// write-only and are never returned by the API.
type SecretInfo struct {
	SecretID       string
	Name           string
	CurrentVersion int64
	Metadata       map[string]string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SecretOptions configures CreateSecret and UpdateSecret.
type SecretOptions struct {
	// Metadata attaches customer metadata to the secret. On UpdateSecret, a
	// non-nil map replaces the stored metadata.
	Metadata map[string]string
}

// SecretListOptions filters and paginates a secret list.
type SecretListOptions struct {
	// Limit caps the number of items per page (0 uses server default).
	Limit int32
	// NextToken resumes from a previous page's cursor.
	NextToken string
}

// SecretPaginator iterates secret list pages, following the server's
// x-next-token response header. Obtain one from Client.ListSecrets.
//
// A SecretPaginator is stateful and not safe for concurrent use.
type SecretPaginator struct {
	c       *Client
	opts    SecretListOptions
	next    string
	hasNext bool
}

// HasNext reports whether another page of secrets is available.
func (p *SecretPaginator) HasNext() bool { return p.hasNext }

// NextToken returns the cursor for the next page of secrets, or "" when exhausted.
func (p *SecretPaginator) NextToken() string { return p.next }

// NextItems fetches the next page of secrets. Once exhausted it returns (nil, nil).
//
// Endpoint: GET /secrets
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (p *SecretPaginator) NextItems(ctx context.Context) ([]SecretInfo, error) {
	if !p.hasNext {
		return nil, nil
	}

	params := &apiclient.GetSecretsParams{}
	if p.opts.Limit > 0 {
		lim := p.opts.Limit
		params.Limit = &lim
	}
	if p.next != "" {
		tok := p.next
		params.NextToken = &tok
	}

	resp, err := p.c.apiCli.GetSecrets(ctx, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSecretsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse secrets list response", err)
	}

	token := ""
	if parsed.HTTPResponse != nil {
		token = parsed.HTTPResponse.Header.Get("x-next-token")
	}
	p.next = token
	p.hasNext = token != ""

	if parsed.JSON200 == nil {
		return nil, nil
	}
	listed := *parsed.JSON200
	out := make([]SecretInfo, 0, len(listed))
	for i := range listed {
		out = append(out, *secretInfoFromAPI(&listed[i]))
	}
	return out, nil
}

// CreateSecret creates a new project secret and stores its initial write-only
// value.
//
// Endpoint: POST /secrets
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) CreateSecret(ctx context.Context, name, value string, opts ...SecretOptions) (*SecretInfo, error) {
	if err := validatePlaceholderName("secret", name); err != nil {
		return nil, err
	}
	body := apiclient.NewSecret{
		Name:  name,
		Value: value,
	}
	if len(opts) > 0 && opts[0].Metadata != nil {
		md := apiclient.SecretMetadata(maps.Clone(opts[0].Metadata))
		body.Metadata = &md
	}
	resp, err := c.apiCli.PostSecrets(ctx, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSecretsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse create secret response", err)
	}
	if parsed.JSON201 == nil {
		return nil, newSandboxError("empty create secret response body", nil)
	}
	return secretInfoFromAPI(parsed.JSON201), nil
}

// UpdateSecret appends a new write-only secret version (and optionally replaces
// stored metadata) for the secret identified by ID or name.
//
// Endpoint: POST /secrets/{secretID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) UpdateSecret(ctx context.Context, secretIDOrName, value string, opts ...SecretOptions) (*SecretInfo, error) {
	if secretIDOrName == "" {
		return nil, &InvalidArgumentError{Message: "secret ID or name must not be empty"}
	}
	body := apiclient.SecretUpdate{Value: value}
	if len(opts) > 0 && opts[0].Metadata != nil {
		md := apiclient.SecretMetadata(maps.Clone(opts[0].Metadata))
		body.Metadata = &md
	}
	resp, err := c.apiCli.PostSecretsSecretID(ctx, secretIDOrName, body)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParsePostSecretsSecretIDResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse update secret response", err)
	}
	if parsed.JSON200 == nil {
		return nil, newSandboxError("empty update secret response body", nil)
	}
	return secretInfoFromAPI(parsed.JSON200), nil
}

// GetSecret fetches metadata for one secret by ID or name.
//
// Endpoint: GET /secrets/{secretID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) GetSecret(ctx context.Context, secretIDOrName string) (*SecretInfo, error) {
	if secretIDOrName == "" {
		return nil, &InvalidArgumentError{Message: "secret ID or name must not be empty"}
	}
	resp, err := c.apiCli.GetSecretsSecretID(ctx, secretIDOrName)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSecretsSecretIDResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse get secret response", err)
	}
	if parsed.JSON200 == nil {
		return nil, newSandboxError("empty get secret response body", nil)
	}
	return secretInfoFromAPI(parsed.JSON200), nil
}

// SecretExists reports whether a secret with the given ID or name exists.
//
// Endpoint: GET /secrets/{secretID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) SecretExists(ctx context.Context, secretIDOrName string) (bool, error) {
	_, err := c.GetSecret(ctx, secretIDOrName)
	if err != nil {
		if _, ok := errors.AsType[*NotFoundError](err); ok {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListSecrets returns a paginator over the project's secrets. No network
// request is issued until the first NextItems call.
//
// Endpoint: GET /secrets
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) ListSecrets(_ context.Context, opts ...SecretListOptions) *SecretPaginator {
	var o SecretListOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return &SecretPaginator{c: c, opts: o, next: o.NextToken, hasNext: true}
}

// ListAllSecrets drains every page and returns all project secrets.
//
// Endpoint: GET /secrets
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) ListAllSecrets(ctx context.Context, opts ...SecretListOptions) ([]SecretInfo, error) {
	p := c.ListSecrets(ctx, opts...)
	var all []SecretInfo
	for p.HasNext() {
		page, err := p.NextItems(ctx)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// DeleteSecret revokes a secret by ID or name. Returns false (nil error) if the
// secret was already absent.
//
// Endpoint: DELETE /secrets/{secretID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.30 (not in runtime < 2026.30).
func (c *Client) DeleteSecret(ctx context.Context, secretIDOrName string) (bool, error) {
	if secretIDOrName == "" {
		return false, &InvalidArgumentError{Message: "secret ID or name must not be empty"}
	}
	if c.cfg.Debug {
		return true, nil
	}
	resp, err := c.apiCli.DeleteSecretsSecretID(ctx, secretIDOrName)
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		drainResponseBody(resp.Body)
		return false, nil
	}
	if err := finishNoContent(resp, ""); err != nil {
		return false, err
	}
	return true, nil
}

// SecretPlaceholder formats a placeholder ("${e2b.secrets.<name>}") that the
// host egress proxy resolves to the secret's current value in NetworkRule
// header transforms without exposing the secret inside the sandbox.
func SecretPlaceholder(name string) string {
	return "${e2b.secrets." + name + "}"
}

// IamTokenPlaceholder formats a placeholder ("${e2b.identity.tokens.<name>}")
// that the host egress proxy replaces with a freshly minted workload identity
// token registered in CreateOptions.Iam.
func IamTokenPlaceholder(name string) string {
	return "${e2b.identity.tokens." + name + "}"
}

func secretInfoFromAPI(s *apiclient.Secret) *SecretInfo {
	md := map[string]string{}
	if s.Metadata != nil {
		md = maps.Clone(map[string]string(s.Metadata))
	}
	return &SecretInfo{
		SecretID:       s.SecretID,
		Name:           s.Name,
		CurrentVersion: s.CurrentVersion,
		Metadata:       md,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
	}
}
