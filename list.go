package e2b

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// Sort order constants for SandboxListOptions.Order.
const (
	ListOrderAsc  = "asc"
	ListOrderDesc = "desc"
)

// SandboxListOptions filters and paginates a sandbox list (GET /v2/sandboxes).
// The zero value lists all sandboxes the caller can see, one server-defined
// page at a time.
type SandboxListOptions struct {
	// Metadata filters by exact key/value pairs (all must match).
	Metadata map[string]string
	// State filters by one or more states. Empty means all states.
	State []SandboxState
	// StartedAfter filters to sandboxes started at or after this timestamp
	// (supported on E2B Cloud and self-hosted runtime >= 2026.30).
	StartedAfter *time.Time
	// Template filters by template ID or alias (supported on E2B Cloud and
	// self-hosted runtime >= 2026.30).
	Template string
	// Order sorts results by sandbox start time ("asc" or "desc"; default "desc",
	// supported on E2B Cloud and self-hosted runtime >= 2026.30).
	Order string
	// Limit caps the number of items per page. 0 uses the server default.
	Limit int32
	// NextToken resumes from a previous page's cursor. Usually left empty;
	// the paginator manages it internally.
	NextToken string
}

// SandboxListV1Options filters legacy unpaginated running sandbox listings
// (GET /sandboxes).
type SandboxListV1Options struct {
	// Metadata filters running sandboxes by exact key/value pairs.
	Metadata map[string]string
}

// SandboxPaginator iterates sandbox list pages, following the server's
// cursor (the x-next-token response header). Obtain one from Client.List.
//
// A SandboxPaginator is stateful and not safe for concurrent use.
type SandboxPaginator struct {
	c            *Client
	opts         SandboxListOptions
	next         string
	hasNext      bool
	totalRunning *int32
}

// HasNext reports whether another page is available. It is true before the
// first NextItems call; afterwards it reflects the x-next-token header of the
// most recent response.
func (p *SandboxPaginator) HasNext() bool { return p.hasNext }

// NextToken returns the cursor for the next page, or "" when exhausted. It can
// be persisted and replayed via SandboxListOptions.NextToken.
func (p *SandboxPaginator) NextToken() string { return p.next }

// TotalRunning returns the X-Total-Running header reported on the most recent
// NextItems response when provided by the server, or nil when omitted.
func (p *SandboxPaginator) TotalRunning() *int32 {
	if p.totalRunning == nil {
		return nil
	}
	v := *p.totalRunning
	return &v
}

// NextItems fetches the next page of sandboxes. Call it only while HasNext
// reports true; once exhausted it returns (nil, nil). After the call HasNext
// reflects whether further pages remain.
//
// Endpoint: GET /v2/sandboxes
// Compatibility: E2B Cloud; self-hosted runtime 2025.31..2026.30 (use ListV1 on < 2025.31).
func (p *SandboxPaginator) NextItems(ctx context.Context) ([]SandboxInfo, error) {
	if !p.hasNext {
		return nil, nil
	}
	if p.opts.Order != "" && p.opts.Order != ListOrderAsc && p.opts.Order != ListOrderDesc {
		return nil, &InvalidArgumentError{
			Message: fmt.Sprintf("list order must be %q or %q, got %q", ListOrderAsc, ListOrderDesc, p.opts.Order),
		}
	}

	params := &apiclient.GetV2SandboxesParams{}
	if md := encodeMetadataQuery(p.opts.Metadata); md != "" {
		params.Metadata = &md
	}
	if len(p.opts.State) > 0 {
		states := make([]apiclient.SandboxState, 0, len(p.opts.State))
		for _, s := range p.opts.State {
			states = append(states, apiclient.SandboxState(s))
		}
		params.State = &states
	}
	if p.opts.StartedAfter != nil && !p.opts.StartedAfter.IsZero() {
		ts := p.opts.StartedAfter.UTC()
		params.StartedAfter = &ts
	}
	params.Template = nilIfEmpty(p.opts.Template)
	if p.opts.Order != "" {
		ord := apiclient.OrderDirection(p.opts.Order)
		params.Order = &ord
	}
	if p.opts.Limit > 0 {
		lim := apiclient.PaginationLimit(p.opts.Limit)
		params.Limit = &lim
	}
	if p.next != "" {
		tok := apiclient.PaginationNextToken(p.next)
		params.NextToken = &tok
	}

	resp, err := p.c.apiCli.GetV2Sandboxes(ctx, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetV2SandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse list response", err)
	}

	token := ""
	if parsed.HTTPResponse != nil {
		token = parsed.HTTPResponse.Header.Get("x-next-token")
	}
	p.next = token
	p.hasNext = token != ""
	if parsed.Headers200 != nil && parsed.Headers200.XTotalRunning != nil {
		v := *parsed.Headers200.XTotalRunning
		p.totalRunning = &v
	} else {
		p.totalRunning = nil
	}

	if parsed.JSON200 == nil {
		return nil, nil
	}
	listed := *parsed.JSON200
	out := make([]SandboxInfo, 0, len(listed))
	for i := range listed {
		out = append(out, *sandboxInfoFromListed(&listed[i]))
	}
	return out, nil
}

