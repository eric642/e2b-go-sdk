package template

import (
	"context"
	"encoding/json"
	"errors"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// List returns every template visible to the caller. Pass opts.TeamID to
// restrict the result to a specific team.
func (c *Client) List(ctx context.Context, opts ListOptions) ([]Template, error) {
	params := &apiclient.GetTemplatesParams{}
	if opts.TeamID != "" {
		tid := opts.TeamID
		params.TeamID = &tid
	}
	resp, err := c.apiCli.GetTemplates(ctx, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "list", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesResponse(resp)
	if err != nil {
		return nil, err
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

// Get returns metadata and one page of build history for a single template.
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
	resp, err := c.apiCli.GetTemplatesTemplateID(ctx, apiclient.TemplateID(templateID), params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "get", TemplateID: templateID, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDResponse(resp)
	if err != nil {
		return nil, err
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
	detail := &TemplateDetail{
		TemplateID:    body.TemplateID,
		Names:         body.Names,
		Public:        body.Public,
		SpawnCount:    body.SpawnCount,
		CreatedAt:     body.CreatedAt,
		UpdatedAt:     body.UpdatedAt,
		LastSpawnedAt: body.LastSpawnedAt,
	}
	detail.Builds = make([]TemplateBuild, 0, len(body.Builds))
	for _, b := range body.Builds {
		detail.Builds = append(detail.Builds, toTemplateBuild(b))
	}
	return detail, nil
}

// SetPublic toggles the template's public visibility flag via
// PATCH /v2/templates/{id}. Returns the current names (namespace/alias
// format when namespaced).
func (c *Client) SetPublic(ctx context.Context, templateID string, public bool) ([]string, error) {
	body := apiclient.PatchV2TemplatesTemplateIDJSONRequestBody{Public: &public}
	resp, err := c.apiCli.PatchV2TemplatesTemplateID(ctx, apiclient.TemplateID(templateID), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "setPublic", TemplateID: templateID, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePatchV2TemplatesTemplateIDResponse(resp)
	if err != nil {
		return nil, err
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

// SetPublicV1 is the PATCH /templates/{id} counterpart of SetPublic for
// self-hosted E2B deployments that predate /v2/templates/{id} (≤ 2.1.x).
// The legacy endpoint has no response body, so no names are returned.
func (c *Client) SetPublicV1(ctx context.Context, templateID string, public bool) error {
	body := apiclient.PatchTemplatesTemplateIDJSONRequestBody{Public: &public}
	resp, err := c.apiCli.PatchTemplatesTemplateID(ctx, apiclient.TemplateID(templateID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "setPublic", TemplateID: templateID, Err: errors.New(resp.Status)}
	}
	return nil
}

// GetBuildLogs fetches structured build logs for a specific build.
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
	resp, err := c.apiCli.GetTemplatesTemplateIDBuildsBuildIDLogs(ctx,
		apiclient.TemplateID(templateID), apiclient.BuildID(buildID), params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "buildLogs", TemplateID: templateID, BuildID: buildID, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDBuildsBuildIDLogsResponse(resp)
	if err != nil {
		return nil, err
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
	out := Template{
		TemplateID:    t.TemplateID,
		BuildID:       t.BuildID,
		Names:         t.Names,
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
	if t.CreatedBy != nil {
		out.CreatedBy = &TemplateUser{
			ID:    t.CreatedBy.Id.String(),
			Email: t.CreatedBy.Email,
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
	if b.DiskSizeMB != nil {
		v := int32(*b.DiskSizeMB)
		tb.DiskSizeMB = &v
	}
	if b.EnvdVersion != nil {
		tb.EnvdVersion = string(*b.EnvdVersion)
	}
	return tb
}
