package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxErrorBody = 1 << 20

// errorPayload mirrors the JSON shape the E2B API uses for error responses
// (OpenAPI components/schemas/Error).
type errorPayload struct {
	Code      int    `json:"code,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type HTTPError struct {
	Status  int
	Code    string
	Message string
	Body    []byte
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("http %d: %s", e.Status, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("http %d: %s", e.Status, e.Code)
	}
	return fmt.Sprintf("http %d", e.Status)
}

func ReadHTTPError(resp *http.Response) *HTTPError {
	e := &HTTPError{Status: resp.StatusCode}
	if resp.Body == nil {
		return e
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e.Body = body
	var p errorPayload
	if err := json.Unmarshal(body, &p); err == nil && (p.Message != "" || p.ErrorCode != "") {
		e.Message = p.Message
		e.Code = p.ErrorCode
	} else if len(body) > 0 {
		e.Message = string(body)
	}
	return e
}
