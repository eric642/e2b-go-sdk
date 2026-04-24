package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"connectrpc.com/connect"
)

// errorPayload mirrors the JSON shape the E2B API uses for error responses.
type errorPayload struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// HTTPError captures an unexpected HTTP response so the caller can classify
// it into the right SDK error type.
type HTTPError struct {
	Status  int
	Message string
	Body    []byte
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("http %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("http %d", e.Status)
}

// ReadHTTPError parses an HTTP response body into an HTTPError. It does not
// close the body — the caller owns it.
func ReadHTTPError(resp *http.Response) *HTTPError {
	body, _ := io.ReadAll(resp.Body)
	e := &HTTPError{Status: resp.StatusCode, Body: body}
	var p errorPayload
	if err := json.Unmarshal(body, &p); err == nil && p.Message != "" {
		e.Message = p.Message
	} else if len(body) > 0 {
		e.Message = string(body)
	}
	return e
}

// IsConnectCode reports whether err is a Connect-RPC error with the given code.
func IsConnectCode(err error, code connect.Code) bool {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code() == code
	}
	return false
}

// ConnectCode extracts the Connect code if present; otherwise returns
// connect.CodeUnknown.
func ConnectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}
