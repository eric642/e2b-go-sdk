package e2b

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/eric642/e2b-go-sdk/internal/transport"
)

func mapConnectErr(err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := contextErr(err); ctxErr != nil {
		return ctxErr
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return newSandboxError(err.Error(), err)
	}
	msg := ce.Message()
	switch ce.Code() {
	case connect.CodeInvalidArgument:
		return &InvalidArgumentError{Message: msg}
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return &AuthenticationError{Message: msg}
	case connect.CodeNotFound:
		return &FileNotFoundError{Message: msg}
	case connect.CodeResourceExhausted:
		// envd returns ResourceExhausted for out-of-space and rate limit.
		// Disk-full messages usually contain "no space" — treat those as
		// NotEnoughSpaceError for parity with JS/Python.
		if strings.Contains(strings.ToLower(msg), "no space") {
			return &NotEnoughSpaceError{Message: msg}
		}
		return &RateLimitError{Message: msg}
	case connect.CodeUnavailable, connect.CodeCanceled, connect.CodeDeadlineExceeded:
		return &TimeoutError{Message: msg, Cause: err}
	}
	return newSandboxError(msg, err)
}

func mapHTTPErr(resp *http.Response, sandboxID string) error {
	return DecodeAPIError(resp, sandboxID)
}

// DecodeAPIError translates a non-2xx control-plane HTTP response into an
// e2b.Error, decoding the JSON error body ({"code", "message", "error_code"})
// into the error message and BaseError.Code / BaseError.StatusCode. It
// returns nil for a 2xx response. The body is read (up to 1 MiB) but not
// closed; the caller still owns resp.Body.
//
// Status mapping:
//
//	400       -> *InvalidArgumentError
//	401, 403  -> *AuthenticationError
//	404       -> *SandboxNotFoundError when sandboxID != "", else *NotFoundError
//	409       -> *ConflictError
//	429       -> *RateLimitError
//	507       -> *NotEnoughSpaceError
//	408, 504  -> *TimeoutError
//	otherwise -> *SandboxError (including 5xx such as 500/502/503)
//
// template and volume pass sandboxID "" and may re-wrap *NotFoundError into
// their own resource-specific types.
func DecodeAPIError(resp *http.Response, sandboxID string) error {
	if resp == nil {
		return newSandboxError("empty response", nil)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	httpErr := transport.ReadHTTPError(resp)
	return classifyStatusError(resp.StatusCode, httpErr.Code, httpErr.Message, sandboxID, httpErr.Error())
}

// ClassifyAPIError classifies an HTTP status code, machine-readable error code,
// and error message into the corresponding e2b.Error type using the same status
// mapping as DecodeAPIError. It returns nil for a 2xx status.
func ClassifyAPIError(status int, code, msg, sandboxID string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	fallback := fmt.Sprintf("http %d: %s", status, msg)
	if msg == "" {
		if code != "" {
			fallback = fmt.Sprintf("http %d: %s", status, code)
		} else {
			fallback = fmt.Sprintf("http %d", status)
		}
	}
	return classifyStatusError(status, code, msg, sandboxID, fallback)
}

func classifyStatusError(status int, code, msg, sandboxID, fallbackMsg string) error {
	base := BaseError{StatusCode: status, Code: code}
	switch status {
	case http.StatusBadRequest:
		return &InvalidArgumentError{BaseError: base, Message: msg}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthenticationError{BaseError: base, Message: msg}
	case http.StatusNotFound:
		if sandboxID != "" {
			return &SandboxNotFoundError{BaseError: base, SandboxID: sandboxID, Message: msg}
		}
		return &NotFoundError{BaseError: base, Message: msg}
	case http.StatusConflict:
		return &ConflictError{BaseError: base, Message: msg}
	case http.StatusTooManyRequests:
		return &RateLimitError{BaseError: base, Message: msg}
	case http.StatusInsufficientStorage:
		return &NotEnoughSpaceError{BaseError: base, Message: msg}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return &TimeoutError{BaseError: base, Message: msg}
	default:
		return &SandboxError{BaseError: base, Message: fallbackMsg}
	}
}

func mapEnvdFileErr(resp *http.Response, path string) error {
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		httpErr := transport.ReadHTTPError(resp)
		return &FileNotFoundError{
			BaseError: BaseError{StatusCode: resp.StatusCode, Code: httpErr.Code},
			Path:      path,
			Message:   httpErr.Message,
		}
	}
	return mapHTTPErr(resp, "")
}

func contextErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &TimeoutError{Message: err.Error(), Cause: err}
	}
	return nil
}
