package template

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// Exists returns true if a template with the given alias exists and the
// caller can see it (200 or 403). 404 means it does not exist; other
// non-2xx status codes are returned as errors.
func (c *Client) Exists(ctx context.Context, alias string) (bool, error) {
	resp, err := c.apiCli.GetTemplatesAliasesAlias(ctx, alias)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		return true, nil
	}
	if resp.StatusCode >= 300 {
		return false, &BuildError{Op: "exists", Err: errors.New(resp.Status)}
	}
	return true, nil
}

// AssignTags assigns one or more tags to an existing template build.
func (c *Client) AssignTags(ctx context.Context, target string, tags []string) (*TagInfo, error) {
	body := apiclient.AssignTemplateTagsRequest{Target: target, Tags: tags}
	resp, err := c.apiCli.PostTemplatesTags(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "tags.assign", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePostTemplatesTagsResponse(resp)
	if err != nil {
		return nil, err
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
func (c *Client) RemoveTags(ctx context.Context, name string, tags []string) error {
	body := apiclient.DeleteTemplateTagsRequest{Name: name, Tags: tags}
	resp, err := c.apiCli.DeleteTemplatesTags(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "tags.remove", Err: errors.New(resp.Status)}
	}
	return nil
}

// GetTags returns every tag associated with a template.
func (c *Client) GetTags(ctx context.Context, templateID string) ([]TemplateTag, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDTags(ctx, apiclient.TemplateID(templateID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "tags.get", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDTagsResponse(resp)
	if err != nil {
		return nil, err
	}
	var rows []apiclient.TemplateTag
	if parsed.JSON200 != nil {
		rows = *parsed.JSON200
	} else if len(parsed.Body) > 0 {
		// Raw-body fallback in case the response Content-Type or status code
		// did not match the generated parser's expectations.
		if err := json.Unmarshal(parsed.Body, &rows); err != nil {
			return nil, &BuildError{Op: "tags.get", Err: err}
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

// Delete removes a template by ID.
func (c *Client) Delete(ctx context.Context, templateID string) error {
	resp, err := c.apiCli.DeleteTemplatesTemplateID(ctx, apiclient.TemplateID(templateID))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "delete", TemplateID: templateID, Err: errors.New(resp.Status)}
	}
	return nil
}
