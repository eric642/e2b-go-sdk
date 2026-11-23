package e2b

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorInterfaceMembership(t *testing.T) {
	cases := []struct {
		name string
		e    Error
	}{
		{"SandboxError", &SandboxError{Message: "x"}},
		{"TimeoutError", &TimeoutError{Message: "x"}},
		{"InvalidArgumentError", &InvalidArgumentError{Message: "x"}},
		{"NotEnoughSpaceError", &NotEnoughSpaceError{Message: "x"}},
		{"NotFoundError", &NotFoundError{Message: "x"}},
		{"FileNotFoundError", &FileNotFoundError{Message: "x"}},
		{"SandboxNotFoundError", &SandboxNotFoundError{Message: "x"}},
		{"AuthenticationError", &AuthenticationError{Message: "x"}},
		{"GitAuthError", &GitAuthError{Message: "x"}},
		{"GitUpstreamError", &GitUpstreamError{Message: "x"}},
		{"TemplateError", &TemplateError{Message: "x"}},
		{"RateLimitError", &RateLimitError{Message: "x"}},
		{"ConflictError", &ConflictError{Message: "x"}},
		{"BuildError", &BuildError{Message: "x"}},
		{"FileUploadError", &FileUploadError{Path: "/p", Message: "x"}},
		{"VolumeError", &VolumeError{Message: "x"}},
		{"CommandExitError", &CommandExitError{Result: CommandResult{ExitCode: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := errors.AsType[Error](tc.e); !ok {
				t.Fatalf("%T should satisfy Error", tc.e)
			}
			if tc.e.Error() == "" {
				t.Fatalf("%T.Error() returned empty string", tc.e)
			}
		})
	}
}

func TestSandboxErrorWithCause(t *testing.T) {
	cause := errors.New("boom")
	e := newSandboxError("failed", cause)
	if !strings.Contains(e.Error(), "failed") || !strings.Contains(e.Error(), "boom") {
		t.Fatalf("unexpected message: %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap should surface the Cause")
	}
}

func TestSandboxErrorWithoutCause(t *testing.T) {
	e := newSandboxError("failed", nil)
	if e.Error() != "e2b: failed" {
		t.Fatalf("got %q", e.Error())
	}
	if e.Unwrap() != nil {
		t.Fatal("Unwrap without Cause should return nil")
	}
}

func TestTimeoutErrorImplementsNetError(t *testing.T) {
	e := &TimeoutError{Message: "slow"}
	if !e.Timeout() {
		t.Fatal("Timeout() must return true")
	}
	if !e.Temporary() {
		t.Fatal("Temporary() must return true")
	}
	if got := e.Error(); got != "e2b: timeout: slow" {
		t.Fatalf("Error()=%q", got)
	}

	inner := errors.New("orig")
	e2 := &TimeoutError{Message: "x", Cause: inner}
	if !errors.Is(e2, inner) {
		t.Fatal("Unwrap should surface Cause")
	}
	if got := e2.Error(); got != "e2b: timeout: x: orig" {
		t.Fatalf("Error()=%q", got)
	}

	eCauseOnly := &TimeoutError{Cause: inner}
	if got := eCauseOnly.Error(); got != "e2b: timeout: orig" {
		t.Fatalf("cause-only Error()=%q", got)
	}

	eSameMsg := &TimeoutError{Message: "orig", Cause: inner}
	if got := eSameMsg.Error(); got != "e2b: timeout: orig" {
		t.Fatalf("same-msg Error()=%q (must not duplicate)", got)
	}

	eContained := &TimeoutError{Message: "slow", Cause: errors.New("deadline_exceeded: slow")}
	if got := eContained.Error(); got != "e2b: timeout: slow" {
		t.Fatalf("contained-msg Error()=%q (must not repeat message)", got)
	}

	eEmpty := &TimeoutError{}
	if got := eEmpty.Error(); got != "e2b: timeout" {
		t.Fatalf("empty Error()=%q", got)
	}
}

func TestFileNotFoundErrorRendersPath(t *testing.T) {
	with := (&FileNotFoundError{Path: "/a/b", Message: "missing"}).Error()
	without := (&FileNotFoundError{Message: "missing"}).Error()
	if !strings.Contains(with, "/a/b") {
		t.Fatalf("Path missing: %q", with)
	}
	if strings.Contains(without, "/a/b") {
		t.Fatalf("without path should not include it: %q", without)
	}
}

func TestSandboxNotFoundErrorRendersID(t *testing.T) {
	with := (&SandboxNotFoundError{SandboxID: "sbx-1", Message: "gone"}).Error()
	without := (&SandboxNotFoundError{Message: "gone"}).Error()
	if !strings.Contains(with, "sbx-1") {
		t.Fatalf("SandboxID missing: %q", with)
	}
	if strings.Contains(without, "sbx-1") {
		t.Fatalf("without ID should not include it: %q", without)
	}
}

func TestCommandExitErrorMessageIncludesCode(t *testing.T) {
	e := &CommandExitError{Result: CommandResult{ExitCode: 42, Error: "bad"}}
	msg := e.Error()
	if !strings.Contains(msg, "42") || !strings.Contains(msg, "bad") {
		t.Fatalf("CommandExitError message missing fields: %q", msg)
	}
}

func TestBuildErrorUnwrap(t *testing.T) {
	cause := errors.New("inner")
	e := &BuildError{Message: "mid", Cause: cause}
	if !errors.Is(e, cause) {
		t.Fatal("BuildError should unwrap Cause")
	}
	if !strings.Contains(e.Error(), "inner") {
		t.Fatalf("BuildError message should include cause: %q", e.Error())
	}
}

func TestFileUploadErrorFormatAndUnwrap(t *testing.T) {
	cause := errors.New("unexpected EOF")
	cases := []struct {
		name string
		err  *FileUploadError
		want string
	}{
		{"pathAndMsg", &FileUploadError{Path: "/tmp/a", Message: "stall"}, "e2b: file upload error: /tmp/a: stall"},
		{"msgOnly", &FileUploadError{Message: "stall"}, "e2b: file upload error: stall"},
		{"pathAndCause", &FileUploadError{Path: "/tmp/a", Cause: cause}, "e2b: file upload error: /tmp/a: unexpected EOF"},
		{"causeOnly", &FileUploadError{Cause: cause}, "e2b: file upload error: unexpected EOF"},
		{"all", &FileUploadError{Path: "/tmp/a", Message: "stall", Cause: cause}, "e2b: file upload error: /tmp/a: stall: unexpected EOF"},
		{"empty", &FileUploadError{}, "e2b: file upload error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error()=%q, want %q", got, tc.want)
			}
			if tc.err.Cause != nil && !errors.Is(tc.err, cause) {
				t.Fatal("FileUploadError.Unwrap should surface Cause")
			}
		})
	}
}

