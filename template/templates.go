package template

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// List returns every template visible to the caller via the unpaginated v1
// endpoint. Pass opts.TeamID to restrict the result to a specific team.
//
// Although marked deprecated in OpenAPI since runtime 2026.26 (because it is
// unpaginated and returns 400 when a team exceeds 5000 templates), GET /templates
// remains actively served on both E2B Cloud and all self-hosted runtime <= 2026.30.
// Use List / ListV1 on self-hosted runtime < 2026.26 (where GET /v2/templates
// does not exist); prefer ListV2 / ListAllV2 on E2B Cloud and runtime 2026.26..2026.30.
//
// Endpoint: GET /templates
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served everywhere; soft-deprecated in OpenAPI since runtime 2026.26 in favor of ListV2, required on self-hosted runtime < 2026.26).
func (c *Client) List(ctx context.Context, opts ListOptions) ([]Template, error) {
	params := &apiclient.GetTemplatesParams{}
	if opts.TeamID != "" {
		tid := opts.TeamID
		params.TeamID = &tid
	}
	resp, err := c.doWithTokenFallback(fallbackOpList, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.GetTemplates(ctx, params) //nolint:staticcheck // legacy v1 route retained for self-hosted < 2026.26 compatibility.
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "list", "", "")
	}
	parsed, err := apiclient.ParseGetTemplatesResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "list", Err: err}
	}
	var rows []apiclient.Template
	if parsed.JSON200 != nil {
		rows = *parsed.JSON200
	} else if len(parsed.Body) > 0 {
		if err := json.Unmarshal(parsed.Body, &rows); err != nil {
			return nil, &BuildError{Op: "list", Err: err}
		}
	}
	out := make([]Template, 0, len(rows))
	for _, row := range rows {
		out = append(out, toTemplate(row))
	}
	return out, nil
}

// ListV1 is an explicit generation-suffixed alias for List, calling the
// unpaginated GET /templates endpoint.
//
// Although marked deprecated in OpenAPI since runtime 2026.26 in favor of
// cursor-paginated GET /v2/templates (ListV2), GET /templates remains actively
// served on both E2B Cloud and all self-hosted runtime <= 2026.30, and is
// required on self-hosted runtime < 2026.26 where GET /v2/templates does not exist.
//
// Endpoint: GET /templates
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served everywhere; soft-deprecated in OpenAPI since runtime 2026.26 in favor of ListV2, required on self-hosted runtime < 2026.26).
func (c *Client) ListV1(ctx context.Context, opts ListOptions) ([]Template, error) {
	return c.List(ctx, opts)
}

// TemplatePaginator iterates pages of templates from GET /v2/templates using
// the server's x-next-token response header cursor.
//
// A TemplatePaginator is stateful and not safe for concurrent use.
type TemplatePaginator struct {
	c       *Client
	opts    ListV2Options
	next    string
	hasNext bool
}

// HasNext reports whether another page of templates is available. It is true
// before the first NextItems call; afterwards it reflects the x-next-token
// header of the most recent response.
func (p *TemplatePaginator) HasNext() bool { return p.hasNext }

// NextToken returns the cursor for the next page, or "" when exhausted. It can
// be persisted and resumed via ListV2Options.NextToken.
func (p *TemplatePaginator) NextToken() string { return p.next }

// NextItems fetches the next page of templates from GET /v2/templates. Once
// exhausted it returns (nil, nil). When ListV2Options.Names is non-empty,
// pages whose entries are all filtered out are skipped automatically until a
// matching item is found or all pages are exhausted.
//
// Endpoint: GET /v2/templates
// Compatibility: E2B Cloud; self-hosted runtime 2026.26..2026.30 (use List / ListV1 on < 2026.26).
func (p *TemplatePaginator) NextItems(ctx context.Context) ([]Template, error) {
	for p.hasNext {
		params := &apiclient.GetV2TemplatesParams{}
		if p.opts.TeamID != "" {
			tid := p.opts.TeamID
			params.TeamID = &tid
		}
		if p.opts.Limit > 0 {
			lim := apiclient.PaginationLimit(p.opts.Limit)
			params.Limit = &lim
		}
		if p.next != "" {
			tok := apiclient.PaginationNextToken(p.next)
			params.NextToken = &tok
		}

		resp, err := p.c.apiCli.GetV2Templates(ctx, params)
		if err != nil {
			return nil, mapTransportErr(err)
		}
		if resp.StatusCode >= 300 {
			err := decodeBuildAPIError(resp, "listV2", "", "")
			_ = resp.Body.Close()
			return nil, err
		}
		token := resp.Header.Get("x-next-token")
		parsed, err := apiclient.ParseGetV2TemplatesResponse(resp)
		_ = resp.Body.Close()
		if err != nil {
			return nil, &BuildError{Op: "listV2", Err: err}
		}

		p.next = token
		p.hasNext = token != ""

		var rows []apiclient.Template
		if parsed.JSON200 != nil {
			rows = *parsed.JSON200
		} else if len(parsed.Body) > 0 {
			if err := json.Unmarshal(parsed.Body, &rows); err != nil {
				return nil, &BuildError{Op: "listV2", Err: err}
			}
		}

		out := make([]Template, 0, len(rows))
		for _, row := range rows {
			tm := toTemplate(row)
			if templateMatchesNames(tm, p.opts.Names) {
				out = append(out, tm)
			}
		}
		if len(out) > 0 || len(p.opts.Names) == 0 || !p.hasNext {
			return out, nil
		}
	}
	return nil, nil
}

