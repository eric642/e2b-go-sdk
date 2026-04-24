package e2b

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// mapConnectErr translates a Connect-RPC error into one of the SDK error
// types. Fallback is *SandboxError.
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

// mapHTTPErr translates an unexpected HTTP response into an SDK error. The
// resp body is consumed via transport.ReadHTTPError. sandboxID optionally
// annotates SandboxNotFoundError when mapping 404 from a sandbox endpoint.
func mapHTTPErr(resp *http.Response, sandboxID string) error {
	if resp == nil {
		return newSandboxError("empty response", nil)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	httpErr := transport.ReadHTTPError(resp)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthenticationError{Message: httpErr.Message}
	case http.StatusNotFound:
		if sandboxID != "" {
			return &SandboxNotFoundError{SandboxID: sandboxID, Message: httpErr.Message}
		}
		return &NotFoundError{Message: httpErr.Message}
	case http.StatusTooManyRequests:
		return &RateLimitError{Message: httpErr.Message}
	case http.StatusInsufficientStorage:
		return &NotEnoughSpaceError{Message: httpErr.Message}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return &TimeoutError{Message: httpErr.Message}
	}
	return newSandboxError(httpErr.Error(), nil)
}

// contextErr wraps context timeouts into TimeoutError.
func contextErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &TimeoutError{Message: err.Error(), Cause: err}
	}
	return nil
}
