package template

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestBuildError_UnwrapsToSentinel(t *testing.T) {
	e := &BuildError{Op: "poll", Message: "boom"}
	if !errors.Is(e, e2b.ErrTemplateBuild) {
		t.Fatalf("should match e2b.ErrTemplateBuild")
	}
}

func TestUploadError_UnwrapsToSentinel(t *testing.T) {
	e := &UploadError{Src: "a", Hash: "h", Err: errors.New("x")}
	if !errors.Is(e, e2b.ErrTemplateUpload) {
		t.Fatalf("should match e2b.ErrTemplateUpload")
	}
}

func TestBuildError_ErrorString(t *testing.T) {
	e := &BuildError{Op: "trigger", TemplateID: "tpl_1", BuildID: "bld_1", Step: "0", Message: "boom"}
	want := "template build trigger: templateID=tpl_1 buildID=bld_1 step=0: boom"
	if got := e.Error(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildError_FallsBackToWrappedErr(t *testing.T) {
	inner := errors.New("inner boom")
	e := &BuildError{Op: "request", Err: inner}
	got := e.Error()
	if got == "" || got[len(got)-len(inner.Error()):] != inner.Error() {
		t.Fatalf("should include inner err at end: %q", got)
	}
}

func TestErrors_SatisfyE2BErrorAndPreserveMetadata(t *testing.T) {
	conflict := &e2b.ConflictError{
		BaseError: e2b.BaseError{StatusCode: 409, Code: "idempotency_request_mismatch"},
		Message:   "Idempotency-Key was already used",
	}
	be := &BuildError{
		BaseError: e2b.BaseError{StatusCode: 409, Code: "idempotency_request_mismatch"},
		Op:        "request",
		Err:       conflict,
	}
	if _, ok := errors.AsType[e2b.Error](be); !ok {
		t.Fatal("*BuildError must satisfy e2b.Error")
	}
	if be.HTTPStatus() != 409 || be.ErrorCode() != "idempotency_request_mismatch" {
		t.Fatalf("status=%d code=%q", be.HTTPStatus(), be.ErrorCode())
	}
	if _, ok := errors.AsType[*e2b.ConflictError](be); !ok {
		t.Fatal("*BuildError should unwrap underlying *e2b.ConflictError")
	}
	if !errors.Is(be, e2b.ErrTemplateBuild) {
		t.Fatal("*BuildError should still match e2b.ErrTemplateBuild")
	}

	ue := &UploadError{
		BaseError: e2b.BaseError{StatusCode: 403, Code: "signature_expired"},
		Src:       "app.txt",
		Hash:      "deadbeef",
		Err:       &e2b.AuthenticationError{BaseError: e2b.BaseError{StatusCode: 403}, Message: "SignatureDoesNotMatch"},
	}
	if _, ok := errors.AsType[e2b.Error](ue); !ok {
		t.Fatal("*UploadError must satisfy e2b.Error")
	}
	if ue.HTTPStatus() != 403 || ue.ErrorCode() != "signature_expired" {
		t.Fatalf("status=%d code=%q", ue.HTTPStatus(), ue.ErrorCode())
	}
	if _, ok := errors.AsType[*e2b.AuthenticationError](ue); !ok {
		t.Fatal("*UploadError should unwrap underlying *e2b.AuthenticationError")
	}
	if !errors.Is(ue, e2b.ErrTemplateUpload) {
		t.Fatal("*UploadError should still match e2b.ErrTemplateUpload")
	}
	if got := ue.Error(); !strings.Contains(got, "app.txt") || !strings.Contains(got, "deadbeef") {
		t.Fatalf("UploadError.Error() = %q", got)
	}
}

func TestDefaultLogger_FormatsEntriesAndHandlesNilWriter(t *testing.T) {
	var buf bytes.Buffer
	logFn := DefaultLogger(&buf)
	ts := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	logFn(LogEntry{
		Timestamp: ts,
		Level:     LogLevelWarn,
		Message:   "retrying step",
	})
	want := "[2026-04-24T12:00:00Z] warn retrying step\n"
	if buf.String() != want {
		t.Fatalf("DefaultLogger output = %q, want %q", buf.String(), want)
	}
	if DefaultLogger(nil) == nil {
		t.Fatal("DefaultLogger(nil) returned nil")
	}
}
