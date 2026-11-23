package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

// A small shared helper that mounts canned handlers for a happy-path build.
// Each test can swap individual handlers by setting fields on the struct.
type fakeTemplateServer struct {
	statusCalls     atomic.Int32
	statusReady     atomic.Bool // when true, status handler returns "ready"
	readyAfterCalls atomic.Int32
	onStatusCall    func(call int32)
	statusErrMsg    string // when non-empty, status handler returns "error" (set before srv starts)
	uploadURL       string // filled in below
	sawUpload       atomic.Int32
	present         atomic.Bool // what the files/hash endpoint reports
	presentWithURL  atomic.Bool // when true and present=true, still return uploadURL
}

func newFakeTemplateServer(t *testing.T) (*httptest.Server, *fakeTemplateServer) {
	t.Helper()
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
			if fake.presentWithURL.Load() {
				_, _ = w.Write([]byte(`{"present":true,"url":"` + fake.uploadURL + `"}`))
				return
			}
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
		call := fake.statusCalls.Add(1)
		if fake.onStatusCall != nil {
			fake.onStatusCall(call)
		}
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
		readyAfter := fake.readyAfterCalls.Load()
		if fake.statusReady.Load() || (readyAfter > 0 && call > readyAfter) {
			_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","status":"ready","logEntries":[],"logs":[]}`))
			return
		}
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

func TestBuild_ForceUploadWithPresentAndNilURLDoesNotFail(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusReady.Store(true)
	fake.present.Store(true)

	dir := t.TempDir()
	writeFile(t, dir, "app.txt", "hello")

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").WithContext(dir).Copy("app.txt", "/app/", WithCopyForceUpload())
	if _, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond}); err != nil {
		t.Fatalf("ForceUpload with present=true and url=nil should not fail: %v", err)
	}
	if fake.sawUpload.Load() != 0 {
		t.Fatalf("expected 0 uploads when server omitted URL for present=true, got %d", fake.sawUpload.Load())
	}

	// When server returns present=true WITH an upload URL, ForceUpload uploads.
	fake.presentWithURL.Store(true)
	if _, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond}); err != nil {
		t.Fatalf("ForceUpload with present=true and url!=nil: %v", err)
	}
	if fake.sawUpload.Load() != 1 {
		t.Fatalf("expected 1 upload when ForceUpload and url!=nil, got %d", fake.sawUpload.Load())
	}
}

func TestBuild_UploadLinkMissingURLFailsFast(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","aliases":[],"names":["demo"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/templates/tpl_1/files/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"present":false}`)) // no url field
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	writeFile(t, dir, "app.txt", "hello")

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").WithContext(dir).Copy("app.txt", "/app/")
	_, err := cli.Build(context.Background(), tpl, BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond})
	if err == nil {
		t.Fatal("expected UploadError when server omits url")
	}
	if _, ok := errors.AsType[*UploadError](err); !ok {
		t.Fatalf("expected *UploadError, got %T: %v", err, err)
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
	be, ok := errors.AsType[*BuildError](err)
	if !ok {
		t.Fatalf("expected *BuildError, got %T: %v", err, err)
	}
	if !strings.Contains(be.Error(), "boom at step 3") {
		t.Fatalf("err message missing reason: %s", be.Error())
	}
}

