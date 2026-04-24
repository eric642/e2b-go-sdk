package e2b

import (
	"errors"
	"fmt"
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

// baseError is embedded by every concrete error type so they all satisfy
// Error automatically.
type baseError struct{}

func (baseError) sandboxError() {}

// SandboxError wraps arbitrary sandbox/API failures that don't have a more
// specific type.
type SandboxError struct {
	baseError
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
	baseError
	Message string
	Cause   error
}

func (e *TimeoutError) Error() string   { return "e2b: timeout: " + e.Message }
func (e *TimeoutError) Unwrap() error   { return e.Cause }
func (e *TimeoutError) Timeout() bool   { return true }
func (e *TimeoutError) Temporary() bool { return true }

// InvalidArgumentError reports an invalid parameter.
type InvalidArgumentError struct {
	baseError
	Message string
}

func (e *InvalidArgumentError) Error() string { return "e2b: invalid argument: " + e.Message }

// NotEnoughSpaceError reports the sandbox has insufficient disk space.
type NotEnoughSpaceError struct {
	baseError
	Message string
}

func (e *NotEnoughSpaceError) Error() string { return "e2b: not enough space: " + e.Message }

// NotFoundError is deprecated. Prefer FileNotFoundError or SandboxNotFoundError.
type NotFoundError struct {
	baseError
	Message string
}

func (e *NotFoundError) Error() string { return "e2b: not found: " + e.Message }

// FileNotFoundError reports a missing file/directory in the sandbox.
type FileNotFoundError struct {
	baseError
	Path    string
	Message string
}

func (e *FileNotFoundError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("e2b: file not found: %s: %s", e.Path, e.Message)
	}
	return "e2b: file not found: " + e.Message
}

// SandboxNotFoundError reports that the sandbox is gone or never existed.
type SandboxNotFoundError struct {
	baseError
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
	baseError
	Message string
}

func (e *AuthenticationError) Error() string { return "e2b: authentication failed: " + e.Message }

// GitAuthError specializes AuthenticationError for Git credential failures.
type GitAuthError struct {
	baseError
	Message string
}

func (e *GitAuthError) Error() string { return "e2b: git authentication failed: " + e.Message }

// GitUpstreamError reports a missing upstream branch.
type GitUpstreamError struct {
	baseError
	Message string
}

func (e *GitUpstreamError) Error() string { return "e2b: git upstream error: " + e.Message }

// TemplateError reports template/envd incompatibility.
type TemplateError struct {
	baseError
	Message string
}

func (e *TemplateError) Error() string { return "e2b: template error: " + e.Message }

// RateLimitError reports HTTP 429.
type RateLimitError struct {
	baseError
	Message string
}

func (e *RateLimitError) Error() string { return "e2b: rate limit: " + e.Message }

// BuildError reports a template build failure.
type BuildError struct {
	baseError
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
	baseError
	Path    string
	Message string
	Cause   error
}

func (e *FileUploadError) Error() string {
	return fmt.Sprintf("e2b: file upload error: %s: %s", e.Path, e.Message)
}
func (e *FileUploadError) Unwrap() error { return e.Cause }

// VolumeError reports a volume operation failure.
type VolumeError struct {
	baseError
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
	baseError
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
