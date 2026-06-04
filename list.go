package e2b

import (
	"context"
	"net/url"
	"sort"
	"strings"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// SandboxListOptions filters and paginates a sandbox list. The zero value
// lists all sandboxes the caller can see, one server-defined page at a time.
type SandboxListOptions struct {
	// Metadata filters by exact key/value pairs (all must match).
	Metadata map[string]string
	// State filters by one or more states. Empty means all states.
	State []SandboxState
	// Limit caps the number of items per page. 0 uses the server default.
	Limit int32
	// NextToken resumes from a previous page's cursor. Usually left empty;
	// the paginator manages it internally.
	NextToken string
}

// SandboxPaginator iterates sandbox list pages, following the server's
// cursor (the x-next-token response header). Obtain one from Client.List.
//
// A SandboxPaginator is stateful and not safe for concurrent use.
type SandboxPaginator struct {
	c       *Client
	opts    SandboxListOptions
	next    string
	hasNext bool
}

// HasNext reports whether another page is available. It is true before the
// first NextItems call; afterwards it reflects the x-next-token header of the
// most recent response.
func (p *SandboxPaginator) HasNext() bool { return p.hasNext }

// NextToken returns the cursor for the next page, or "" when exhausted. It can
// be persisted and replayed via SandboxListOptions.NextToken.
func (p *SandboxPaginator) NextToken() string { return p.next }

// NextItems fetches the next page of sandboxes. Call it only while HasNext
// reports true; once exhausted it returns (nil, nil). After the call HasNext
// reflects whether further pages remain.
func (p *SandboxPaginator) NextItems(ctx context.Context) ([]SandboxInfo, error) {
	if !p.hasNext {
		return nil, nil
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
	// mapHTTPErr returns nil for 2xx without reading the body, leaving it for
	// ParseGetV2SandboxesResponse below. On an error status it consumes the
	// body, so we must not also parse it.
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetV2SandboxesResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse list response", err)
	}

	// Advance the cursor from the x-next-token response header.
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
	out := make([]SandboxInfo, 0, len(listed))
	for i := range listed {
		out = append(out, *sandboxInfoFromListed(&listed[i]))
	}
	return out, nil
}

// List returns a paginator over the sandboxes visible to this Client. The
// request is not issued until the first NextItems call.
//
// Example:
//
//	p := c.List(ctx, e2b.SandboxListOptions{State: []e2b.SandboxState{e2b.SandboxStateRunning}})
//	for p.HasNext() {
//		page, err := p.NextItems(ctx)
//		if err != nil {
//			return err
//		}
//		for _, s := range page {
//			fmt.Println(s.SandboxID)
//		}
//	}
//
// SandboxInfo records returned here are lighter than Sandbox.GetInfo: see
// sandboxInfoFromListed.
func (c *Client) List(_ context.Context, opts SandboxListOptions) *SandboxPaginator {
	return &SandboxPaginator{c: c, opts: opts, next: opts.NextToken, hasNext: true}
}

// ListAll drains every page and returns all matching sandboxes. Prefer List
// for large result sets where streaming pages avoids buffering everything.
func (c *Client) ListAll(ctx context.Context, opts SandboxListOptions) ([]SandboxInfo, error) {
	p := c.List(ctx, opts)
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

// List returns a paginator over sandboxes using a one-shot Client built from
// cfg.
//
// Deprecated: prefer NewClient(cfg).List, which reuses one control-plane REST
// client across calls.
func List(ctx context.Context, cfg Config, opts SandboxListOptions) (*SandboxPaginator, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.List(ctx, opts), nil
}

// ListAll drains every page using a one-shot Client built from cfg.
//
// Deprecated: prefer NewClient(cfg).ListAll (see List).
func ListAll(ctx context.Context, cfg Config, opts SandboxListOptions) ([]SandboxInfo, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.ListAll(ctx, opts)
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