func templateMatchesNames(tm Template, names []string) bool {
	if len(names) == 0 {
		return true
	}
	for _, want := range names {
		if want == "" {
			continue
		}
		if tm.TemplateID == want {
			return true
		}
		if slices.Contains(tm.Aliases, want) {
			return true
		}
		for _, n := range tm.Names {
			if n == want {
				return true
			}
			if !strings.Contains(want, "/") {
				if _, alias, ok := strings.Cut(n, "/"); ok && alias == want {
					return true
				}
			}
		}
	}
	return false
}

// ListV2 returns a cursor-based TemplatePaginator backed by GET /v2/templates.
// No network request is issued until the first NextItems call.
//
// Endpoint: GET /v2/templates
// Compatibility: E2B Cloud; self-hosted runtime 2026.26..2026.30 (use List / ListV1 on < 2026.26).
func (c *Client) ListV2(_ context.Context, opts ListV2Options) *TemplatePaginator {
	cloned := opts
	cloned.Names = slices.Clone(opts.Names)
	return &TemplatePaginator{
		c:       c,
		opts:    cloned,
		next:    cloned.NextToken,
		hasNext: true,
	}
}

// ListAllV2 drains every page from GET /v2/templates and returns all matching
// templates.
//
// Endpoint: GET /v2/templates
// Compatibility: E2B Cloud; self-hosted runtime 2026.26..2026.30 (use List / ListV1 on < 2026.26).
func (c *Client) ListAllV2(ctx context.Context, opts ListV2Options) ([]Template, error) {
	p := c.ListV2(ctx, opts)
	var all []Template
	for p.HasNext() {
		page, err := p.NextItems(ctx)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// ListDefault lists the platform's default templates ordered by template ID.
//
// Endpoint: GET /default-templates
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30).
func (c *Client) ListDefault(ctx context.Context) ([]Template, error) {
	resp, err := c.apiCli.GetDefaultTemplates(ctx)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "listDefault", "", "")
	}
	parsed, err := apiclient.ParseGetDefaultTemplatesResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "listDefault", Err: err}
	}
	var rows []apiclient.Template
	if parsed.JSON200 != nil {
		rows = *parsed.JSON200
	} else if len(parsed.Body) > 0 {
		if err := json.Unmarshal(parsed.Body, &rows); err != nil {
			return nil, &BuildError{Op: "listDefault", Err: err}
		}
	}
	out := make([]Template, 0, len(rows))
	for _, row := range rows {
		out = append(out, toTemplate(row))
	}
	return out, nil
}

