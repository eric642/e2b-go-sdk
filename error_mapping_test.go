package e2b

import (
	"context"
	"errors"
	"fmt"
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
	err := mapConnectErr(errors.New("plain"))
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("want *SandboxError, got %T", err)
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
		err := DecodeAPIError(jsonResp(c.status, fmt.Sprintf(body, c.status)), c.sandboxID)
		if err == nil || !c.check(err) {
			t.Errorf("%d: unexpected %T: %v", c.status, err, err)
			continue
		}
		if _, ok := errors.AsType[Error](err); !ok {
			t.Errorf("%d: %T does not implement e2b.Error", c.status, err)
		}
		coder, ok := err.(interface {
			ErrorCode() string
			HTTPStatus() int
		})
		if !ok || coder.ErrorCode() != "some_code" || coder.HTTPStatus() != c.status {
			t.Errorf("%d: Code/StatusCode not captured: %v", c.status, err)
		}
		if !strings.Contains(err.Error(), "msg") {
			t.Errorf("%d: message missing: %q", c.status, err.Error())
		}
	}
}

func TestDecodeAPIErrorSuccessAndNil(t *testing.T) {
	if err := DecodeAPIError(jsonResp(204, ""), ""); err != nil {
		t.Fatalf("2xx should be nil, got %v", err)
	}
	if _, ok := errors.AsType[*SandboxError](DecodeAPIError(nil, "")); !ok {
		t.Fatal("nil response should map to *SandboxError")
	}
}

func TestDecodeAPIErrorPlainTextBody(t *testing.T) {
	err := DecodeAPIError(jsonResp(409, "already exists"), "")
	ce, ok := errors.AsType[*ConflictError](err)
	if !ok || ce.Message != "already exists" || ce.Code != "" || ce.StatusCode != 409 {
		t.Fatalf("got %#v", err)
	}
}

func TestMapEnvdFileErrCapturesCode(t *testing.T) {
	err := mapEnvdFileErr(jsonResp(404, `{"code":404,"message":"no such file"}`), "/a")
	fe, ok := errors.AsType[*FileNotFoundError](err)
	if !ok || fe.Path != "/a" || fe.StatusCode != 404 || fe.Message != "no such file" {
		t.Fatalf("got %#v", err)
	}
}
