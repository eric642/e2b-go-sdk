package template

import (
	"fmt"

	e2b "github.com/eric642/e2b-go-sdk"
)

// BuildError describes a failure during a server-side template build.
// Unwrap() returns e2b.ErrTemplateBuild so callers can use errors.Is.
type BuildError struct {
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

func (e *BuildError) Unwrap() error { return e2b.ErrTemplateBuild }

// UploadError describes a failure while uploading a template COPY layer.
type UploadError struct {
	Src, Hash string
	Err       error
}

func (e *UploadError) Error() string {
	return fmt.Sprintf("template upload src=%s hash=%s: %v", e.Src, e.Hash, e.Err)
}

func (e *UploadError) Unwrap() error { return e2b.ErrTemplateUpload }
