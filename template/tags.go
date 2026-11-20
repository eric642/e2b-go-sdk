package template

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// Exists returns true if a template with the given alias/name exists (200 OK
// or 403 Forbidden when owned by another team). 404 means it does not exist;
// other non-2xx status codes are returned as errors.
//
// Endpoint: GET /templates/aliases/{alias}
// Compatibility: E2B Cloud; self-hosted runtime 2026.02..2026.30 (not in < 2026.02).
func (c *Client) Exists(ctx context.Context, alias string) (bool, error) {
	resp, err := c.apiCli.GetTemplatesAliasesAlias(ctx, alias)
	if err != nil {
		return false, mapTransportErr(err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		return true, nil
	}
	if resp.StatusCode >= 300 {
		return false, decodeBuildAPIError(resp, "exists", "", "")
	}
	return true, nil
}

// AssignTags assigns one or more tags to an existing template build. For
// idempotent retries via Idempotency-Key, use AssignTagsWithOptions.
//
// Endpoint: POST /templates/tags
// Compatibility: E2B Cloud; self-hosted runtime 2026.01..2026.30 (not in < 2026.01).
func (c *Client) AssignTags(ctx context.Context, target string, tags []string) (*TagInfo, error) {
	return c.AssignTagsWithOptions(ctx, target, tags, AssignTagsOptions{})
}

// AssignTagsWithOptions assigns one or more tags to an existing template build,
// optionally attaching an Idempotency-Key header via opts.IdempotencyKey.
//
// Endpoint: POST /templates/tags
// Compatibility: E2B Cloud; self-hosted runtime 2026.01..2026.30 (Idempotency-Key honored on E2B Cloud; not in < 2026.01).
func (c *Client) AssignTagsWithOptions(ctx context.Context, target string, tags []string, opts AssignTagsOptions) (*TagInfo, error) {
	if err := validateIdempotencyKey(opts.IdempotencyKey); err != nil {
		return nil, err
	}
	body := apiclient.AssignTemplateTagsRequest{Target: target, Tags: tags}
	resp, err := c.apiCli.PostTemplatesTags(ctx, body, idempotencyEditor(opts.IdempotencyKey)...)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "tags.assign", "", "")
	}
	parsed, err := apiclient.ParsePostTemplatesTagsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "tags.assign", Err: err}
	}
	// The generated parser only populates JSON201 for the documented 201
	// response. Accept any 2xx by decoding the raw body as a fallback.
	if parsed.JSON201 != nil {
		return &TagInfo{BuildID: parsed.JSON201.BuildID.String(), Tags: parsed.JSON201.Tags}, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.AssignedTemplateTags
		if err := json.Unmarshal(parsed.Body, &out); err == nil {
			return &TagInfo{BuildID: out.BuildID.String(), Tags: out.Tags}, nil
		}
	}
	return nil, &BuildError{Op: "tags.assign", Err: errors.New("empty response body")}
}

// RemoveTags removes one or more tags from a template.
//
// Endpoint: DELETE /templates/tags
// Compatibility: E2B Cloud; self-hosted runtime 2026.01..2026.30 (not in < 2026.01).
func (c *Client) RemoveTags(ctx context.Context, name string, tags []string) error {
	body := apiclient.DeleteTemplateTagsRequest{Name: name, Tags: tags}
	resp, err := c.apiCli.DeleteTemplatesTags(ctx, body)
	if err != nil {
		return mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeBuildAPIError(resp, "tags.remove", "", "")
	}
	return nil
}

// GetTags returns every tag associated with a template.
//
// Endpoint: GET /templates/{templateID}/tags
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in < 2026.07).
func (c *Client) GetTags(ctx context.Context, templateID string) ([]TemplateTag, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDTags(ctx, apiclient.TemplateID(templateID))
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "tags.get", templateID, "")
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "tags.get", TemplateID: templateID, Err: err}
	}
	var rows []apiclient.TemplateTag
	if parsed.JSON200 != nil {
		rows = *parsed.JSON200
	} else if len(parsed.Body) > 0 {
		if err := json.Unmarshal(parsed.Body, &rows); err != nil {
			return nil, &BuildError{Op: "tags.get", TemplateID: templateID, Err: err}
		}
	}
	out := make([]TemplateTag, 0, len(rows))
	for _, it := range rows {
		out = append(out, TemplateTag{
			Tag:       it.Tag,
			BuildID:   it.BuildID.String(),
			CreatedAt: it.CreatedAt,
		})
	}
	return out, nil
}