func TestBuild_DrainsFinalLogPagesOnTerminalStatus(t *testing.T) {
	allLogs := make([]map[string]any, 125)
	for i := range allLogs {
		allLogs[i] = map[string]any{
			"timestamp": "2026-01-01T00:00:00Z",
			"level":     "info",
			"message":   fmt.Sprintf("line-%03d", i),
		}
	}

	var statusCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_drain","buildID":"bld_drain","aliases":[],"names":["drain"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_drain/builds/bld_drain", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/templates/tpl_drain/builds/bld_drain/status", func(w http.ResponseWriter, r *http.Request) {
		statusCalls.Add(1)
		offset, _ := strconv.Atoi(r.URL.Query().Get("logsOffset"))
		var page []map[string]any
		if offset < len(allLogs) {
			end := min(offset+100, len(allLogs))
			page = allLogs[offset:end]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"templateID": "tpl_drain",
			"buildID":    "bld_drain",
			"status":     "error",
			"logEntries": page,
			"logs":       []string{},
			"reason":     map[string]any{"message": "failed at end", "step": "2"},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	var seen []string
	_, err := cli.Build(context.Background(), New().FromImage("alpine:3").RunCmd("false"), BuildOptions{
		Name:         "drain",
		PollInterval: 5 * time.Millisecond,
		OnBuildLogs: func(le LogEntry) {
			seen = append(seen, le.Message)
		},
	})
	be, ok := errors.AsType[*BuildError](err)
	if !ok {
		t.Fatalf("expected *BuildError, got %T: %v", err, err)
	}
	if len(seen) != 125 {
		t.Fatalf("expected all 125 logs drained, got %d (statusCalls=%d)", len(seen), statusCalls.Load())
	}
	if len(be.LogTail) != logTailLimit || be.LogTail[len(be.LogTail)-1].Message != "line-124" {
		t.Fatalf("expected LogTail ending in line-124, got len=%d last=%+v", len(be.LogTail), be.LogTail)
	}
}

func TestBuild_TerminalLogDrainStopsWhenServerIgnoresOffset(t *testing.T) {
	staticPage := make([]map[string]any, 100)
	for i := range staticPage {
		staticPage[i] = map[string]any{
			"timestamp": "2026-01-01T00:00:00Z",
			"level":     "info",
			"message":   fmt.Sprintf("repeat-%03d", i),
		}
	}
	var statusCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_rep","buildID":"bld_rep","aliases":[],"names":["rep"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_rep/builds/bld_rep", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/templates/tpl_rep/builds/bld_rep/status", func(w http.ResponseWriter, _ *http.Request) {
		statusCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"templateID": "tpl_rep",
			"buildID":    "bld_rep",
			"status":     "ready",
			"logEntries": staticPage,
			"logs":       []string{},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	info, err := cli.Build(context.Background(), New().FromImage("alpine:3").RunCmd("true"), BuildOptions{
		Name:         "rep",
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil || info == nil {
		t.Fatalf("expected clean finish, got info=%+v err=%v", info, err)
	}
	if statusCalls.Load() != 2 {
		t.Fatalf("expected drain to stop after detecting repeated page (2 calls), got %d", statusCalls.Load())
	}
}

func TestBuildStream_EmitsLogsThenDone(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	// First poll returns "building" with a log; second poll returns "ready".
	fake.readyAfterCalls.Store(1)

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
	srv, fake := newFakeTemplateServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel deterministically as soon as the first status poll arrives.
	fake.onStatusCall = func(call int32) {
		if call == 1 {
			cancel()
		}
	}

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")

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
	if fake.statusCalls.Load() != 0 {
		t.Fatalf("BuildInBackground should not poll, got %d status calls", fake.statusCalls.Load())
	}
}

func TestGetBuildStatus_AndValidation(t *testing.T) {
	srv, fake := newFakeTemplateServer(t)
	fake.statusReady.Store(true)
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})

	st, err := cli.GetBuildStatus(context.Background(), BuildInfo{TemplateID: "tpl_1", BuildID: "bld_1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != BuildStatusReady || st.TemplateID != "tpl_1" || st.BuildID != "bld_1" {
		t.Fatalf("unexpected status: %+v", st)
	}

	if _, err := cli.GetBuildStatus(context.Background(), BuildInfo{TemplateID: "tpl_1", BuildID: "bld_1"}, -1); err == nil {
		t.Fatal("expected error for negative LogsOffset")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("negative LogsOffset: want *InvalidArgumentError, got %T", err)
	}

	if _, err := cli.GetBuildStatus(context.Background(), BuildInfo{TemplateID: "..", BuildID: "bld_1"}, 0); err == nil {
		t.Fatal("expected error for invalid templateID")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("invalid templateID: want *InvalidArgumentError, got %T", err)
	}
}

func TestBuildV2_ReadyAndFinalBuilderHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_v2","buildID":"bld_v2"}`))
	})
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_v3","buildID":"bld_v3","aliases":[],"names":["demo"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_v2/builds/bld_v2", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v2/templates/tpl_v3/builds/bld_v3", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/templates/tpl_v2/builds/bld_v2/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_v2","buildID":"bld_v2","status":"ready","logEntries":[],"logs":[]}`))
	})
	mux.HandleFunc("/templates/tpl_v3/builds/bld_v3/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_v3","buildID":"bld_v3","status":"ready","logEntries":[],"logs":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	final := New().FromImage("alpine:3").RunCmd("echo ok").SetStartCmd("sleep 1", ReadyCmd{})
	opts := BuildOptions{Name: "demo", PollInterval: 5 * time.Millisecond}

	if info, err := final.BuildV2(context.Background(), cli, opts); err != nil || info.TemplateID != "tpl_v2" {
		t.Fatalf("FinalBuilder.BuildV2: info=%+v err=%v", info, err)
	}
	if info, err := final.BuildInBackground(context.Background(), cli, opts); err != nil || info.TemplateID != "tpl_v3" {
		t.Fatalf("FinalBuilder.BuildInBackground: info=%+v err=%v", info, err)
	}
	if info, err := final.BuildInBackgroundV2(context.Background(), cli, opts); err != nil || info.TemplateID != "tpl_v2" {
		t.Fatalf("FinalBuilder.BuildInBackgroundV2: info=%+v err=%v", info, err)
	}
	chV3, err := final.BuildStream(context.Background(), cli, opts)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range chV3 {
		if ev.Err != nil {
			t.Fatalf("FinalBuilder.BuildStream err: %v", ev.Err)
		}
	}
	chV2, err := final.BuildStreamV2(context.Background(), cli, opts)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range chV2 {
		if ev.Err != nil {
			t.Fatalf("FinalBuilder.BuildStreamV2 err: %v", ev.Err)
		}
	}
}

func TestBuild_ValidationErrorEmptyName(t *testing.T) {
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: "http://unused"})
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	_, err := cli.Build(context.Background(), tpl, BuildOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("want InvalidArgumentError, got %T", err)
	}
}
