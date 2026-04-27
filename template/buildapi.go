package template

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// requestBuild calls POST /v3/templates.
func (c *Client) requestBuild(ctx context.Context, name string, tags []string, cpu, mem int32) (*apiclient.TemplateRequestResponseV3, error) {
	body := apiclient.TemplateBuildRequestV3{Name: &name}
	if len(tags) > 0 {
		t := tags
		body.Tags = &t
	}
	if cpu > 0 {
		cc := apiclient.CPUCount(cpu)
		body.CpuCount = &cc
	}
	if mem > 0 {
		mm := apiclient.MemoryMB(mem)
		body.MemoryMB = &mm
	}
	resp, err := c.apiCli.PostV3Templates(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "request", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePostV3TemplatesResponse(resp)
	if err != nil {
		return nil, err
	}
	// The generated parser only populates JSON202 for the documented 202
	// response. Accept any 2xx by decoding the raw body as a fallback.
	if parsed.JSON202 != nil {
		return parsed.JSON202, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateRequestResponseV3
		if err := json.Unmarshal(parsed.Body, &out); err == nil && out.TemplateID != "" {
			return &out, nil
		}
	}
	return nil, &BuildError{Op: "request", Err: errors.New("empty response body")}
}

// requestBuildV2 calls POST /v2/templates. Compared to v3 the payload only
// carries alias/cpuCount/memoryMB — tags are not supported on this endpoint.
func (c *Client) requestBuildV2(ctx context.Context, alias string, cpu, mem int32) (*apiclient.TemplateLegacy, error) {
	body := apiclient.TemplateBuildRequestV2{Alias: alias}
	if cpu > 0 {
		cc := apiclient.CPUCount(cpu)
		body.CpuCount = &cc
	}
	if mem > 0 {
		mm := apiclient.MemoryMB(mem)
		body.MemoryMB = &mm
	}
	resp, err := c.apiCli.PostV2Templates(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "request", Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParsePostV2TemplatesResponse(resp)
	if err != nil {
		return nil, err
	}
	if parsed.JSON202 != nil {
		return parsed.JSON202, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateLegacy
		if err := json.Unmarshal(parsed.Body, &out); err == nil && out.TemplateID != "" {
			return &out, nil
		}
	}
	return nil, &BuildError{Op: "request", Err: errors.New("empty response body")}
}

// getFileUploadLink calls GET /templates/{id}/files/{hash}.
func (c *Client) getFileUploadLink(ctx context.Context, templateID, hash string) (*apiclient.TemplateBuildFileUpload, error) {
	resp, err := c.apiCli.GetTemplatesTemplateIDFilesHash(ctx, apiclient.TemplateID(templateID), hash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &UploadError{Hash: hash, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDFilesHashResponse(resp)
	if err != nil {
		return nil, err
	}
	// Generated parser populates JSON201; fall back to a raw decode if the
	// server returns a different 2xx (e.g. 200).
	if parsed.JSON201 != nil {
		return parsed.JSON201, nil
	}
	if len(parsed.Body) > 0 {
		var out apiclient.TemplateBuildFileUpload
		if err := json.Unmarshal(parsed.Body, &out); err == nil {
			return &out, nil
		}
	}
	return nil, &UploadError{Hash: hash, Err: errors.New("empty response body")}
}

// uploadFile PUTs a tar.gz body to a pre-signed URL (not via the API client).
func (c *Client) uploadFile(ctx context.Context, url string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &UploadError{Err: errors.New(resp.Status)}
	}
	return nil
}

// triggerBuild calls POST /v2/templates/{id}/builds/{buildID}.
func (c *Client) triggerBuild(ctx context.Context, templateID, buildID string, body apiclient.TemplateBuildStartV2) error {
	resp, err := c.apiCli.PostV2TemplatesTemplateIDBuildsBuildID(ctx, apiclient.TemplateID(templateID), apiclient.BuildID(buildID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &BuildError{Op: "trigger", TemplateID: templateID, BuildID: buildID, Err: errors.New(resp.Status)}
	}
	return nil
}

// getBuildStatus calls GET /templates/{id}/builds/{buildID}/status and maps
// the parsed body to our exported BuildStatus type.
func (c *Client) getBuildStatus(ctx context.Context, templateID, buildID string, logsOffset int) (*BuildStatus, error) {
	params := &apiclient.GetTemplatesTemplateIDBuildsBuildIDStatusParams{}
	if logsOffset > 0 {
		lo := int32(logsOffset)
		params.LogsOffset = &lo
	}
	resp, err := c.apiCli.GetTemplatesTemplateIDBuildsBuildIDStatus(ctx, apiclient.TemplateID(templateID), apiclient.BuildID(buildID), params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: errors.New(resp.Status)}
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDBuildsBuildIDStatusResponse(resp)
	if err != nil {
		return nil, err
	}
	return mapBuildStatus(parsed.JSON200), nil
}

// mapBuildStatus translates the generated apiclient.TemplateBuildInfo into
// our public BuildStatus type.
func mapBuildStatus(r *apiclient.TemplateBuildInfo) *BuildStatus {
	if r == nil {
		return nil
	}
	bs := &BuildStatus{
		TemplateID: r.TemplateID,
		BuildID:    r.BuildID,
		Status:     BuildStatusValue(r.Status),
	}
	for _, le := range r.LogEntries {
		bs.Logs = append(bs.Logs, toLogEntry(le))
	}
	if r.Reason != nil {
		br := &BuildReason{Message: r.Reason.Message}
		if r.Reason.Step != nil {
			br.Step = *r.Reason.Step
		}
		if r.Reason.LogEntries != nil {
			for _, le := range *r.Reason.LogEntries {
				br.Logs = append(br.Logs, toLogEntry(le))
			}
		}
		bs.Reason = br
	}
	return bs
}

// toLogEntry translates the generated apiclient.BuildLogEntry into our
// public LogEntry domain type.
func toLogEntry(le apiclient.BuildLogEntry) LogEntry {
	return LogEntry{
		Timestamp: le.Timestamp,
		Level:     LogLevel(le.Level),
		Message:   le.Message,
	}
}
