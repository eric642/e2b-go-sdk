package template

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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
	resp, err := cli.requestBuildWithOptions(context.Background(), BuildOptions{
		Name:     "demo",
		CPUCount: 2,
		MemoryMB: 1024,
	})
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
	_, err := cli.requestBuildWithOptions(context.Background(), BuildOptions{Name: "demo"})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	be, ok := errors.AsType[*BuildError](err)
	if !ok {
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
	if gotMethod != http.MethodPut {
		t.Fatalf("method: %q", gotMethod)
	}
	if gotBody != "hello" {
		t.Fatalf("body: %q", gotBody)
	}
}

func TestClient_UploadFile_StreamsOSFileWithGetBodyAndPreservesLongerDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != "streamed-file-payload" {
			t.Errorf("unexpected payload %q", string(raw))
		}
		if r.ContentLength != int64(len("streamed-file-payload")) {
			t.Errorf("ContentLength=%d", r.ContentLength)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var (
		gotDeadline   time.Time
		gotGetBodyOK  bool
		gotBodyReplay string
	)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotDeadline, _ = req.Context().Deadline()
		if req.GetBody != nil {
			rc, err := req.GetBody()
			if err == nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				gotBodyReplay = string(b)
				gotGetBodyOK = true
			}
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	cli, err := NewClient(e2b.Config{
		APIKey:     "k",
		APIURL:     srv.URL,
		HTTPClient: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(t.TempDir(), "layer.tar.gz")
	if err := os.WriteFile(p, []byte("streamed-file-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// Caller sets a 2-hour deadline; uploadFile must not clamp it to 1 hour.
	wantDeadline := time.Now().Add(2 * time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), wantDeadline)
	defer cancel()

	if err := cli.uploadFile(ctx, srv.URL+"/blob", f); err != nil {
		t.Fatal(err)
	}
	if !gotGetBodyOK || gotBodyReplay != "streamed-file-payload" {
		t.Fatalf("expected retry-safe GetBody, ok=%v replay=%q", gotGetBodyOK, gotBodyReplay)
	}
	if gotDeadline.Before(wantDeadline.Add(-time.Second)) {
		t.Fatalf("uploadFile clamped caller deadline: got %v want ~%v", gotDeadline, wantDeadline)
	}
}

func TestClient_DoWithTokenFallback_DoesNotCacheOn403Or5xx(t *testing.T) {
	var apiKeyCalls, bearerCalls atomic.Int32
	var bearerStatus atomic.Int32
	bearerStatus.Store(http.StatusForbidden)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			apiKeyCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			bearerCalls.Add(1)
			w.WriteHeader(int(bearerStatus.Load()))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	cli, err := NewClient(e2b.Config{
		APIKey:      "k",
		AccessToken: "tok",
		APIURL:      srv.URL,
		Retries:     new(0),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 403 on Bearer must NOT cache fallback mask.
	if _, err := cli.List(context.Background(), ListOptions{}); err == nil {
		t.Fatal("expected 403 error")
	}
	if cli.tokenFallbackMask.Load() != 0 {
		t.Fatalf("403 must not cache tokenFallbackMask, got %d", cli.tokenFallbackMask.Load())
	}

	// 500 on Bearer must NOT cache fallback mask.
	bearerStatus.Store(http.StatusInternalServerError)
	if _, err := cli.List(context.Background(), ListOptions{}); err == nil {
		t.Fatal("expected 500 error")
	}
	if cli.tokenFallbackMask.Load() != 0 {
		t.Fatalf("500 must not cache tokenFallbackMask, got %d", cli.tokenFallbackMask.Load())
	}
	if apiKeyCalls.Load() != 2 || bearerCalls.Load() != 2 {
		t.Fatalf("expected 2 apiKey + 2 bearer attempts, got %d / %d", apiKeyCalls.Load(), bearerCalls.Load())
	}
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
