package e2b

import (
	"context"
	"fmt"
	"maps"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// Log level constants for SandboxLogsOptions.Level.
const (
	SandboxLogLevelDebug = "debug"
	SandboxLogLevelInfo  = "info"
	SandboxLogLevelWarn  = "warn"
	SandboxLogLevelError = "error"
)

// Log direction constants for SandboxLogsOptions.Direction.
const (
	LogsDirectionForward  = "forward"
	LogsDirectionBackward = "backward"
)

// SandboxLogEntry is a structured sandbox log record.
type SandboxLogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
	Fields    map[string]string
}

// SandboxLogLine is a plain-text timestamped log line returned by GetLogsV1.
type SandboxLogLine struct {
	Timestamp time.Time
	Line      string
}

// SandboxLogsV1 holds both plain-text and structured logs returned by legacy
// GET /sandboxes/{sandboxID}/logs.
type SandboxLogsV1 struct {
	Logs       []SandboxLogLine
	LogEntries []SandboxLogEntry
}

// SandboxLogsOptions filters structured sandbox logs on GetLogs
// (GET /v2/sandboxes/{sandboxID}/logs).
type SandboxLogsOptions struct {
	// Cursor is the starting timestamp in Unix milliseconds (nil omits cursor).
	Cursor *int64
	// Limit caps the maximum number of log entries returned (<= 0 uses server default).
	Limit int32
	// Direction is "forward" or "backward".
	Direction string
	// Level is the minimum log level ("debug", "info", "warn", "error").
	Level string
	// Search filters log messages by case-sensitive substring match.
	Search string
}

// SandboxLogsV1Options filters legacy sandbox logs on GetLogsV1
// (GET /sandboxes/{sandboxID}/logs).
type SandboxLogsV1Options struct {
	// Start is the starting timestamp in Unix milliseconds (nil omits start).
	Start *int64
	// Limit caps the maximum number of log entries returned (<= 0 uses server default).
	Limit int32
}

// GetLogs fetches structured logs for a sandbox via the v2 logs endpoint. It
// does not fall back to GetLogsV1 on older self-hosted servers.
//
// Endpoint: GET /v2/sandboxes/{sandboxID}/logs
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (use GetLogsV1 on < 2026.07).
func (c *Client) GetLogs(ctx context.Context, sandboxID string, opts ...SandboxLogsOptions) ([]SandboxLogEntry, error) {
	return fetchSandboxLogsV2(ctx, c.apiCli, sandboxID, opts...)
}

// GetLogs fetches structured logs for this sandbox via the v2 logs endpoint. It
// does not fall back to GetLogsV1 on older self-hosted servers.
//
// Endpoint: GET /v2/sandboxes/{sandboxID}/logs
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (use GetLogsV1 on < 2026.07).
func (s *Sandbox) GetLogs(ctx context.Context, opts ...SandboxLogsOptions) ([]SandboxLogEntry, error) {
	return fetchSandboxLogsV2(ctx, s.apiCli, s.ID, opts...)
}

// GetLogsV1 fetches sandbox logs via GET /sandboxes/{sandboxID}/logs, returning
// both plain-text lines and structured entries when provided by the server.
//
// Soft-deprecated in OpenAPI in favor of GetLogs (GET /v2/sandboxes/{sandboxID}/logs,
// added in runtime 2026.07 with cursor/direction/level/search filtering), this
// endpoint remains actively served (not retired) on both E2B Cloud and all
// self-hosted runtime <= 2026.30 deployments. Choose GetLogsV1 on self-hosted
// runtime < 2026.07 (where /v2/sandboxes/{sandboxID}/logs does not exist) or
// when plain-text Logs lines are needed; prefer GetLogs on E2B Cloud and
// runtime >= 2026.07.
//
// Endpoint: GET /sandboxes/{sandboxID}/logs
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2026.07; soft-deprecated in OpenAPI in favor of GetLogs).
func (c *Client) GetLogsV1(ctx context.Context, sandboxID string, opts ...SandboxLogsV1Options) (*SandboxLogsV1, error) {
	return fetchSandboxLogsV1(ctx, c.apiCli, sandboxID, opts...)
}

