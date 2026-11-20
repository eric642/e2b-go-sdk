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
			var generic Error
			if !errors.As(tc.e, &generic) {
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
	inner := errors.New("orig")
	e2 := &TimeoutError{Message: "x", Cause: inner}
	if !errors.Is(e2, inner) {
		t.Fatal("Unwrap should surface Cause")
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

func TestFileUploadErrorIncludesPath(t *testing.T) {
	e := &FileUploadError{Path: "/tmp/a", Message: "stall"}
	if !strings.Contains(e.Error(), "/tmp/a") {
		t.Fatalf("FileUploadError missing Path: %q", e.Error())
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
	coder, ok := err.(interface {
		ErrorCode() string
		HTTPStatus() int
	})
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
