package e2b

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestMapConnectErrNil(t *testing.T) {
	if err := mapConnectErr(nil); err != nil {
		t.Fatalf("nil input should return nil, got %v", err)
	}
}

func TestMapConnectErrInvalidArgument(t *testing.T) {
	in := connect.NewError(connect.CodeInvalidArgument, errors.New("bad path"))
	err := mapConnectErr(in)
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("want InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestMapConnectErrUnauthenticated(t *testing.T) {
	in := connect.NewError(connect.CodeUnauthenticated, errors.New("nope"))
	err := mapConnectErr(in)
	var ae *AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
}

func TestMapConnectErrPermissionDenied(t *testing.T) {
	in := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	err := mapConnectErr(in)
	var ae *AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
}

func TestMapConnectErrNotFound(t *testing.T) {
	in := connect.NewError(connect.CodeNotFound, errors.New("missing"))
	err := mapConnectErr(in)
	var fe *FileNotFoundError
	if !errors.As(err, &fe) {
		t.Fatalf("want FileNotFoundError, got %T", err)
	}
}

func TestMapConnectErrResourceExhaustedDiskFull(t *testing.T) {
	in := connect.NewError(connect.CodeResourceExhausted, errors.New("no space left on device"))
	err := mapConnectErr(in)
	var ns *NotEnoughSpaceError
	if !errors.As(err, &ns) {
		t.Fatalf("want NotEnoughSpaceError, got %T", err)
	}
}

func TestMapConnectErrResourceExhaustedRateLimit(t *testing.T) {
	in := connect.NewError(connect.CodeResourceExhausted, errors.New("rate exceeded"))
	err := mapConnectErr(in)
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("want RateLimitError, got %T", err)
	}
}

func TestMapConnectErrDeadlineIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow"))
	err := mapConnectErr(in)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestMapConnectErrCanceledIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeCanceled, errors.New("cancel"))
	err := mapConnectErr(in)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestMapConnectErrUnavailableIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	err := mapConnectErr(in)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestMapConnectErrInternalFallsBackToSandboxError(t *testing.T) {
	in := connect.NewError(connect.CodeInternal, errors.New("boom"))
	err := mapConnectErr(in)
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("want *SandboxError, got %T", err)
	}
}

func TestMapConnectErrNonConnectErrorStillMaps(t *testing.T) {
	// A plain error (not a connect.Error) should become *SandboxError.
	err := mapConnectErr(errors.New("plain"))
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("want *SandboxError, got %T", err)
	}
}

func TestMapHTTPErrSandboxNotFound(t *testing.T) {
	resp := &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(strings.NewReader(`{"message":"gone"}`)),
	}
	err := mapHTTPErr(resp, "sbx-123")
	var se *SandboxNotFoundError
	if !errors.As(err, &se) {
		t.Fatalf("want SandboxNotFoundError, got %T", err)
	}
	if se.SandboxID != "sbx-123" {
		t.Fatalf("SandboxID: %q", se.SandboxID)
	}
}

func TestMapHTTPErr404WithoutSandboxIDIsNotFoundError(t *testing.T) {
	resp := &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}
	err := mapHTTPErr(resp, "")
	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("want *NotFoundError, got %T", err)
	}
	// Sanity: not accidentally a SandboxNotFoundError.
	var sne *SandboxNotFoundError
	if errors.As(err, &sne) {
		t.Fatal("should not match SandboxNotFoundError without sandboxID")
	}
}

func TestMapHTTPErrRateLimit(t *testing.T) {
	resp := &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(""))}
	err := mapHTTPErr(resp, "")
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("want RateLimitError, got %T", err)
	}
}

func TestMapHTTPErrAuth401(t *testing.T) {
	resp := &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(""))}
	err := mapHTTPErr(resp, "")
	var ae *AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
}

func TestMapHTTPErrAuth403(t *testing.T) {
	resp := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(""))}
	err := mapHTTPErr(resp, "")
	var ae *AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
}

func TestMapHTTPErrInsufficientStorage(t *testing.T) {
	resp := &http.Response{StatusCode: 507, Body: io.NopCloser(strings.NewReader(""))}
	err := mapHTTPErr(resp, "")
	var ns *NotEnoughSpaceError
	if !errors.As(err, &ns) {
		t.Fatalf("want NotEnoughSpaceError, got %T", err)
	}
}

func TestMapHTTPErrTimeouts(t *testing.T) {
	for _, code := range []int{408, 504} {
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
		err := mapHTTPErr(resp, "")
		var te *TimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("code %d: want TimeoutError, got %T", code, err)
		}
	}
}

func TestMapHTTPErrNilResponse(t *testing.T) {
	err := mapHTTPErr(nil, "")
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("want *SandboxError for nil resp, got %T", err)
	}
}

func TestMapHTTPErrSuccessReturnsNil(t *testing.T) {
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}
	if err := mapHTTPErr(resp, ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestContextErrWrapsDeadline(t *testing.T) {
	err := contextErr(context.DeadlineExceeded)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestContextErrWrapsCanceled(t *testing.T) {
	err := contextErr(context.Canceled)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestContextErrIgnoresOthers(t *testing.T) {
	if contextErr(errors.New("random")) != nil {
		t.Fatal("non-context error should return nil")
	}
	if contextErr(nil) != nil {
		t.Fatal("nil input should return nil")
	}
}
