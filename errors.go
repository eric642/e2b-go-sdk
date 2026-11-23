package e2b

import (
	"errors"
	"fmt"
	"strings"
)

// Error is the base interface implemented by every SDK-specific error.
// It lets callers switch on a single interface via errors.As:
//
//	var sErr e2b.Error
//	if errors.As(err, &sErr) { ... }
type Error interface {
	error
	sandboxError()
}

// BaseError is embedded by every concrete SDK error type so they all satisfy
// Error automatically. Subpackages (template, volume) embed it too so their
// error types satisfy e2b.Error.
//
// For errors decoded from a control-plane HTTP response, StatusCode carries
// the HTTP status and Code the machine-readable `error_code` from the JSON
// error body (e.g. "sandbox_capacity_unavailable"); both are zero otherwise.
type BaseError struct {
	// StatusCode is the HTTP status of the response this error was decoded
	// from, or 0.
	StatusCode int
	// Code is the server's machine-readable error code (the OpenAPI
	// Error.error_code field), or "".
	Code string
}

func (BaseError) sandboxError() {}

// ErrorCode returns the server's machine-readable error code, if any.
func (e BaseError) ErrorCode() string { return e.Code }

// HTTPStatus returns the HTTP status code the error was decoded from, or 0.
func (e BaseError) HTTPStatus() int { return e.StatusCode }

// SandboxError wraps arbitrary sandbox/API failures that don't have a more
// specific type.
type SandboxError struct {
	BaseError
	Message string
	Cause   error
}

func (e *SandboxError) Error() string {
	if e.Cause != nil {
		return "e2b: " + e.Message + ": " + e.Cause.Error()
	}
	return "e2b: " + e.Message
}
func (e *SandboxError) Unwrap() error { return e.Cause }

func newSandboxError(msg string, cause error) *SandboxError {
	return &SandboxError{Message: msg, Cause: cause}
}

// TimeoutError is returned when a request or sandbox operation times out.
// Mirrors Python's TimeoutException / JS TimeoutError.
type TimeoutError struct {
	BaseError
	Message string
	Cause   error
}

func (e *TimeoutError) Error() string {
	switch {
	case e.Message != "" && e.Cause != nil && !strings.Contains(e.Cause.Error(), e.Message):
		return "e2b: timeout: " + e.Message + ": " + e.Cause.Error()
	case e.Message != "":
		return "e2b: timeout: " + e.Message
	case e.Cause != nil:
		return "e2b: timeout: " + e.Cause.Error()
	default:
		return "e2b: timeout"
	}
}
func (e *TimeoutError) Unwrap() error   { return e.Cause }
func (e *TimeoutError) Timeout() bool   { return true }
func (e *TimeoutError) Temporary() bool { return true }

// InvalidArgumentError reports an invalid parameter.
type InvalidArgumentError struct {
	BaseError
	Message string
	Cause   error
}

func (e *InvalidArgumentError) Error() string { return "e2b: invalid argument: " + e.Message }
func (e *InvalidArgumentError) Unwrap() error { return e.Cause }

// NotEnoughSpaceError reports the sandbox has insufficient disk space.
type NotEnoughSpaceError struct {
	BaseError
	Message string
	Cause   error
}

func (e *NotEnoughSpaceError) Error() string { return "e2b: not enough space: " + e.Message }
func (e *NotEnoughSpaceError) Unwrap() error { return e.Cause }

// NotFoundError reports a generic HTTP 404 when no resource-specific error type
// applies. Prefer FileNotFoundError or SandboxNotFoundError for filesystem and
// sandbox lookups.
type NotFoundError struct {
	BaseError
	Message string
}

func (e *NotFoundError) Error() string { return "e2b: not found: " + e.Message }

// FileNotFoundError reports a missing file/directory in the sandbox.
type FileNotFoundError struct {
	BaseError
	Path    string
	Message string
	Cause   error
}

func (e *FileNotFoundError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("e2b: file not found: %s: %s", e.Path, e.Message)
	}
	return "e2b: file not found: " + e.Message
}
func (e *FileNotFoundError) Unwrap() error { return e.Cause }