// List returns a paginator over the sandboxes visible to this Client. The
// request is not issued until the first NextItems call. It does not fall back
// to ListV1 on legacy servers.
//
// Endpoint: GET /v2/sandboxes
// Compatibility: E2B Cloud; self-hosted runtime 2025.31..2026.30 (use ListV1 on < 2025.31).
func (c *Client) List(_ context.Context, opts ...SandboxListOptions) *SandboxPaginator {
	var o SandboxListOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return &SandboxPaginator{c: c, opts: o, next: o.NextToken, hasNext: true}
}

// ListAll drains every page and returns all matching sandboxes. Prefer List
// for large result sets where streaming pages avoids buffering everything.
//
// Endpoint: GET /v2/sandboxes
// Compatibility: E2B Cloud; self-hosted runtime 2025.31..2026.30 (use ListV1 on < 2025.31).
func (c *Client) ListAll(ctx context.Context, opts ...SandboxListOptions) ([]SandboxInfo, error) {
	p := c.List(ctx, opts...)
	var all []SandboxInfo
	for p.HasNext() {
		page, err := p.NextItems(ctx)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// ListV1 lists running sandboxes via the unpaginated GET /sandboxes endpoint.
//
// Soft-deprecated in OpenAPI in favor of cursor-paginated List / ListAll
// (GET /v2/sandboxes, added in runtime 2025.31 with paused-sandbox visibility),
// GET /sandboxes remains actively served (not retired) on both E2B Cloud and
// all self-hosted runtime <= 2026.30 deployments (returning HTTP 400 only when
// more than 5000 running sandboxes match). Choose ListV1 on self-hosted
// runtime < 2025.31 (where /v2/sandboxes does not exist); prefer List / ListAll
// on E2B Cloud and runtime >= 2025.31.
//
// Endpoint: GET /sandboxes
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.31; soft-deprecated in OpenAPI in favor of List / ListAll).
func (c *Client) ListV1(ctx context.Context, opts ...SandboxListV1Options) ([]SandboxInfo, error) {
	var params *apiclient.GetSandboxesParams
	if len(opts) > 0 {
		if md := encodeMetadataQuery(opts[0].Metadata); md != "" {
			params = &apiclient.GetSandboxesParams{Metadata: &md}
		}
	}

	resp, err := c.apiCli.GetSandboxes(ctx, params) //nolint:staticcheck // v1 GET /sandboxes is preserved for legacy self-hosted < 2025.31 deployments.
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse v1 list response", err)
	}
	if parsed.JSON200 == nil {
		return nil, nil
	}
	listed := *parsed.JSON200
	out := make([]SandboxInfo, 0, len(listed))
	for i := range listed {
		out = append(out, *sandboxInfoFromListed(&listed[i]))
	}
	return out, nil
}

// List returns a paginator over sandboxes using a one-shot Client built from
// cfg.
//
// Deprecated: prefer NewClient(cfg).List, which reuses one control-plane REST
// client across calls.
//
// Endpoint: GET /v2/sandboxes
// Compatibility: E2B Cloud; self-hosted runtime 2025.31..2026.30 (use ListV1 on < 2025.31).
func List(ctx context.Context, cfg Config, opts ...SandboxListOptions) (*SandboxPaginator, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.List(ctx, opts...), nil
}

// ListAll drains every page using a one-shot Client built from cfg.
//
// Deprecated: prefer NewClient(cfg).ListAll (see List).
//
// Endpoint: GET /v2/sandboxes
// Compatibility: E2B Cloud; self-hosted runtime 2025.31..2026.30 (use ListV1 on < 2025.31).
func ListAll(ctx context.Context, cfg Config, opts ...SandboxListOptions) ([]SandboxInfo, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.ListAll(ctx, opts...)
}

// ListV1 lists running sandboxes via the unpaginated GET /sandboxes endpoint
// using a one-shot Client built from cfg. Soft-deprecated in OpenAPI in favor
// of List / ListAll (GET /v2/sandboxes), it remains actively served (not
// retired) on E2B Cloud and all self-hosted runtime <= 2026.30, and is required
// on self-hosted runtime < 2025.31.
//
// Deprecated: prefer NewClient(cfg).ListV1 (see List).
//
// Endpoint: GET /sandboxes
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2025.31; soft-deprecated in OpenAPI in favor of List / ListAll).
func ListV1(ctx context.Context, cfg Config, opts ...SandboxListV1Options) ([]SandboxInfo, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.ListV1(ctx, opts...)
}

// encodeMetadataQuery builds the metadata filter the API expects: each key and
// value URL-encoded, joined as key=value pairs with "&". The generated request
// builder then URL-encodes this whole string as a single query value, so the
// per-pair encoding survives the round trip (matching the Python/JS SDKs).
// Pairs are sorted for deterministic output.
func encodeMetadataQuery(md map[string]string) string {
	if len(md) == 0 {
		return ""
	}
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}
