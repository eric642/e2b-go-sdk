package e2b

import (
	"errors"
	"strings"
	"testing"
)

// Each concrete error type must satisfy the sentinel Error interface so
// callers can errors.As(err, &e2b.Error). baseError is the shared marker, so
// if this list ever falls out of sync with errors.go the type system will
// catch it.
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
			// .Error() must not panic or return empty.
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
