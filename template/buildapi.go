package template

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

const defaultFileUploadTimeout = time.Hour

func validatePathParam(name, val string) error {
	if val == "" || val == "." || val == ".." {
		return &e2b.InvalidArgumentError{
			Message: fmt.Sprintf("invalid %s %q: must not be empty, '.', or '..'", name, val),
		}
	}
	return nil
}

// decodeJSONFallback returns *typed when non-nil; otherwise decodes raw into T.
// When raw is empty and allowEmpty is false, it returns an "empty response body"
// error. Real json.Unmarshal errors are preserved rather than masked.
func decodeJSONFallback[T any](typed *T, raw []byte, allowEmpty bool) (T, error) {
	var zero T
	if typed != nil {
		return *typed, nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if allowEmpty {
			return zero, nil
		}
		return zero, errors.New("empty response body")
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}

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
		cc := opts.CPUCount
		body.CpuCount = &cc
	}
	if opts.MaxCPUCount != nil {
		mcc := *opts.MaxCPUCount
		body.MaxCpuCount = &mcc
	}
	if opts.MemoryMB > 0 {
		mm := opts.MemoryMB
		body.MemoryMB = &mm
	}
	if opts.MinFreeDiskMB != nil {
		disk := *opts.MinFreeDiskMB
		body.MinFreeDiskMb = &disk
	}

	resp, err := c.apiCli.PostV3Templates(ctx, body, idempotencyEditor(opts.IdempotencyKey)...)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "request", "", "")
	}
	parsed, err := apiclient.ParsePostV3TemplatesResponse(resp)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	// The generated parser only populates JSON202 for the documented 202
	// response. Accept any 2xx by decoding the raw body as a fallback.
	out, err := decodeJSONFallback(parsed.JSON202, parsed.Body, false)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	if out.TemplateID == "" {
		return nil, &BuildError{Op: "request", Err: errors.New("empty templateID in response")}
	}
	return &out, nil
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
		return nil, &BuildError{Op: "request", Err: err}
	}
	endpoint, err := url.JoinPath(c.apiCli.Server, "v2", "templates")
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	for _, edit := range c.apiCli.RequestEditors {
		if err := edit(ctx, req); err != nil {
			if _, ok := errors.AsType[e2b.Error](err); ok {
				return nil, err
			}
			return nil, &BuildError{Op: "request", Err: err}
		}
	}
	resp, err := c.apiCli.Client.Do(req)
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "request", "", "")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	out, err := decodeJSONFallback[templateLegacyResponse](nil, raw, false)
	if err != nil {
		return nil, &BuildError{Op: "request", Err: err}
	}
	if out.TemplateID == "" {
		return nil, &BuildError{Op: "request", Err: errors.New("empty templateID in response")}
	}
	return &out, nil
}

