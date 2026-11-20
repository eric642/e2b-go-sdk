package template

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	e2b "github.com/eric642/e2b-go-sdk"
)

var (
	_ e2b.Error = (*BuildError)(nil)
	_ e2b.Error = (*UploadError)(nil)
)

// BuildError describes a failure during a server-side template build or
// template management API operation.
//
// BuildError embeds e2b.BaseError so it satisfies e2b.Error and exposes
// HTTPStatus() / ErrorCode() when decoded from an API HTTP response. Unwrap()
// exposes both e2b.ErrTemplateBuild and the underlying error (if any) so
// errors.Is and errors.As inspect both sentinels and typed API errors.
type BuildError struct {
	e2b.BaseError
	Op         string
	TemplateID string
	BuildID    string
	Step       string
	Message    string
	LogTail    []LogEntry
	Err        error
}

func (e *BuildError) Error() string {
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("template build %s: templateID=%s buildID=%s step=%s: %s",
		e.Op, e.TemplateID, e.BuildID, e.Step, msg)
}

func (e *BuildError) Unwrap() []error {
	if e.Err != nil {
		return []error{e2b.ErrTemplateBuild, e.Err}
	}
	return []error{e2b.ErrTemplateBuild}
}

// UploadError describes a failure while uploading a template COPY layer.
//
// UploadError embeds e2b.BaseError so it satisfies e2b.Error and exposes
// HTTPStatus() / ErrorCode() when decoded from an HTTP response. Unwrap()
// exposes both e2b.ErrTemplateUpload and the underlying error (if any).
type UploadError struct {
	e2b.BaseError
	Src, Hash string
	Err       error
}

func (e *UploadError) Error() string {
	return fmt.Sprintf("template upload src=%s hash=%s: %v", e.Src, e.Hash, e.Err)
}

func (e *UploadError) Unwrap() []error {
	if e.Err != nil {
		return []error{e2b.ErrTemplateUpload, e.Err}
	}
	return []error{e2b.ErrTemplateUpload}
}

type apiStatusCoder interface {
	error
	HTTPStatus() int
	ErrorCode() string
}

func extractBaseError(err error) e2b.BaseError {
	if sc, ok := errors.AsType[apiStatusCoder](err); ok {
		return e2b.BaseError{
			StatusCode: sc.HTTPStatus(),
			Code:       sc.ErrorCode(),
		}
	}
	return e2b.BaseError{}
}

func decodeBuildAPIError(resp *http.Response, op, templateID, buildID string) *BuildError {
	apiErr := e2b.DecodeAPIError(resp, "")
	return &BuildError{
		BaseError:  extractBaseError(apiErr),
		Op:         op,
		TemplateID: templateID,
		BuildID:    buildID,
		Err:        apiErr,
	}
}

func decodeUploadAPIError(resp *http.Response, src, hash string) *UploadError {
	apiErr := e2b.DecodeAPIError(resp, "")
	return &UploadError{
		BaseError: extractBaseError(apiErr),
		Src:       src,
		Hash:      hash,
		Err:       apiErr,
	}
}

func mapTransportErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[e2b.Error](err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &e2b.TimeoutError{Message: err.Error(), Cause: err}
	}
	return &e2b.SandboxError{Message: err.Error(), Cause: err}
}
