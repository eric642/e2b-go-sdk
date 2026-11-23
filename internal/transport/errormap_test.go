package transport

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadHTTPErrorParsesJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(`{"code":400,"message":"bad"}`)),
	}
	e := ReadHTTPError(resp)
	if e.Status != 400 {
		t.Fatalf("Status: %d", e.Status)
	}
	if e.Message != "bad" {
		t.Fatalf("Message: %q", e.Message)
	}
}

func TestReadHTTPErrorParsesErrorCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: 503,
		Body:       io.NopCloser(strings.NewReader(`{"code":503,"error_code":"sandbox_capacity_unavailable","message":"no capacity"}`)),
	}
	e := ReadHTTPError(resp)
	if e.Code != "sandbox_capacity_unavailable" || e.Message != "no capacity" {
		t.Fatalf("got %+v", e)
	}
}

func TestReadHTTPErrorNilBody(t *testing.T) {
	e := ReadHTTPError(&http.Response{StatusCode: 500})
	if e.Status != 500 || e.Message != "" {
		t.Fatalf("got %+v", e)
	}
}

func TestReadHTTPErrorBoundsBody(t *testing.T) {
	big := strings.Repeat("x", maxErrorBody+100)
	e := ReadHTTPError(&http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(big))})
	if len(e.Body) != maxErrorBody {
		t.Fatalf("body len=%d, want %d", len(e.Body), maxErrorBody)
	}
}

func TestReadHTTPErrorFallsBackToRawBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 502,
		Body:       io.NopCloser(strings.NewReader("plain text")),
	}
	e := ReadHTTPError(resp)
	if e.Message != "plain text" {
		t.Fatalf("Message: %q", e.Message)
	}
}

func TestReadHTTPErrorEmptyBodyLeavesMessageBlank(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader("")),
	}
	e := ReadHTTPError(resp)
	if e.Message != "" {
		t.Fatalf("empty body should yield empty message, got %q", e.Message)
	}
	if !strings.Contains(e.Error(), "http 500") {
		t.Fatalf("Error()=%q", e.Error())
	}
}

func TestHTTPErrorErrorFormat(t *testing.T) {
	e := &HTTPError{Status: 401, Message: "token expired"}
	if got := e.Error(); got != "http 401: token expired" {
		t.Fatalf("got %q", got)
	}
	eCodeOnly := &HTTPError{Status: 503, Code: "sandbox_capacity_unavailable"}
	if got := eCodeOnly.Error(); got != "http 503: sandbox_capacity_unavailable" {
		t.Fatalf("got %q", got)
	}
	e2 := &HTTPError{Status: 500}
	if got := e2.Error(); got != "http 500" {
		t.Fatalf("got %q", got)
	}
}

func TestReadHTTPErrorCodeOnlyJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: 503,
		Body:       io.NopCloser(strings.NewReader(`{"error_code":"sandbox_capacity_unavailable"}`)),
	}
	e := ReadHTTPError(resp)
	if e.Code != "sandbox_capacity_unavailable" || e.Message != "" {
		t.Fatalf("got %+v", e)
	}
	if got := e.Error(); got != "http 503: sandbox_capacity_unavailable" {
		t.Fatalf("Error()=%q", got)
	}
}