// CountTags returns the number of distinct tags on the template that have at
// least one ready build.
//
// Endpoint: GET /templates/{templateID}/tags/count
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30).
func (c *Client) CountTags(ctx context.Context, templateID string) (int64, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDTagsCount(ctx, apiclient.TemplateID(templateID))
	if err != nil {
		return 0, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, decodeBuildAPIError(resp, "tags.count", templateID, "")
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsCountResponse(resp)
	if err != nil {
		return 0, &BuildError{Op: "tags.count", TemplateID: templateID, Err: err}
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200.Total, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateTagsCountResponse
		if err := json.Unmarshal(parsed.Body, &out); err == nil {
			return out.Total, nil
		}
	}
	return 0, &BuildError{Op: "tags.count", TemplateID: templateID, Err: errors.New("empty response body")}
}

// TagExists checks whether a tag on the given template has at least one ready
// build, returning both Exists and the server's NormalizedTag.
//
// Endpoint: GET /templates/{templateID}/tags/exists
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30).
func (c *Client) TagExists(ctx context.Context, templateID, tag string) (*TagExistsResult, error) {
	if tag == "" {
		return nil, &e2b.InvalidArgumentError{Message: "tag is required"}
	}
	params := &apiclient.GetTemplatesTemplateIDTagsExistsParams{Tag: tag}
	resp, err := c.apiCli.GetTemplatesTemplateIDTagsExists(ctx, apiclient.TemplateID(templateID), params)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "tags.exists", templateID, "")
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsExistsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "tags.exists", TemplateID: templateID, Err: err}
	}
	if parsed.JSON200 != nil {
		return &TagExistsResult{
			Exists:        parsed.JSON200.Exists,
			NormalizedTag: parsed.JSON200.NormalizedTag,
		}, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateTagExistsResponse
		if err := json.Unmarshal(parsed.Body, &out); err == nil {
			return &TagExistsResult{
				Exists:        out.Exists,
				NormalizedTag: out.NormalizedTag,
			}, nil
		}
	}
	return nil, &BuildError{Op: "tags.exists", TemplateID: templateID, Err: errors.New("empty response body")}
}

// ListTagGroups lists the template's tags that have at least one ready build,
// each with its most recent ready assignments, paginated via a keyset cursor.
//
// Endpoint: GET /templates/{templateID}/tags/groups
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30).
func (c *Client) ListTagGroups(ctx context.Context, templateID string, opts TagGroupsOptions) (*TagGroupsPage, error) {
	params := &apiclient.GetTemplatesTemplateIDTagsGroupsParams{}
	if opts.AssignmentLimit > 0 {
		al := opts.AssignmentLimit
		params.AssignmentLimit = &al
	}
	if opts.TagsLimit > 0 {
		tl := opts.TagsLimit
		params.TagsLimit = &tl
	}
	if opts.TagsCursor != "" {
		tc := opts.TagsCursor
		params.TagsCursor = &tc
	}
	if opts.Search != "" {
		s := opts.Search
		params.Search = &s
	}
	if opts.Sort != "" {
		sortVal := apiclient.GetTemplatesTemplateIDTagsGroupsParamsSort(opts.Sort)
		params.Sort = &sortVal
	}

	resp, err := c.apiCli.GetTemplatesTemplateIDTagsGroups(ctx, apiclient.TemplateID(templateID), params)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "tags.groups", templateID, "")
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsGroupsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "tags.groups", TemplateID: templateID, Err: err}
	}
	var body apiclient.TemplateTagGroupsResponse
	switch {
	case parsed.JSON200 != nil:
		body = *parsed.JSON200
	case len(parsed.Body) > 0:
		if err := json.Unmarshal(parsed.Body, &body); err != nil {
			return nil, &BuildError{Op: "tags.groups", TemplateID: templateID, Err: err}
		}
	default:
		return nil, &BuildError{Op: "tags.groups", TemplateID: templateID, Err: errors.New("empty response body")}
	}

	page := &TagGroupsPage{
		Tags: make([]TagGroup, 0, len(body.Tags)),
	}
	if body.NextCursor != nil {
		page.NextCursor = *body.NextCursor
	}
	for _, g := range body.Tags {
		tg := TagGroup{
			Tag:         g.Tag,
			HasMore:     g.HasMore,
			Assignments: make([]TagAssignment, 0, len(g.Assignments)),
		}
		for _, a := range g.Assignments {
			tg.Assignments = append(tg.Assignments, toTagAssignment(a))
		}
		page.Tags = append(page.Tags, tg)
	}
	return page, nil
}

