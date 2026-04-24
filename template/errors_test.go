package template

import (
	"errors"
	"testing"

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