func TestTypedErrorsUnwrapCause(t *testing.T) {
	cause := errors.New("underlying")
	for _, err := range []error{
		&InvalidArgumentError{Message: "m", Cause: cause},
		&AuthenticationError{Message: "m", Cause: cause},
		&FileNotFoundError{Message: "m", Cause: cause},
		&NotEnoughSpaceError{Message: "m", Cause: cause},
		&RateLimitError{Message: "m", Cause: cause},
	} {
		if !errors.Is(err, cause) {
			t.Errorf("%T did not unwrap Cause", err)
		}
	}
}

func TestVolumeErrorUnwrap(t *testing.T) {
	cause := errors.New("bad")
	e := &VolumeError{Message: "x", Cause: cause}
	if !errors.Is(e, cause) {
		t.Fatal("VolumeError should unwrap Cause")
	}
	bare := &VolumeError{Message: "x"}
	if !strings.Contains(bare.Error(), "x") {
		t.Fatalf("bare VolumeError message: %q", bare.Error())
	}
}

func TestErrNotImplemented(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented must be a sentinel")
	}
	if !strings.Contains(ErrNotImplemented.Error(), "not implemented") {
		t.Fatalf("unexpected message: %q", ErrNotImplemented.Error())
	}
}

type externalError struct {
	BaseError
	msg string
}

func (e *externalError) Error() string { return e.msg }

func TestBaseErrorEmbeddingSatisfiesError(t *testing.T) {
	var err error = &externalError{BaseError: BaseError{StatusCode: 409, Code: "conflict_code"}, msg: "boom"}
	if _, ok := errors.AsType[Error](err); !ok {
		t.Fatal("type embedding BaseError should satisfy e2b.Error")
	}
	coder, ok := errors.AsType[interface {
		error
		ErrorCode() string
		HTTPStatus() int
	}](err)
	if !ok || coder.ErrorCode() != "conflict_code" || coder.HTTPStatus() != 409 {
		t.Fatalf("BaseError accessors not promoted: %v", err)
	}
}

func TestConflictErrorMessage(t *testing.T) {
	e := &ConflictError{BaseError: BaseError{Code: "idempotency_in_progress"}, Message: "busy"}
	if e.Error() != "e2b: conflict: busy" {
		t.Fatalf("got %q", e.Error())
	}
	if e.Code != "idempotency_in_progress" {
		t.Fatalf("Code=%q", e.Code)
	}
}