func (c *Client) getFileUploadLink(ctx context.Context, templateID, hash string) (*apiclient.TemplateBuildFileUpload, error) {
	if err := validatePathParam("templateID", templateID); err != nil {
		return nil, err
	}
	if err := validatePathParam("hash", hash); err != nil {
		return nil, err
	}
	resp, err := c.doWithTokenFallback(fallbackOpUploadLink, func(cli *apiclient.Client) (*http.Response, error) {
		return cli.GetTemplatesTemplateIDFilesHash(ctx, templateID, hash)
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, decodeUploadAPIError(resp, "", hash)
	}
	parsed, err := apiclient.ParseGetTemplatesTemplateIDFilesHashResponse(resp)
	if err != nil {
		return nil, &UploadError{Hash: hash, Err: err}
	}
	// Generated parser populates JSON201; fall back to a raw decode if the
	// server returns a different 2xx (e.g. 200).
	out, err := decodeJSONFallback(parsed.JSON201, parsed.Body, false)
	if err != nil {
		return nil, &UploadError{Hash: hash, Err: err}
	}
	return &out, nil
}

// uploadFile PUTs a tar.gz body to a pre-signed URL using the dedicated
// data-plane HTTP client (no control-plane retries or 60s timeout).
//
// When body is an *os.File (such as the spooled archive from tarFileToTemp),
// uploadFile streams directly from disk via io.SectionReader with an exact
// Content-Length (required by AWS S3 presigned PUT URLs, which reject chunked
// Transfer-Encoding with 501 NotImplemented) and retry-safe GetBody. Unknown
// streaming readers are spooled to a temporary file rather than buffered in RAM.
// Any headers returned by the control plane on TemplateBuildFileUpload.Headers
// (e.g. Azure Blob SAS x-ms-blob-type: BlockBlob) are applied to the PUT
// request, and Content-Type defaults to application/gzip when not supplied by
// headers. A 1-hour context timeout is applied only when ctx carries no
// deadline and RequestTimeoutDisabled is false.
func (c *Client) uploadFile(ctx context.Context, rawURL string, body io.Reader, headers ...map[string]string) error {
	if !c.cfg.RequestTimeoutDisabled {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultFileUploadTimeout)
			defer cancel()
		}
	}

	var (
		reqBody       io.Reader
		contentLength int64
		getBody       func() (io.ReadCloser, error)
		cleanupTemp   func()
	)
	defer func() {
		if cleanupTemp != nil {
			cleanupTemp()
		}
	}()

	switch r := body.(type) {
	case nil:
		reqBody = http.NoBody
		contentLength = 0
	case *os.File:
		info, err := r.Stat()
		if err != nil {
			return &UploadError{Err: err}
		}
		startOff, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return &UploadError{Err: err}
		}
		contentLength = max(info.Size()-startOff, 0)
		// Wrap with NopCloser(SectionReader) so net/http closing req.Body does
		// not close the caller's *os.File while GetBody may still reopen it.
		reqBody = io.NopCloser(io.NewSectionReader(r, startOff, contentLength))
		getBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(r, startOff, contentLength)), nil
		}
	case *bytes.Reader:
		reqBody = r
		contentLength = int64(r.Len())
	case *strings.Reader:
		reqBody = r
		contentLength = int64(r.Len())
	case *bytes.Buffer:
		reqBody = r
		contentLength = int64(r.Len())
	default:
		tmp, err := os.CreateTemp("", "e2b-upload-*.tmp")
		if err != nil {
			return &UploadError{Err: err}
		}
		tmpPath := tmp.Name()
		cleanupTemp = func() {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
		n, err := io.Copy(tmp, r)
		if err != nil {
			return mapTransportErr(err)
		}
		contentLength = n
		reqBody = io.NopCloser(io.NewSectionReader(tmp, 0, contentLength))
		getBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(tmp, 0, contentLength)), nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, reqBody)
	if err != nil {
		return &UploadError{Err: err}
	}
	req.ContentLength = contentLength
	if getBody != nil {
		req.GetBody = getBody
	}
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return decodeUploadAPIError(resp, "", "")
	}
	return nil
}

func (c *Client) triggerBuild(ctx context.Context, templateID, buildID string, body apiclient.TemplateBuildStartV2, idempotencyKey string) error {
	if err := validatePathParam("templateID", templateID); err != nil {
		return err
	}
	if err := validatePathParam("buildID", buildID); err != nil {
		return err
	}
	resp, err := c.apiCli.PostV2TemplatesTemplateIDBuildsBuildID(
		ctx,
		templateID,
		buildID,
		body,
		idempotencyEditor(idempotencyKey)...,
	)
	if err != nil {
		return mapTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
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
	if err := validatePathParam("templateID", templateID); err != nil {
		return nil, err
	}
	if err := validatePathParam("buildID", buildID); err != nil {
		return nil, err
	}
	if opts.LogsOffset < 0 || opts.LogsOffset > math.MaxInt32 {
		return nil, &e2b.InvalidArgumentError{
			Message: fmt.Sprintf("BuildStatusOptions.LogsOffset %d out of range [0, %d]", opts.LogsOffset, math.MaxInt32),
		}
	}
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
		return cli.GetTemplatesTemplateIDBuildsBuildIDStatus(ctx, templateID, buildID, params)
	})
	if err != nil {
		return nil, mapTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, decodeBuildAPIError(resp, "poll", templateID, buildID)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &BuildError{Op: "poll", TemplateID: templateID, BuildID: buildID, Err: err}
	}
	wire, err := decodeJSONFallback[templateBuildStatusWire](nil, raw, false)
	if err != nil {
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
	} else if len(r.Logs) > 0 {
		// Fallback to legacy plain-string logs for older self-hosted servers.
		bs.Logs = make([]LogEntry, 0, len(r.Logs))
		for _, line := range r.Logs {
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
