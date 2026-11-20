package e2b

import (
	"context"
	"fmt"
	"net/http"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// Snapshot persistence modes for CreateSnapshotOptions.Mode and LifecycleOptions.Mode.
const (
	SnapshotModeFull       = "full"
	SnapshotModeFilesystem = "filesystem"
)

// CreateSnapshotOptions configures CreateSnapshot (POST /sandboxes/{sandboxID}/snapshots).
type CreateSnapshotOptions struct {
	// Name is an optional snapshot template name. If a snapshot template with
	// this name already exists, a new build is assigned to that template.
	Name string
	// Mode controls what the snapshot persists: "full" (memory + filesystem,
	// server default) or "filesystem" (filesystem only; sandboxes started from
	// it cold-boot from disk). Empty omits memory so the server default applies.
	Mode string
	// IdempotencyKey sets the optional Idempotency-Key header for safe retries
	// (supported on E2B Cloud).
	IdempotencyKey string
}

// SnapshotListOptions filters and paginates a snapshot list. The zero value
// lists every snapshot the caller can see, one server-defined page at a time.
type SnapshotListOptions struct {
	// SandboxID filters to snapshots created from a specific sandbox. Empty
	// means all sandboxes.
	SandboxID string
	// Name filters snapshots by name or ID, optionally tag-qualified (e.g.
	// "my-snapshot", "my-team/my-snapshot", or "my-snapshot:v1"; supported on
	// E2B Cloud and self-hosted runtime >= 2026.29).
	Name string
	// Limit caps the number of items per page. 0 uses the server default.
	Limit int32
	// NextToken resumes from a previous page's cursor. Usually left empty; the
	// paginator manages it internally.
	NextToken string
}

// SnapshotPaginator iterates snapshot list pages, following the server's
// cursor (the x-next-token response header). Obtain one from
// Client.ListSnapshots or Sandbox.ListSnapshots.
//
// A SnapshotPaginator is stateful and not safe for concurrent use.
type SnapshotPaginator struct {
	apiCli  *apiclient.Client
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
//
// Endpoint: GET /snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
func (p *SnapshotPaginator) NextItems(ctx context.Context) ([]SnapshotInfo, error) {
	if !p.hasNext {
		return nil, nil
	}

	params := &apiclient.GetSnapshotsParams{
		SandboxID: nilIfEmpty(p.opts.SandboxID),
		Name:      nilIfEmpty(p.opts.Name),
	}
	if p.opts.Limit > 0 {
		lim := apiclient.PaginationLimit(p.opts.Limit)
		params.Limit = &lim
	}
	if p.next != "" {
		tok := apiclient.PaginationNextToken(p.next)
		params.NextToken = &tok
	}

	resp, err := p.apiCli.GetSnapshots(ctx, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, ""); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSnapshotsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse snapshot list response", err)
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
	out := make([]SnapshotInfo, 0, len(listed))
	for _, s := range listed {
		out = append(out, SnapshotInfo{SnapshotID: s.SnapshotID, Names: s.Names})
	}
	return out, nil
}

// CreateSnapshot persists a sandbox's current state as a reusable snapshot
// template.
//
// Endpoint: POST /sandboxes/{sandboxID}/snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
// Envd: >= v0.5.0
func (c *Client) CreateSnapshot(ctx context.Context, sandboxID string, opts ...CreateSnapshotOptions) (*SnapshotInfo, error) {
	return createSandboxSnapshot(ctx, c.apiCli, sandboxID, opts...)
}

// CreateSnapshot persists this sandbox's current state as a reusable snapshot
// template.
//
// Endpoint: POST /sandboxes/{sandboxID}/snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
// Envd: >= v0.5.0
func (s *Sandbox) CreateSnapshot(ctx context.Context, opts ...CreateSnapshotOptions) (*SnapshotInfo, error) {
	return createSandboxSnapshot(ctx, s.apiCli, s.ID, opts...)
}

// ListSnapshots returns a paginator over the snapshots visible to this Client.
// The request is not issued until the first NextItems call.
//
// Endpoint: GET /snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
func (c *Client) ListSnapshots(_ context.Context, opts ...SnapshotListOptions) *SnapshotPaginator {
	var o SnapshotListOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return &SnapshotPaginator{apiCli: c.apiCli, opts: o, next: o.NextToken, hasNext: true}
}

// ListAllSnapshots drains every page and returns all matching snapshots. Prefer
// ListSnapshots for large result sets where streaming pages avoids buffering
// everything.
//
// Endpoint: GET /snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
func (c *Client) ListAllSnapshots(ctx context.Context, opts ...SnapshotListOptions) ([]SnapshotInfo, error) {
	p := c.ListSnapshots(ctx, opts...)
	return drainSnapshotPages(ctx, p)
}

// ListSnapshots returns a paginator over snapshots created from this sandbox.
// Any SandboxID in opts is overridden with s.ID.
//
// Endpoint: GET /snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
func (s *Sandbox) ListSnapshots(_ context.Context, opts ...SnapshotListOptions) *SnapshotPaginator {
	var o SnapshotListOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	o.SandboxID = s.ID
	return &SnapshotPaginator{apiCli: s.apiCli, opts: o, next: o.NextToken, hasNext: true}
}

// ListAllSnapshots drains every page of snapshots created from this sandbox.
//
// Endpoint: GET /snapshots
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
func (s *Sandbox) ListAllSnapshots(ctx context.Context, opts ...SnapshotListOptions) ([]SnapshotInfo, error) {
	p := s.ListSnapshots(ctx, opts...)
	return drainSnapshotPages(ctx, p)
}

// DeleteSnapshot deletes a snapshot by its ID (the snapshot template ID,
// optionally with a tag). Returns false (nil error) if the snapshot was already
// gone, mirroring Client.Kill.
//
// Endpoint: DELETE /templates/{templateID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.08..2026.30 (not in runtime < 2026.08).
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
		drainResponseBody(resp.Body)
		return false, nil
	}
	if err := mapHTTPErr(resp, ""); err != nil {
		return false, err
	}
	return true, nil
}

func createSandboxSnapshot(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ...CreateSnapshotOptions) (*SnapshotInfo, error) {
	var body apiclient.PostSandboxesSandboxIDSnapshotsJSONRequestBody
	var editors []apiclient.RequestEditorFn
	if len(opts) > 0 {
		o := opts[0]
		body.Name = nilIfEmpty(o.Name)
		mem, err := resolveSnapshotModeMemory("mode", o.Mode)
		if err != nil {
			return nil, err
		}
		body.Memory = mem
		if o.IdempotencyKey != "" {
			if err := validateIdempotencyKey(o.IdempotencyKey); err != nil {
				return nil, err
			}
			key := o.IdempotencyKey
			editors = append(editors, func(_ context.Context, req *http.Request) error {
				req.Header.Set("Idempotency-Key", key)
				return nil
			})
		}
	}

	resp, err := apiCli.PostSandboxesSandboxIDSnapshots(ctx, sandboxID, body, editors...)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
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

func resolveSnapshotModeMemory(field, mode string) (*bool, error) {
	switch mode {
	case "":
		return nil, nil
	case SnapshotModeFull:
		return new(true), nil
	case SnapshotModeFilesystem:
		return new(false), nil
	default:
		return nil, &InvalidArgumentError{
			Message: fmt.Sprintf("%s must be %q or %q, got %q", field, SnapshotModeFull, SnapshotModeFilesystem, mode),
		}
	}
}

func drainSnapshotPages(ctx context.Context, p *SnapshotPaginator) ([]SnapshotInfo, error) {
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
