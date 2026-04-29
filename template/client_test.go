package template

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestNewClient_WithEmptyConfigResolvesDefaults(t *testing.T) {
	cli, err := NewClient(e2b.Config{APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if cli == nil {
		t.Fatal("nil Client")
	}
	if cli.cfg.APIURL == "" {
		t.Fatal("APIURL should be populated after Resolve")
	}
}

func TestClient_RequestBuild_PostsV3(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/templates" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","aliases":[],"names":["demo"],"public":false,"tags":[]}`))
	}))
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.requestBuild(context.Background(), "demo", nil, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TemplateID != "tpl_1" || resp.BuildID != "bld_1" {
		t.Fatalf("response: %+v", resp)
	}
	if body["name"] != "demo" {
		t.Fatalf("request body name: %v", body)
	}
}

func TestClient_RequestBuild_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	_, err := cli.requestBuild(context.Background(), "demo", nil, 0, 0)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	var be *BuildError
	if !asError(err, &be) {
		t.Fatalf("expected *BuildError, got %T", err)
	}
	if be.Op != "request" {
		t.Fatalf("Op: %q", be.Op)
	}
}

func TestClient_UploadFile_PutsToSignedURL(t *testing.T) {
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err := cli.uploadFile(context.Background(), srv.URL+"/blob", bytesReader("hello")); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Fatalf("method: %q", gotMethod)
	}
	if gotBody != "hello" {
		t.Fatalf("body: %q", gotBody)
	}
}

// helpers
func asError(err error, dst **BuildError) bool {
	if be, ok := err.(*BuildError); ok {
		*dst = be
		return true
	}
	return false
}

func bytesReader(s string) io.Reader {
	return &stringReader{s: s}
}

type stringReader struct{ s string }

// Pointer receiver is required so the slice advance sticks across Read calls.
func (r *stringReader) Read(p []byte) (int, error) {
	n := copy(p, r.s)
	if n >= len(r.s) {
		return n, io.EOF
	}
	r.s = r.s[n:]
	return n, nil
}