// SandboxNotFoundError reports that the sandbox is gone or never existed.
type SandboxNotFoundError struct {
	BaseError
	SandboxID string
	Message   string
}

func (e *SandboxNotFoundError) Error() string {
	if e.SandboxID != "" {
		return fmt.Sprintf("e2b: sandbox %q not found: %s", e.SandboxID, e.Message)
	}
	return "e2b: sandbox not found: " + e.Message
}

// AuthenticationError reports invalid or missing credentials.
type AuthenticationError struct {
	BaseError
	Message string
	Cause   error
}

func (e *AuthenticationError) Error() string { return "e2b: authentication failed: " + e.Message }
func (e *AuthenticationError) Unwrap() error { return e.Cause }

// GitAuthError specializes AuthenticationError for Git credential failures.
type GitAuthError struct {
	BaseError
	Message string
}

func (e *GitAuthError) Error() string { return "e2b: git authentication failed: " + e.Message }

// GitUpstreamError reports a missing upstream branch.
type GitUpstreamError struct {
	BaseError
	Message string
}

func (e *GitUpstreamError) Error() string { return "e2b: git upstream error: " + e.Message }

// TemplateError reports template/envd incompatibility.
type TemplateError struct {
	BaseError
	Message string
}

func (e *TemplateError) Error() string { return "e2b: template error: " + e.Message }

// RateLimitError reports HTTP 429.
type RateLimitError struct {
	BaseError
	Message string
	Cause   error
}

func (e *RateLimitError) Error() string { return "e2b: rate limit: " + e.Message }
func (e *RateLimitError) Unwrap() error { return e.Cause }

// ConflictError reports HTTP 409 from the control plane (e.g. a resource that
// already exists, an operation incompatible with the current state, or an
// idempotency-key conflict; see Code).
type ConflictError struct {
	BaseError
	Message string
}

func (e *ConflictError) Error() string { return "e2b: conflict: " + e.Message }

// BuildError reports a template build failure.
type BuildError struct {
	BaseError
	Message string
	Cause   error
}

func (e *BuildError) Error() string {
	if e.Cause != nil {
		return "e2b: build error: " + e.Message + ": " + e.Cause.Error()
	}
	return "e2b: build error: " + e.Message
}
func (e *BuildError) Unwrap() error { return e.Cause }

// FileUploadError reports a file upload failure during template build.
type FileUploadError struct {
	BaseError
	Path    string
	Message string
	Cause   error
}

func (e *FileUploadError) Error() string {
	var b strings.Builder
	b.WriteString("e2b: file upload error")
	if e.Path != "" {
		b.WriteString(": ")
		b.WriteString(e.Path)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Cause != nil && e.Cause.Error() != e.Message {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}
func (e *FileUploadError) Unwrap() error { return e.Cause }

// VolumeError reports a volume operation failure.
type VolumeError struct {
	BaseError
	Message string
	Cause   error
}

func (e *VolumeError) Error() string {
	if e.Cause != nil {
		return "e2b: volume error: " + e.Message + ": " + e.Cause.Error()
	}
	return "e2b: volume error: " + e.Message
}
func (e *VolumeError) Unwrap() error { return e.Cause }

// CommandResult reports the outcome of a finished command.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int32
	Error    string
}

// CommandExitError is returned by CommandHandle.Wait when a command exits
// non-zero. It embeds CommandResult so callers can inspect output.
type CommandExitError struct {
	BaseError
	Result CommandResult
}

func (e *CommandExitError) Error() string {
	return fmt.Sprintf("e2b: command exited with code %d: %s", e.Result.ExitCode, e.Result.Error)
}

// ErrNotImplemented is returned by stubbed features pending future work.
var ErrNotImplemented = errors.New("e2b: not implemented")

var (
	ErrTemplateBuild  = errors.New("template build failed")
	ErrTemplateUpload = errors.New("template file upload failed")
	ErrTemplate       = errors.New("template error")
)
