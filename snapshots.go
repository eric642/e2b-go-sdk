package e2b

import (
	"context"
	"net/http"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// SnapshotListOptions filters and paginates a snapshot list. The zero value
// lists every snapshot the caller can see, one server-defined page at a time.
type SnapshotListOptions struct {
	// SandboxID filters to snapshots created from a specific sandbox. Empty
	// means all sandboxes.
	SandboxID string
	// Limit caps the number of items per page. 0 uses the server default.
	Limit int32
	// NextToken resumes from a previous page's cursor. Usually left empty; the
	// paginator manages it internally.
	NextToken string
}

// SnapshotPaginator iterates snapshot list pages, following the server's
// cursor (the x-next-token response header). Obtain one from Client.ListSnapshots.
//
// A SnapshotPaginator is stateful and not safe for concurrent use.
type SnapshotPaginator struct {
	c       *Client
	opts    SnapshotListOptions
	next    string
	hasNext bool
}

// HasNext reports whether another page is available. It is true before the
// first NextItems call; afterwards it reflects the x-next-token header of the
// most recent response.
func (p *SnapshotPaginator) HasNext() bool { return p.hasNext }

// NextToken returns the cursor for the next page, or "" when exhausted. It can
// be persisted and replayed via SnapshotListOptions.NextToken.
func (p *SnapshotPaginator) NextToken() string { return p.next }

// NextItems fetches the next page of snapshots. Call it only while HasNext
// reports true; once exhausted it returns (nil, nil). After the call HasNext
// reflects whether further pages remain.
func (p *SnapshotPaginator) NextItems(ctx context.Context) ([]SnapshotInfo, error) {
	if !p.hasNext {
		return nil, nil
	}

	params := &apiclient.GetSnapshotsParams{}
	if p.opts.SandboxID != "" {
		sid := p.opts.SandboxID
		params.SandboxID = &sid
	}
	if p.opts.Limit > 0 {
		lim := apiclient.PaginationLimit(p.opts.Limit)
		params.Limit = &lim
	}
	if p.next != "" {
		tok := apiclient.PaginationNextToken(p.next)
		params.NextToken = &tok
	}

	resp, err := p.c.apiCli.GetSnapshots(ctx, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	// mapHTTPErr returns nil for 2xx without reading the body, leaving it for
	// ParseGetSnapshotsResponse below. On an error status it consumes the body,
	// so we must not also parse it.
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSnapshotsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse snapshot list response", err)
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
	out := make([]SnapshotInfo, 0, len(listed))
	for _, s := range listed {
		out = append(out, SnapshotInfo{SnapshotID: s.SnapshotID, Names: s.Names})
	}
	return out, nil
}

// ListSnapshots returns a paginator over the snapshots visible to this Client.
// The request is not issued until the first NextItems call.
//
// Example:
//
//	p := c.ListSnapshots(ctx, e2b.SnapshotListOptions{})
//	for p.HasNext() {
//		page, err := p.NextItems(ctx)
//		if err != nil {
//			return err
//		}
//		for _, s := range page {
//			fmt.Println(s.SnapshotID)
//		}
//	}
func (c *Client) ListSnapshots(_ context.Context, opts SnapshotListOptions) *SnapshotPaginator {
	return &SnapshotPaginator{c: c, opts: opts, next: opts.NextToken, hasNext: true}
}

// ListAllSnapshots drains every page and returns all matching snapshots. Prefer
// ListSnapshots for large result sets where streaming pages avoids buffering
// everything.
func (c *Client) ListAllSnapshots(ctx context.Context, opts SnapshotListOptions) ([]SnapshotInfo, error) {
	p := c.ListSnapshots(ctx, opts)
	var all []SnapshotInfo
	for p.HasNext() {
		page, err := p.NextItems(ctx)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// DeleteSnapshot deletes a snapshot by its ID (the snapshot template ID,
// optionally with a tag). Returns false (nil error) if the snapshot was already
// gone, mirroring Client.Kill.
func (c *Client) DeleteSnapshot(ctx context.Context, snapshotID string) (bool, error) {
	if c.cfg.Debug {
		return true, nil
	}
	resp, err := c.apiCli.DeleteTemplatesTemplateID(ctx, snapshotID)
	if err != nil {
		return false, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := mapHTTPErr(resp, ""); err != nil {
		return false, err
	}
	return true, nil
}
