package e2b

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want InvalidArgumentError, got %T: %v", err, err)
	}
	if !errors.Is(err, in) {
		t.Fatal("InvalidArgumentError should preserve Connect error Cause")
	}
}

func TestMapConnectErrUnauthenticated(t *testing.T) {
	in := connect.NewError(connect.CodeUnauthenticated, errors.New("nope"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*AuthenticationError](err); !ok {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("AuthenticationError should preserve Connect error Cause")
	}
}

func TestMapConnectErrPermissionDenied(t *testing.T) {
	in := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*AuthenticationError](err); !ok {
		t.Fatalf("want AuthenticationError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("AuthenticationError should preserve Connect error Cause")
	}
}

func TestMapConnectErrNotFound(t *testing.T) {
	in := connect.NewError(connect.CodeNotFound, errors.New("missing"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*FileNotFoundError](err); !ok {
		t.Fatalf("want FileNotFoundError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("FileNotFoundError should preserve Connect error Cause")
	}
}

func TestMapConnectErrResourceExhaustedDiskFull(t *testing.T) {
	in := connect.NewError(connect.CodeResourceExhausted, errors.New("no space left on device"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*NotEnoughSpaceError](err); !ok {
		t.Fatalf("want NotEnoughSpaceError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("NotEnoughSpaceError should preserve Connect error Cause")
	}
}

func TestMapConnectErrResourceExhaustedRateLimit(t *testing.T) {
	in := connect.NewError(connect.CodeResourceExhausted, errors.New("rate exceeded"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*RateLimitError](err); !ok {
		t.Fatalf("want RateLimitError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("RateLimitError should preserve Connect error Cause")
	}
}

func TestMapConnectErrDeadlineIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want TimeoutError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("TimeoutError should preserve Connect error Cause")
	}
	if got := err.Error(); got != "e2b: timeout: slow" {
		t.Fatalf("Error()=%q, want non-repeating %q", got, "e2b: timeout: slow")
	}
}

func TestMapConnectErrCanceledIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeCanceled, errors.New("cancel"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestMapConnectErrUnavailableIsTimeout(t *testing.T) {
	in := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want TimeoutError, got %T", err)
	}
}

func TestMapConnectErrInternalFallsBackToSandboxError(t *testing.T) {
	in := connect.NewError(connect.CodeInternal, errors.New("boom"))
	err := mapConnectErr(in)
	if _, ok := errors.AsType[*SandboxError](err); !ok {
		t.Fatalf("want *SandboxError, got %T", err)
	}
	if !errors.Is(err, in) {
		t.Fatal("SandboxError should preserve Connect error Cause")
	}
}

func TestMapConnectErrNonConnectErrorStillMaps(t *testing.T) {
	err := mapConnectErr(errors.New("plain"))
	if _, ok := errors.AsType[*SandboxError](err); !ok {
		t.Fatalf("want *SandboxError, got %T", err)
	}
}

type fakeNetError struct {
	msg     string
	timeout bool
}

func (e fakeNetError) Error() string   { return e.msg }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return e.timeout }

func TestContextErrWrapsDeadline(t *testing.T) {
	err := contextErr(context.DeadlineExceeded)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want TimeoutError, got %T", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expected TimeoutError to unwrap context.DeadlineExceeded")
	}
}

func TestContextErrWrapsCanceled(t *testing.T) {
	err := contextErr(context.Canceled)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want TimeoutError, got %T", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("expected TimeoutError to unwrap context.Canceled")
	}
}

func TestContextErrWrapsNetAndOSDeadlines(t *testing.T) {
	osErr := fmt.Errorf("read: %w", os.ErrDeadlineExceeded)
	gotOS := contextErr(osErr)
	if _, ok := errors.AsType[*TimeoutError](gotOS); !ok || !errors.Is(gotOS, os.ErrDeadlineExceeded) {
		t.Fatalf("os.ErrDeadlineExceeded -> %#v", gotOS)
	}

	netTimeout := fmt.Errorf("dial: %w", fakeNetError{msg: "i/o timeout", timeout: true})
	gotNet := contextErr(netTimeout)
	if _, ok := errors.AsType[*TimeoutError](gotNet); !ok || !errors.Is(gotNet, netTimeout) {
		t.Fatalf("net timeout -> %#v", gotNet)
	}

	netNonTimeout := fakeNetError{msg: "connection refused", timeout: false}
	if got := contextErr(netNonTimeout); got != nil {
		t.Fatalf("non-timeout net.Error should return nil, got %#v", got)
	}

	existing := &TimeoutError{Message: "already timeout"}
	gotExisting := contextErr(existing)
	if te, ok := errors.AsType[*TimeoutError](gotExisting); !ok || !errors.Is(gotExisting, existing) || te.Cause != nil {
		t.Fatalf("existing *TimeoutError should not be double-wrapped: %#v", gotExisting)
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

func jsonResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestDecodeAPIErrorStatusMapping(t *testing.T) {
	body := `{"code":%d,"error_code":"some_code","message":"msg"}`
	cases := []struct {
		status    int
		sandboxID string
		check     func(error) bool
	}{
		{400, "", func(e error) bool { _, ok := errors.AsType[*InvalidArgumentError](e); return ok }},
		{401, "", func(e error) bool { _, ok := errors.AsType[*AuthenticationError](e); return ok }},
		{403, "", func(e error) bool { _, ok := errors.AsType[*AuthenticationError](e); return ok }},
		{404, "sbx-1", func(e error) bool {
			x, ok := errors.AsType[*SandboxNotFoundError](e)
			return ok && x.SandboxID == "sbx-1"
		}},
		{404, "", func(e error) bool { _, ok := errors.AsType[*NotFoundError](e); return ok }},
		{409, "", func(e error) bool { _, ok := errors.AsType[*ConflictError](e); return ok }},
		{429, "", func(e error) bool { _, ok := errors.AsType[*RateLimitError](e); return ok }},
		{507, "", func(e error) bool { _, ok := errors.AsType[*NotEnoughSpaceError](e); return ok }},
		{408, "", func(e error) bool { _, ok := errors.AsType[*TimeoutError](e); return ok }},
		{504, "", func(e error) bool { _, ok := errors.AsType[*TimeoutError](e); return ok }},
		{500, "", func(e error) bool { _, ok := errors.AsType[*SandboxError](e); return ok }},
		{502, "", func(e error) bool { _, ok := errors.AsType[*SandboxError](e); return ok }},
		{503, "", func(e error) bool { _, ok := errors.AsType[*SandboxError](e); return ok }},
	}
	for _, c := range cases {
		resp := jsonResp(c.status, fmt.Sprintf(body, c.status))
		err := DecodeAPIError(resp, c.sandboxID)
		_ = resp.Body.Close()
		if err == nil || !c.check(err) {
			t.Errorf("%d: unexpected %T: %v", c.status, err, err)
			continue
		}
		if _, ok := errors.AsType[Error](err); !ok {
			t.Errorf("%d: %T does not implement e2b.Error", c.status, err)
		}
		coder, ok := errors.AsType[interface {
			error
			ErrorCode() string
			HTTPStatus() int
		}](err)
		if !ok || coder.ErrorCode() != "some_code" || coder.HTTPStatus() != c.status {
			t.Errorf("%d: Code/StatusCode not captured: %v", c.status, err)
		}
		if !strings.Contains(err.Error(), "msg") {
			t.Errorf("%d: message missing: %q", c.status, err.Error())
		}
	}
}

func TestDecodeAPIErrorSuccessAndNil(t *testing.T) {
	resp := jsonResp(204, "")
	err := DecodeAPIError(resp, "")
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("2xx should be nil, got %v", err)
	}
	if _, ok := errors.AsType[*SandboxError](DecodeAPIError(nil, "")); !ok {
		t.Fatal("nil response should map to *SandboxError")
	}
}

func TestDecodeAPIErrorPlainTextBody(t *testing.T) {
	resp := jsonResp(409, "already exists")
	err := DecodeAPIError(resp, "")
	_ = resp.Body.Close()
	ce, ok := errors.AsType[*ConflictError](err)
	if !ok || ce.Message != "already exists" || ce.Code != "" || ce.StatusCode != 409 {
		t.Fatalf("got %#v", err)
	}
}

func TestMapEnvdFileErrCapturesCode(t *testing.T) {
	resp := jsonResp(404, `{"code":404,"message":"no such file"}`)
	err := mapEnvdFileErr(resp, "/a")
	_ = resp.Body.Close()
	fe, ok := errors.AsType[*FileNotFoundError](err)
	if !ok || fe.Path != "/a" || fe.StatusCode != 404 || fe.Message != "no such file" {
		t.Fatalf("got %#v", err)
	}
}

func TestClassifyAPIError(t *testing.T) {
	if err := ClassifyAPIError(200, "", "ok", ""); err != nil {
		t.Fatalf("2xx should return nil, got %v", err)
	}

	// Non-2xx with message
	err := ClassifyAPIError(404, "not_found", "gone", "sbx-42")
	snf, ok := errors.AsType[*SandboxNotFoundError](err)
	if !ok || snf.SandboxID != "sbx-42" || snf.Code != "not_found" || snf.StatusCode != 404 || snf.Message != "gone" {
		t.Fatalf("404 with sandboxID: %#v", err)
	}

	// Default 5xx fallback formats with code when message is empty
	errCodeOnly := ClassifyAPIError(503, "sandbox_capacity_unavailable", "", "")
	se1, ok := errors.AsType[*SandboxError](errCodeOnly)
	if !ok || se1.Message != "http 503: sandbox_capacity_unavailable" || se1.Code != "sandbox_capacity_unavailable" || se1.StatusCode != 503 {
		t.Fatalf("503 code-only: %#v", errCodeOnly)
	}

	// Default 5xx fallback formats status only when both message and code are empty
	errBare := ClassifyAPIError(500, "", "", "")
	se2, ok := errors.AsType[*SandboxError](errBare)
	if !ok || se2.Message != "http 500" || se2.StatusCode != 500 {
		t.Fatalf("500 bare: %#v", errBare)
	}

	// Default 5xx fallback formats with message when present
	errMsg := ClassifyAPIError(502, "bad_gw", "upstream down", "")
	se3, ok := errors.AsType[*SandboxError](errMsg)
	if !ok || se3.Message != "http 502: upstream down" {
		t.Fatalf("502 with msg: %#v", errMsg)
	}
}