// Get returns template metadata and one page of build history for a single
// template, populating TemplateDetail.NextToken from the x-next-token response
// header when further build pages exist.
//
// Endpoint: GET /templates/{templateID}
// Compatibility: E2B Cloud; self-hosted runtime 2025.45..2026.30 (not in < 2025.45).
func (c *Client) Get(ctx context.Context, templateID string, opts GetOptions) (*TemplateDetail, error) {
	params := &apiclient.GetTemplatesTemplateIDParams{}
	if opts.NextToken != "" {
		tok := apiclient.PaginationNextToken(opts.NextToken)
		params.NextToken = &tok
	}
	if opts.Limit > 0 {
		lim := apiclient.PaginationLimit(opts.Limit)
		params.Limit = &lim
	}
	resp, err := c.doWithTokenFallback(fallbackOpGet, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.GetTemplatesTemplateID(ctx, apiclient.TemplateID(templateID), params)
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "get", templateID, "")
	}
	nextToken := resp.Header.Get("x-next-token")
	parsed, err := apiclient.ParseGetTemplatesTemplateIDResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "get", TemplateID: templateID, Err: err}
	}
	var body apiclient.TemplateWithBuilds
	switch {
	case parsed.JSON200 != nil:
		body = *parsed.JSON200
	case len(parsed.Body) > 0:
		if err := json.Unmarshal(parsed.Body, &body); err != nil {
			return nil, &BuildError{Op: "get", TemplateID: templateID, Err: err}
		}
	default:
		return nil, &BuildError{Op: "get", TemplateID: templateID, Err: errors.New("empty response body")}
	}
	if nextToken == "" && parsed.Headers200 != nil && parsed.Headers200.XNextToken != nil {
		nextToken = *parsed.Headers200.XNextToken
	}

	names := body.Names
	if len(names) == 0 && len(body.Aliases) > 0 { //nolint:staticcheck // fallback to deprecated aliases on older servers.
		names = slices.Clone(body.Aliases) //nolint:staticcheck // fallback to deprecated aliases on older servers.
	}
	detail := &TemplateDetail{
		TemplateID:    body.TemplateID,
		BuildID:       body.BuildID,
		Names:         names,
		Aliases:       slices.Clone(body.Aliases), //nolint:staticcheck // expose legacy aliases alongside names.
		Public:        body.Public,
		CPUCount:      int32(body.CpuCount),
		MemoryMB:      int32(body.MemoryMB),
		DiskSizeMB:    int32(body.DiskSizeMB),
		EnvdVersion:   string(body.EnvdVersion),
		BuildStatus:   BuildStatusValue(body.BuildStatus),
		BuildCount:    body.BuildCount,
		SpawnCount:    body.SpawnCount,
		CreatedAt:     body.CreatedAt,
		UpdatedAt:     body.UpdatedAt,
		LastSpawnedAt: body.LastSpawnedAt,
		NextToken:     nextToken,
	}
	if body.Description != nil {
		detail.Description = *body.Description
	}
	if body.IsDefault != nil {
		detail.IsDefault = *body.IsDefault
	}
	if body.MaxCpuCount != nil {
		mcc := int32(*body.MaxCpuCount)
		detail.MaxCPUCount = &mcc
	}
	if body.CreatedBy != nil {
		detail.CreatedBy = &TemplateUser{
			ID: body.CreatedBy.Id.String(),
		}
		if body.CreatedBy.Email != nil { //nolint:staticcheck // expose creator email when provided by server.
			detail.CreatedBy.Email = *body.CreatedBy.Email //nolint:staticcheck // expose creator email when provided by server.
		}
	}
	detail.Builds = make([]TemplateBuild, 0, len(body.Builds))
	for _, b := range body.Builds {
		detail.Builds = append(detail.Builds, toTemplateBuild(b))
	}
	return detail, nil
}

// SetPublic toggles the template's public visibility flag via
// PATCH /v2/templates/{templateID}. Returns the current names (namespace/alias
// format when namespaced).
//
// Endpoint: PATCH /v2/templates/{templateID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.05..2026.30 (use SetPublicV1 on < 2026.05).
func (c *Client) SetPublic(ctx context.Context, templateID string, public bool) ([]string, error) {
	body := apiclient.PatchV2TemplatesTemplateIDJSONRequestBody{Public: &public}
	resp, err := c.apiCli.PatchV2TemplatesTemplateID(ctx, apiclient.TemplateID(templateID), body)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "setPublic", templateID, "")
	}
	parsed, err := apiclient.ParsePatchV2TemplatesTemplateIDResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "setPublic", TemplateID: templateID, Err: err}
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200.Names, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateUpdateResponse
		if err := json.Unmarshal(parsed.Body, &out); err == nil {
			return out.Names, nil
		}
	}
	return nil, &BuildError{Op: "setPublic", TemplateID: templateID, Err: errors.New("empty response body")}
}

// SetPublicV1 toggles template public visibility via PATCH /templates/{templateID}.
// Unlike SetPublic (PATCH /v2/templates/{templateID}), the v1 endpoint returns
// an empty response body rather than the template's updated names.
//
// Although marked deprecated in OpenAPI since runtime 2026.05 in favor of
// SetPublic, PATCH /templates/{templateID} remains actively served on both
// E2B Cloud and all self-hosted runtime <= 2026.30. Use SetPublicV1 on
// self-hosted runtime < 2026.05 (where PATCH /v2/templates/{templateID} does
// not exist); prefer SetPublic on E2B Cloud and runtime 2026.05..2026.30.
//
// Endpoint: PATCH /templates/{templateID}
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served everywhere; soft-deprecated in OpenAPI since runtime 2026.05 in favor of SetPublic, required on self-hosted runtime < 2026.05).
func (c *Client) SetPublicV1(ctx context.Context, templateID string, public bool) error {
	body := apiclient.PatchTemplatesTemplateIDJSONRequestBody{Public: &public} //nolint:staticcheck // legacy v1 route kept for old self-hosted servers.
	resp, err := c.doWithTokenFallback(fallbackOpSetPublicV1, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.PatchTemplatesTemplateID(ctx, apiclient.TemplateID(templateID), body) //nolint:staticcheck // legacy v1 route kept for old self-hosted servers.
	})
	if err != nil {
		return mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeBuildAPIError(resp, "setPublic", templateID, "")
	}
	return nil
}