// GetTagAssignments lists the ready assignment events for a single tag on the
// template, newest first, with keyset cursor pagination.
//
// Endpoint: GET /templates/{templateID}/tags/{tag}/assignments
// Compatibility: E2B Cloud only (not in self-hosted runtime <= 2026.30).
func (c *Client) GetTagAssignments(ctx context.Context, templateID, tag string, opts TagAssignmentsOptions) (*TagAssignmentsPage, error) {
	if tag == "" {
		return nil, &e2b.InvalidArgumentError{Message: "tag is required"}
	}
	params := &apiclient.GetTemplatesTemplateIDTagsTagAssignmentsParams{}
	if opts.Cursor != "" {
		cur := opts.Cursor
		params.Cursor = &cur
	}
	if opts.Limit > 0 {
		lim := opts.Limit
		params.Limit = &lim
	}
	resp, err := c.apiCli.GetTemplatesTemplateIDTagsTagAssignments(ctx, apiclient.TemplateID(templateID), tag, params)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "tags.assignments", templateID, "")
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsTagAssignmentsResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "tags.assignments", TemplateID: templateID, Err: err}
	}
	var body apiclient.TemplateTagAssignmentsResponse
	switch {
	case parsed.JSON200 != nil:
		body = *parsed.JSON200
	case len(parsed.Body) > 0:
		if err := json.Unmarshal(parsed.Body, &body); err != nil {
			return nil, &BuildError{Op: "tags.assignments", TemplateID: templateID, Err: err}
		}
	default:
		return nil, &BuildError{Op: "tags.assignments", TemplateID: templateID, Err: errors.New("empty response body")}
	}

	page := &TagAssignmentsPage{
		Data: make([]TagAssignment, 0, len(body.Data)),
	}
	if body.NextCursor != nil {
		page.NextCursor = *body.NextCursor
	}
	for _, a := range body.Data {
		page.Data = append(page.Data, toTagAssignment(a))
	}
	return page, nil
}

func toTagAssignment(a apiclient.TemplateTagAssignment) TagAssignment {
	return TagAssignment{
		AssignmentID:    a.AssignmentId.String(),
		BuildID:         a.BuildId.String(),
		AssignedAt:      a.AssignedAt,
		BuildCreatedAt:  a.BuildCreatedAt,
		BuildFinishedAt: a.BuildFinishedAt,
	}
}

// Delete removes a template by ID. Returns an error on non-2xx responses
// (including 404 Not Found; see Destroy for 404-tolerant deletion).
//
// Endpoint: DELETE /templates/{templateID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) Delete(ctx context.Context, templateID string) error {
	resp, err := c.doWithTokenFallback(fallbackOpDelete, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.DeleteTemplatesTemplateID(ctx, apiclient.TemplateID(templateID))
	})
	if err != nil {
		return mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeBuildAPIError(resp, "delete", templateID, "")
	}
	return nil
}

// Destroy deletes a template by ID, returning (false, nil) when the template
// was already absent (404 Not Found) and (true, nil) when deleted — matching
// upstream Template.delete and e2b.Client.Kill / volume.Client.Destroy.
//
// Endpoint: DELETE /templates/{templateID}
// Compatibility: E2B Cloud; self-hosted runtime <= 2026.30.
func (c *Client) Destroy(ctx context.Context, templateID string) (bool, error) {
	resp, err := c.doWithTokenFallback(fallbackOpDelete, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.DeleteTemplatesTemplateID(ctx, apiclient.TemplateID(templateID))
	})
	if err != nil {
		return false, mapTransportErr(err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 300 {
		return false, decodeBuildAPIError(resp, "delete", templateID, "")
	}
	return true, nil
}
