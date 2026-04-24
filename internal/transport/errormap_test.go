package transport

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
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
	// Error() must still include the status.
	if !strings.Contains(e.Error(), "http 500") {
		t.Fatalf("Error()=%q", e.Error())
	}
}

func TestHTTPErrorErrorFormat(t *testing.T) {
	e := &HTTPError{Status: 401, Message: "token expired"}
	if got := e.Error(); got != "http 401: token expired" {
		t.Fatalf("got %q", got)
	}
	e2 := &HTTPError{Status: 500}
	if got := e2.Error(); got != "http 500" {
		t.Fatalf("got %q", got)
	}
}

func TestIsConnectCodeTrue(t *testing.T) {
	err := connect.NewError(connect.CodeNotFound, errors.New("missing"))
	if !IsConnectCode(err, connect.CodeNotFound) {
		t.Fatal("IsConnectCode should match")
	}
}

func TestIsConnectCodeWrapped(t *testing.T) {
	err := fmt.Errorf("context: %w", connect.NewError(connect.CodeUnauthenticated, errors.New("x")))
	if !IsConnectCode(err, connect.CodeUnauthenticated) {
		t.Fatal("IsConnectCode should see through wrap")
	}
}

func TestIsConnectCodeFalseForPlainError(t *testing.T) {
	if IsConnectCode(errors.New("plain"), connect.CodeNotFound) {
		t.Fatal("plain error should not match any connect code")
	}
}

func TestConnectCode(t *testing.T) {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New("limit"))
	if code := ConnectCode(err); code != connect.CodeResourceExhausted {
		t.Fatalf("code: %v", code)
	}
	if code := ConnectCode(errors.New("plain")); code != connect.CodeUnknown {
		t.Fatalf("plain error should return CodeUnknown, got %v", code)
	}
	if code := ConnectCode(nil); code != connect.CodeUnknown {
		t.Fatalf("nil should return CodeUnknown, got %v", code)
	}
}