// GetBuildLogs fetches structured build logs for a specific build.
//
// Endpoint: GET /templates/{templateID}/builds/{buildID}/logs
// Compatibility: E2B Cloud; self-hosted runtime 2025.50..2026.30 (not in < 2025.50; use GetBuildStatus logs).
func (c *Client) GetBuildLogs(ctx context.Context, templateID, buildID string, opts BuildLogsOptions) ([]LogEntry, error) {
	params := &apiclient.GetTemplatesTemplateIDBuildsBuildIDLogsParams{}
	if opts.CursorMs != 0 {
		c := opts.CursorMs
		params.Cursor = &c
	}
	if opts.Limit > 0 {
		l := opts.Limit
		params.Limit = &l
	}
	if opts.Direction != "" {
		d := apiclient.LogsDirection(opts.Direction)
		params.Direction = &d
	}
	if opts.Level != "" {
		l := apiclient.LogLevel(opts.Level)
		params.Level = &l
	}
	if opts.Source != "" {
		s := apiclient.LogsSource(opts.Source)
		params.Source = &s
	}
	resp, err := c.apiCli.GetTemplatesTemplateIDBuildsBuildIDLogs(ctx,
		apiclient.TemplateID(templateID), apiclient.BuildID(buildID), params)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "buildLogs", templateID, buildID)
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDBuildsBuildIDLogsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "buildLogs", TemplateID: templateID, BuildID: buildID, Err: err}
	}
	var body apiclient.TemplateBuildLogsResponse
	switch {
	case parsed.JSON200 != nil:
		body = *parsed.JSON200
	case len(parsed.Body) > 0:
		if err := json.Unmarshal(parsed.Body, &body); err != nil {
			return nil, &BuildError{Op: "buildLogs", TemplateID: templateID, BuildID: buildID, Err: err}
		}
	}
	out := make([]LogEntry, 0, len(body.Logs))
	for _, le := range body.Logs {
		out = append(out, toLogEntry(le))
	}
	return out, nil
}

func toTemplate(t apiclient.Template) Template {
	names := t.Names
	if len(names) == 0 && len(t.Aliases) > 0 { //nolint:staticcheck // fallback to deprecated aliases on older servers.
		names = slices.Clone(t.Aliases) //nolint:staticcheck // fallback to deprecated aliases on older servers.
	}
	out := Template{
		TemplateID:    t.TemplateID,
		BuildID:       t.BuildID,
		Names:         names,
		Aliases:       slices.Clone(t.Aliases), //nolint:staticcheck // expose legacy aliases alongside names.
		Public:        t.Public,
		CPUCount:      int32(t.CpuCount),
		MemoryMB:      int32(t.MemoryMB),
		DiskSizeMB:    int32(t.DiskSizeMB),
		EnvdVersion:   string(t.EnvdVersion),
		BuildStatus:   BuildStatusValue(t.BuildStatus),
		BuildCount:    t.BuildCount,
		SpawnCount:    t.SpawnCount,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
		LastSpawnedAt: t.LastSpawnedAt,
	}
	if t.Description != nil {
		out.Description = *t.Description
	}
	if t.IsDefault != nil {
		out.IsDefault = *t.IsDefault
	}
	if t.MaxCpuCount != nil {
		mcc := int32(*t.MaxCpuCount)
		out.MaxCPUCount = &mcc
	}
	if t.CreatedBy != nil {
		out.CreatedBy = &TemplateUser{
			ID: t.CreatedBy.Id.String(),
		}
		if t.CreatedBy.Email != nil { //nolint:staticcheck // expose creator email when provided by server.
			out.CreatedBy.Email = *t.CreatedBy.Email //nolint:staticcheck // expose creator email when provided by server.
		}
	}
	return out
}

func toTemplateBuild(b apiclient.TemplateBuild) TemplateBuild {
	tb := TemplateBuild{
		BuildID:    b.BuildID.String(),
		Status:     BuildStatusValue(b.Status),
		CPUCount:   int32(b.CpuCount),
		MemoryMB:   int32(b.MemoryMB),
		CreatedAt:  b.CreatedAt,
		UpdatedAt:  b.UpdatedAt,
		FinishedAt: b.FinishedAt,
	}
	if b.MaxCpuCount != nil {
		mcc := int32(*b.MaxCpuCount)
		tb.MaxCPUCount = &mcc
	}
	if b.DiskSizeMB != nil {
		v := int32(*b.DiskSizeMB)
		tb.DiskSizeMB = &v
	}
	if b.EnvdVersion != nil {
		tb.EnvdVersion = string(*b.EnvdVersion)
	}
	return tb
}
