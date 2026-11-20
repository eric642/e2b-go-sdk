package template

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

const defaultFileUploadTimeout = time.Hour

func idempotencyEditor(key string) []apiclient.RequestEditorFn {
	if key == "" {
		return nil
	}
	return []apiclient.RequestEditorFn{
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Idempotency-Key", key)
			return nil
		},
	}
}

func (c *Client) requestBuildWithOptions(ctx context.Context, opts BuildOptions) (*apiclient.TemplateRequestResponseV3, error) {
	name := opts.Name
	body := apiclient.TemplateBuildRequestV3{Name: &name}
	if len(opts.Tags) > 0 {
		t := append([]string(nil), opts.Tags...)
		body.Tags = &t
	}
	if opts.CPUCount > 0 {
		cc := apiclient.CPUCount(opts.CPUCount)
		body.CpuCount = &cc
	}
	if opts.MaxCPUCount != nil {
		mcc := apiclient.CPUCount(*opts.MaxCPUCount)
		body.MaxCpuCount = &mcc
	}
	if opts.MemoryMB > 0 {
		mm := apiclient.MemoryMB(opts.MemoryMB)
		body.MemoryMB = &mm
	}
	if opts.MinFreeDiskMB != nil {
		disk := apiclient.MinFreeDiskMb(*opts.MinFreeDiskMB)
		body.MinFreeDiskMb = &disk
	}

	resp, err := c.apiCli.PostV3Templates(ctx, body, idempotencyEditor(opts.IdempotencyKey)...)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "request", "", "")
	}
	parsed, err := apiclient.ParsePostV3TemplatesResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
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

// templateBuildRequestV2 is the POST /v2/templates request body. Upstream
// dropped this legacy route (and its schemas) from openapi.yml on E2B Cloud,
// so it is hand-written here to keep BuildV2* working against older
// self-hosted deployments without patching the vendored spec.
type templateBuildRequestV2 struct {
	Alias    string `json:"alias"`
	CPUCount *int32 `json:"cpuCount,omitempty"`
	MemoryMB *int32 `json:"memoryMB,omitempty"`
}

type templateLegacyResponse struct {
	TemplateID string `json:"templateID"`
	BuildID    string `json:"buildID"`
}

func (c *Client) requestBuildV2(ctx context.Context, alias string, cpu, mem int32) (*templateLegacyResponse, error) {
	body := templateBuildRequestV2{Alias: alias}
	if cpu > 0 {
		body.CPUCount = &cpu
	}
	if mem > 0 {
		body.MemoryMB = &mem
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.JoinPath(c.apiCli.Server, "v2", "templates")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, edit := range c.apiCli.RequestEditors {
		if err := edit(ctx, req); err != nil {
			return nil, err
		}
	}
	resp, err := c.apiCli.Client.Do(req)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "request", "", "")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	if len(raw) > 0 {
		var out templateLegacyResponse
		if err := json.Unmarshal(raw, &out); err == nil && out.TemplateID != "" {
			return &out, nil
		}
	}
	return nil, &BuildError{Op: "request", Err: errors.New("empty response body")}
}

