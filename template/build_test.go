package template

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

// A small shared helper that mounts canned handlers for a happy-path build.
// Each test can swap individual handlers by setting fields on the struct.
type fakeTemplateServer struct {
	statusCalls  atomic.Int32
	statusReady  atomic.Bool // when true, status handler returns "ready"
	statusErrMsg string      // when non-empty, status handler returns "error" (set before srv starts)
	uploadURL    string      // filled in below
	sawUpload    atomic.Int32
	present      atomic.Bool // what the files/hash endpoint reports
}

func newFakeTemplateServer(t *testing.T) (*httptest.Server, *fakeTemplateServer) {
	fake := &fakeTemplateServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","aliases":[],"names":["demo"],"public":false,"tags":[]}`))
	})

	// GET /templates/{id}/files/{hash}
	mux.HandleFunc("/templates/tpl_1/files/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if fake.present.Load() {
			_, _ = w.Write([]byte(`{"present":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"present":false,"url":"` + fake.uploadURL + `"}`))
	})

	// Upload target
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			fake.sawUpload.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})

	// POST /v2/templates/{id}/builds/{bld}
	mux.HandleFunc("/v2/templates/tpl_1/builds/bld_1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	// GET /templates/{id}/builds/{bld}/status
	mux.HandleFunc("/templates/tpl_1/builds/bld_1/status", func(w http.ResponseWriter, r *http.Request) {
		fake.statusCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fake.statusErrMsg != "" {
			body := map[string]any{
				"templateID": "tpl_1",
				"buildID":    "bld_1",
				"status":     "error",
				"logEntries": []any{},
				"logs":       []any{},
				"reason": map[string]any{
					"message":    fake.statusErrMsg,
					"logEntries": []any{},
				},
			}
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		if fake.statusReady.Load() {
			_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","status":"ready","logEntries":[],"logs":[]}`))
			return
		}
		// Building → first call emits one log, then next will be ready.
		body := map[string]any{
			"templateID": "tpl_1",
			"buildID":    "bld_1",
			"status":     "building",
			"logEntries": []any{
				map[string]any{"timestamp": time.Now(), "level": "info", "message": "still going"},
			},
			"logs": []any{},
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	srv := httptest.NewServer(mux)
	fake.uploadURL = srv.URL + "/upload"
	t.Cleanup(srv.Close)
	return srv, fake
}

func TestBuild_HappyPathNoCopy(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusReady.Store(true)

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	info, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if info.TemplateID != "tpl_1" || info.BuildID != "bld_1" {
		t.Fatalf("info: %+v", info)
	}
}

func TestBuild_UploadsWhenNotPresent(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusReady.Store(true)
	fake.present.Store(false)

	dir := t.TempDir()
	writeFile(t, dir, "app.txt", "hello")

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").WithContext(dir).Copy("app.txt", "/app/")
	if _, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if fake.sawUpload.Load() != 1 {
		t.Fatalf("expected 1 upload, got %d", fake.sawUpload.Load())
	}
}

func TestBuild_SkipsUploadWhenCached(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusReady.Store(true)
	fake.present.Store(true)

	dir := t.TempDir()
	writeFile(t, dir, "app.txt", "hello")

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").WithContext(dir).Copy("app.txt", "/app/")
	if _, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if fake.sawUpload.Load() != 0 {
		t.Fatalf("cache hit should skip upload, got %d uploads", fake.sawUpload.Load())
	}
}

func TestBuild_ErrorStatusReturnsBuildError(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusErrMsg = "boom at step 3"

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	_, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error")
	}
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BuildError, got %T: %v", err, err)
	}
	if !strings.Contains(be.Error(), "boom at step 3") {
		t.Fatalf("err message missing reason: %s", be.Error())
	}
}

func TestBuildStream_EmitsLogsThenDone(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	// First poll returns "building" with a log, next poll returns "ready".
	// Flip statusReady after the first poll completes.
	go func() {
		// Wait for first status request, then flip to ready.
		for fake.statusCalls.Load() < 1 {
			time.Sleep(1 * time.Millisecond)
		}
		fake.statusReady.Store(true)
	}()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	events, err := cli.BuildStream(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sawLog := false
	var done *BuildInfo
	for ev := range events {
		if ev.Log != nil {
			sawLog = true
		}
		if ev.Done != nil {
			done = ev.Done
		}
		if ev.Err != nil {
			t.Fatalf("unexpected err: %v", ev.Err)
		}
	}
	if !sawLog {
		t.Fatal("expected at least one log event")
	}
	if done == nil {
		t.Fatal("expected a Done event")
	}
}

func TestBuild_CtxCancel(t *testing.T) {
	srv, _ := newFakeTemplateServer(t)
	// Never flip to ready — build stays in "building" forever.

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	_, err := cli.Build(ctx, tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond})
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestBuildInBackground_DoesNotPoll(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	info, err := cli.BuildInBackground(context.Background(), tpl, BuildOptions{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if info.TemplateID != "tpl_1" {
		t.Fatalf("info: %+v", info)
	}
	// Give any stray goroutine a moment; status should have zero calls.
	time.Sleep(20 * time.Millisecond)
	if fake.statusCalls.Load() != 0 {
		t.Fatalf("BuildInBackground should not poll, got %d status calls", fake.statusCalls.Load())
	}
}

func TestBuild_ValidationErrorEmptyName(t *testing.T) {
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: "http://unused"})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	_, err := cli.Build(context.Background(), tpl, BuildOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	var invalid *e2b.InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("want InvalidArgumentError, got %T", err)
	}
}