// GetLogsV1 fetches logs for this sandbox via GET /sandboxes/{sandboxID}/logs.
// Soft-deprecated in OpenAPI in favor of GetLogs, it remains actively served
// (not retired) on E2B Cloud and all self-hosted runtime <= 2026.30, and is
// required on self-hosted runtime < 2026.07 (see Client.GetLogsV1).
//
// Endpoint: GET /sandboxes/{sandboxID}/logs
// Compatibility: E2B Cloud and self-hosted runtime <= 2026.30 (actively served; required on < 2026.07; soft-deprecated in OpenAPI in favor of GetLogs).
func (s *Sandbox) GetLogsV1(ctx context.Context, opts ...SandboxLogsV1Options) (*SandboxLogsV1, error) {
	return fetchSandboxLogsV1(ctx, s.apiCli, s.ID, opts...)
}

func fetchSandboxLogsV2(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ...SandboxLogsOptions) ([]SandboxLogEntry, error) {
	var params *apiclient.GetV2SandboxesSandboxIDLogsParams
	if len(opts) > 0 {
		o := opts[0]
		if o.Direction != "" && o.Direction != LogsDirectionForward && o.Direction != LogsDirectionBackward {
			return nil, &InvalidArgumentError{Message: fmt.Sprintf("logs direction must be %q or %q, got %q", LogsDirectionForward, LogsDirectionBackward, o.Direction)}
		}
		if o.Level != "" && o.Level != SandboxLogLevelDebug && o.Level != SandboxLogLevelInfo && o.Level != SandboxLogLevelWarn && o.Level != SandboxLogLevelError {
			return nil, &InvalidArgumentError{Message: fmt.Sprintf("logs level must be debug, info, warn, or error, got %q", o.Level)}
		}
		params = &apiclient.GetV2SandboxesSandboxIDLogsParams{}
		if o.Cursor != nil {
			cur := *o.Cursor
			params.Cursor = &cur
		}
		if o.Limit > 0 {
			lim := o.Limit
			params.Limit = &lim
		}
		if o.Direction != "" {
			dir := apiclient.LogsDirection(o.Direction)
			params.Direction = &dir
		}
		if o.Level != "" {
			lvl := apiclient.LogLevel(o.Level)
			params.Level = &lvl
		}
		params.Search = nilIfEmpty(o.Search)
	}

	resp, err := apiCli.GetV2SandboxesSandboxIDLogs(ctx, sandboxID, params)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetV2SandboxesSandboxIDLogsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse v2 logs response", err)
	}
	if parsed.JSON200 == nil {
		return nil, nil
	}
	return convertLogEntries(parsed.JSON200.Logs), nil
}

func fetchSandboxLogsV1(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts ...SandboxLogsV1Options) (*SandboxLogsV1, error) {
	var params *apiclient.GetSandboxesSandboxIDLogsParams
	if len(opts) > 0 {
		o := opts[0]
		params = &apiclient.GetSandboxesSandboxIDLogsParams{}
		if o.Start != nil {
			st := *o.Start
			params.Start = &st
		}
		if o.Limit > 0 {
			lim := o.Limit
			params.Limit = &lim
		}
	}

	resp, err := apiCli.GetSandboxesSandboxIDLogs(ctx, sandboxID, params) //nolint:staticcheck // v1 GET /sandboxes/{sandboxID}/logs is preserved for self-hosted runtime < 2026.07.
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	if err := mapHTTPErr(resp, sandboxID); err != nil {
		return nil, err
	}
	parsed, err := apiclient.ParseGetSandboxesSandboxIDLogsResponse(resp)
	if err != nil {
		return nil, newSandboxError("parse v1 logs response", err)
	}
	if parsed.JSON200 == nil {
		return &SandboxLogsV1{}, nil
	}

	out := &SandboxLogsV1{
		Logs:       make([]SandboxLogLine, 0, len(parsed.JSON200.Logs)),
		LogEntries: convertLogEntries(parsed.JSON200.LogEntries),
	}
	for _, l := range parsed.JSON200.Logs {
		out.Logs = append(out.Logs, SandboxLogLine{
			Timestamp: l.Timestamp,
			Line:      l.Line,
		})
	}
	return out, nil
}

func convertLogEntries(entries []apiclient.SandboxLogEntry) []SandboxLogEntry {
	out := make([]SandboxLogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, SandboxLogEntry{
			Timestamp: e.Timestamp,
			Level:     string(e.Level),
			Message:   e.Message,
			Fields:    maps.Clone(e.Fields),
		})
	}
	return out
}