func (c *Client) getFileUploadLink(ctx context.Context, templateID, hash string) (*apiclient.TemplateBuildFileUpload, error) {
	resp, err := c.doWithTokenFallback(fallbackOpUploadLink, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.GetTemplatesTemplateIDFilesHash(ctx, apiclient.TemplateID(templateID), hash)
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeUploadAPIError(resp, "", hash)
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDFilesHashResponse(resp)
	if err != nil {
		return nil, &UploadError{Hash: hash, Err: err}
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

// uploadFile PUTs a tar.gz body to a pre-signed URL using the dedicated
// data-plane HTTP client (no control-plane retries or 60s timeout).
//
// The body is buffered into a *bytes.Reader if not already one so
// http.NewRequestWithContext sets an exact Content-Length (required by AWS S3
// presigned PUT URLs, which reject chunked Transfer-Encoding with 501
// NotImplemented) and GetBody. Any headers returned by the control plane on
// TemplateBuildFileUpload.Headers (e.g. Azure Blob SAS x-ms-blob-type:
// BlockBlob) are applied to the PUT request, and Content-Type defaults to
// application/gzip when not supplied by headers. A 1-hour context timeout
// bounds the upload when ctx carries no shorter deadline.
func (c *Client) uploadFile(ctx context.Context, rawURL string, body io.Reader, headers ...map[string]string) error {
	if !c.cfg.RequestTimeoutDisabled {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultFileUploadTimeout)
		defer cancel()
	}

	var reader *bytes.Reader
	if br, ok := body.(*bytes.Reader); ok {
		reader = br
	} else {
		var buf bytes.Buffer
		if body != nil {
			if _, err := io.Copy(&buf, body); err != nil {
				return mapTransportErr(err)
			}
		}
		reader = bytes.NewReader(buf.Bytes())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, reader)
	if err != nil {
		return err
	}
	req.ContentLength = int64(reader.Len())
	for _, hdrs := range headers {
		for k, v := range hdrs {
			if strings.EqualFold(k, "Content-Length") {
				continue
			}
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/gzip")
	}

	resp, err := c.uploadCli.Do(req)
	if err != nil {
		return mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeUploadAPIError(resp, "", "")
	}
	return nil
}

func (c *Client) triggerBuild(ctx context.Context, templateID, buildID string, body apiclient.TemplateBuildStartV2, idempotencyKey string) error {
	resp, err := c.apiCli.PostV2TemplatesTemplateIDBuildsBuildID(
		ctx,
		apiclient.TemplateID(templateID),
		apiclient.BuildID(buildID),
		body,
		idempotencyEditor(idempotencyKey)...,
	)
	if err != nil {
		return mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeBuildAPIError(resp, "trigger", templateID, buildID)
	}
	return nil
}

// templateBuildStatusWire decodes GET /templates/{id}/builds/{buildID}/status
// while accepting both structured reason objects (runtime >= 2025.44) and
// legacy plain-string reason values (runtime < 2025.44).
type templateBuildStatusWire struct {
	TemplateID string                        `json:"templateID"`
	BuildID    string                        `json:"buildID"`
	Status     apiclient.TemplateBuildStatus `json:"status"`
	LogEntries []apiclient.BuildLogEntry     `json:"logEntries"`
	Logs       []string                      `json:"logs"`
	Reason     json.RawMessage               `json:"reason,omitempty"`
}

func (c *Client) getBuildStatus(ctx context.Context, templateID, buildID string, logsOffset int) (*BuildStatus, error) {
	return c.getBuildStatusWithOptions(ctx, templateID, buildID, BuildStatusOptions{LogsOffset: logsOffset})
}

func (c *Client) getBuildStatusWithOptions(ctx context.Context, templateID, buildID string, opts BuildStatusOptions) (*BuildStatus, error) {
	params := &apiclient.GetTemplatesTemplateIDBuildsBuildIDStatusParams{}
	if opts.LogsOffset > 0 {
		lo := int32(opts.LogsOffset)
		params.LogsOffset = &lo
	}
	if opts.Limit > 0 {
		lim := opts.Limit
		params.Limit = &lim
	}
	if opts.Level != "" {
		lvl := apiclient.LogLevel(opts.Level)
		params.Level = &lvl
	}
	resp, err := c.doWithTokenFallback(fallbackOpBuildStatus, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.GetTemplatesTemplateIDBuildsBuildIDStatus(ctx, apiclient.TemplateID(templateID), apiclient.BuildID(buildID), params)
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "poll", templateID, buildID)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: err}
	}
	if len(raw) == 0 {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: errors.New("empty response body")}
	}
	var wire templateBuildStatusWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: err}
	}
	info := apiclient.TemplateBuildInfo{
		TemplateID: wire.TemplateID,
		BuildID:    wire.BuildID,
		Status:     wire.Status,
		LogEntries: wire.LogEntries,
		Logs:       wire.Logs,
	}
	trimmed := bytes.TrimSpace(wire.Reason)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if trimmed[0] == '"' {
			var msg string
			if err := json.Unmarshal(trimmed, &msg); err == nil && msg != "" {
				info.Reason = &apiclient.BuildStatusReason{Message: msg}
			}
		} else {
			var r apiclient.BuildStatusReason
			if err := json.Unmarshal(trimmed, &r); err == nil {
				info.Reason = &r
			}
		}
	}
	return mapBuildStatus(&info), nil
}

// mapBuildStatus translates the generated apiclient.TemplateBuildInfo into
// our public BuildStatus type. When structured LogEntries are absent (older
// self-hosted servers returning plain logs: []string), it synthesizes Info-level
// LogEntry records from r.Logs so streaming and OnBuildLogs callbacks work
// seamlessly across server generations.
func mapBuildStatus(r *apiclient.TemplateBuildInfo) *BuildStatus {
	if r == nil {
		return nil
	}
	bs := &BuildStatus{
		TemplateID: r.TemplateID,
		BuildID:    r.BuildID,
		Status:     BuildStatusValue(r.Status),
	}
	if len(r.LogEntries) > 0 {
		bs.Logs = make([]LogEntry, 0, len(r.LogEntries))
		for _, le := range r.LogEntries {
			bs.Logs = append(bs.Logs, toLogEntry(le))
		}
	} else if len(r.Logs) > 0 { //nolint:staticcheck // intentional fallback to deprecated r.Logs for legacy self-hosted servers.
		bs.Logs = make([]LogEntry, 0, len(r.Logs)) //nolint:staticcheck // legacy plain-string logs fallback.
		for _, line := range r.Logs {              //nolint:staticcheck // legacy plain-string logs fallback.
			bs.Logs = append(bs.Logs, LogEntry{
				Level:   LogLevelInfo,
				Message: line,
			})
		}
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

func toLogEntry(le apiclient.BuildLogEntry) LogEntry {
	return LogEntry{
		Timestamp: le.Timestamp,
		Level:     LogLevel(le.Level),
		Message:   le.Message,
	}
}
