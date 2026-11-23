package e2b

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
	"github.com/eric642/e2b-go-sdk/internal/envd/filesystem/filesystemconnect"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

func newCustomFsSandbox(t *testing.T, envdVer string, cfgMod func(*Config), fsHandler filesystemconnect.FilesystemHandler, httpMux func(*http.ServeMux)) *Sandbox {
	t.Helper()
	mux := http.NewServeMux()
	if fsHandler != nil {
		fsPath, handler := filesystemconnect.NewFilesystemHandler(fsHandler)
		mux.Handle(fsPath, handler)
	}
	if httpMux != nil {
		httpMux(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := Config{
		APIKey:     "k",
		Domain:     "example.com",
		SandboxURL: srv.URL,
	}
	if cfgMod != nil {
		cfgMod(&cfg)
	}
	cfg = cfg.Resolve()

	envdAuth := transport.EnvdAuth{
		Token:     "envd-tok-1",
		User:      resolveEnvdUser(envdVer, ""),
		SandboxID: "sbx-fs-1",
		Headers:   cfg.envdHeaders(),
	}
	hc := cfg.DataPlaneHTTPClient()
	envdCli, err := transport.NewEnvdClients(srv.URL, hc, envdAuth)
	if err != nil {
		t.Fatal(err)
	}
	sbx := &Sandbox{
		ID:              "sbx-fs-1",
		Domain:          "example.com",
		EnvdVersion:     envdVer,
		EnvdAccessToken: "envd-tok-1",
		cfg:             cfg,
		envd:            envdCli,
		httpCli:         hc,
	}
	sbx.Files = &Filesystem{sbx: sbx}
	return sbx
}

type customWatchFsSvc struct {
	filesystemconnect.UnimplementedFilesystemHandler
	watchFn func(context.Context, *connect.Request[fspb.WatchDirRequest], *connect.ServerStream[fspb.WatchDirResponse]) error
	statFn  func(context.Context, *connect.Request[fspb.StatRequest]) (*connect.Response[fspb.StatResponse], error)
	listFn  func(context.Context, *connect.Request[fspb.ListDirRequest]) (*connect.Response[fspb.ListDirResponse], error)
	moveFn  func(context.Context, *connect.Request[fspb.MoveRequest]) (*connect.Response[fspb.MoveResponse], error)
	remFn   func(context.Context, *connect.Request[fspb.RemoveRequest]) (*connect.Response[fspb.RemoveResponse], error)
}

func (s *customWatchFsSvc) WatchDir(ctx context.Context, req *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
	if s.watchFn != nil {
		return s.watchFn(ctx, req, stream)
	}
	return s.UnimplementedFilesystemHandler.WatchDir(ctx, req, stream)
}

func (s *customWatchFsSvc) Stat(ctx context.Context, req *connect.Request[fspb.StatRequest]) (*connect.Response[fspb.StatResponse], error) {
	if s.statFn != nil {
		return s.statFn(ctx, req)
	}
	return s.UnimplementedFilesystemHandler.Stat(ctx, req)
}

func (s *customWatchFsSvc) ListDir(ctx context.Context, req *connect.Request[fspb.ListDirRequest]) (*connect.Response[fspb.ListDirResponse], error) {
	if s.listFn != nil {
		return s.listFn(ctx, req)
	}
	return s.UnimplementedFilesystemHandler.ListDir(ctx, req)
}

func (s *customWatchFsSvc) Move(ctx context.Context, req *connect.Request[fspb.MoveRequest]) (*connect.Response[fspb.MoveResponse], error) {
	if s.moveFn != nil {
		return s.moveFn(ctx, req)
	}
	return s.UnimplementedFilesystemHandler.Move(ctx, req)
}

func (s *customWatchFsSvc) Remove(ctx context.Context, req *connect.Request[fspb.RemoveRequest]) (*connect.Response[fspb.RemoveResponse], error) {
	if s.remFn != nil {
		return s.remFn(ctx, req)
	}
	return s.UnimplementedFilesystemHandler.Remove(ctx, req)
}

func TestWatchDirRequestTimeoutBoundsHandshakeOnly(t *testing.T) {
	sendDelayed := make(chan struct{})
	svc := &customWatchFsSvc{
		watchFn: func(ctx context.Context, _ *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
			if err := stream.Send(&fspb.WatchDirResponse{
				Event: &fspb.WatchDirResponse_Start{Start: &fspb.WatchDirResponse_StartEvent{}},
			}); err != nil {
				return err
			}
			select {
			case <-sendDelayed:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := stream.Send(&fspb.WatchDirResponse{
				Event: &fspb.WatchDirResponse_Filesystem{
					Filesystem: &fspb.FilesystemEvent{
						Name: "after-timeout.txt",
						Type: fspb.EventType_EVENT_TYPE_WRITE,
					},
				},
			}); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		},
	}

	sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
	ctx := context.Background()
	const reqTimeout = 50 * time.Millisecond

	wh, err := sbx.Files.WatchWithOptions(ctx, "/watched", WatchOptions{
		RequestTimeout: reqTimeout,
	})
	if err != nil {
		t.Fatalf("WatchWithOptions: %v", err)
	}

	// Wait well past RequestTimeout before triggering the first filesystem event.
	timer := time.NewTimer(reqTimeout * 3)
	defer timer.Stop()
	select {
	case <-wh.done:
		t.Fatalf("watch stream terminated prematurely after RequestTimeout; err=%v", wh.Err())
	case <-timer.C:
	}

	close(sendDelayed)
	select {
	case ev, ok := <-wh.Events():
		if !ok {
			t.Fatalf("events channel closed early; err=%v", wh.Err())
		}
		if ev.Name != "after-timeout.txt" || ev.Type != FsEventWrite {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event past RequestTimeout")
	}

	if err := wh.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if wh.Err() != nil {
		t.Fatalf("clean Stop should leave Err()==nil, got %v", wh.Err())
	}
}

func TestWatchDirRequestTimeoutFailsWhenHandshakeHangs(t *testing.T) {
	serverCanceled := make(chan struct{})
	svc := &customWatchFsSvc{
		watchFn: func(ctx context.Context, _ *connect.Request[fspb.WatchDirRequest], _ *connect.ServerStream[fspb.WatchDirResponse]) error {
			<-ctx.Done()
			close(serverCanceled)
			return ctx.Err()
		},
	}

	sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
	start := time.Now()
	_, err := sbx.Files.WatchWithOptions(context.Background(), "/hung", WatchOptions{
		RequestTimeout: 50 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want *TimeoutError when WatchDir handshake hangs, got %T: %v", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want error wrapping context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("WatchDir handshake took too long to time out: %v", elapsed)
	}
	select {
	case <-serverCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server WatchDir handler did not observe cancellation")
	}
}

func TestWatchDirStartHandshakeEdgeCases(t *testing.T) {
	t.Run("rpc_not_found", func(t *testing.T) {
		svc := &customWatchFsSvc{
			watchFn: func(context.Context, *connect.Request[fspb.WatchDirRequest], *connect.ServerStream[fspb.WatchDirResponse]) error {
				return connect.NewError(connect.CodeNotFound, errors.New("dir does not exist"))
			},
		}
		sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
		_, err := sbx.Files.Watch(context.Background(), "/missing", false)
		if _, ok := errors.AsType[*FileNotFoundError](err); !ok {
			t.Fatalf("want *FileNotFoundError, got %T: %v", err, err)
		}
	})

	t.Run("premature_eof", func(t *testing.T) {
		svc := &customWatchFsSvc{
			watchFn: func(context.Context, *connect.Request[fspb.WatchDirRequest], *connect.ServerStream[fspb.WatchDirResponse]) error {
				return nil
			},
		}
		sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
		_, err := sbx.Files.Watch(context.Background(), "/empty", false)
		if _, ok := errors.AsType[*SandboxError](err); !ok {
			t.Fatalf("want *SandboxError on premature EOF, got %T: %v", err, err)
		}
		if !errors.Is(err, io.EOF) {
			t.Fatalf("want error wrapping io.EOF, got %v", err)
		}
	})

	t.Run("first_event_filesystem_without_start", func(t *testing.T) {
		svc := &customWatchFsSvc{
			watchFn: func(ctx context.Context, _ *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
				if err := stream.Send(&fspb.WatchDirResponse{
					Event: &fspb.WatchDirResponse_Filesystem{
						Filesystem: &fspb.FilesystemEvent{
							Name: "immediate.txt",
							Type: fspb.EventType_EVENT_TYPE_CREATE,
						},
					},
				}); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			},
		}
		sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
		wh, err := sbx.Files.Watch(context.Background(), "/dir", false)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		select {
		case ev, ok := <-wh.Events():
			if !ok || ev.Name != "immediate.txt" || ev.Type != FsEventCreate {
				t.Fatalf("unexpected initial event: ok=%v ev=%+v", ok, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for initial filesystem event")
		}
		if err := wh.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
}

func TestWatchHandleDoneClosesAfterEvents(t *testing.T) {
	svc := &customWatchFsSvc{
		watchFn: func(ctx context.Context, _ *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
			if err := stream.Send(&fspb.WatchDirResponse{
				Event: &fspb.WatchDirResponse_Start{Start: &fspb.WatchDirResponse_StartEvent{}},
			}); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		},
	}
	sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
	wh, err := sbx.Files.Watch(context.Background(), "/dir", false)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := wh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// w.done must close AFTER w.events, so Events() must already be closed now.
	select {
	case ev, ok := <-wh.Events():
		if ok {
			t.Fatalf("expected Events() closed after Stop(), got event %+v", ev)
		}
	default:
		t.Fatal("Events() was not yet closed when Stop() returned")
	}
}

func TestWatchHandleFullBufferContextCancelAndStop(t *testing.T) {
	makeBurstSvc := func() *customWatchFsSvc {
		return &customWatchFsSvc{
			watchFn: func(ctx context.Context, _ *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
				if err := stream.Send(&fspb.WatchDirResponse{
					Event: &fspb.WatchDirResponse_Start{Start: &fspb.WatchDirResponse_StartEvent{}},
				}); err != nil {
					return err
				}
				for range 64 {
					if err := stream.Send(&fspb.WatchDirResponse{
						Event: &fspb.WatchDirResponse_Filesystem{
							Filesystem: &fspb.FilesystemEvent{
								Name: "burst.txt",
								Type: fspb.EventType_EVENT_TYPE_WRITE,
							},
						},
					}); err != nil {
						return err
					}
				}
				<-ctx.Done()
				return nil
			},
		}
	}

	waitBufferFull := func(t *testing.T, wh *WatchHandle) {
		t.Helper()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(2 * time.Second)
		defer timeout.Stop()
		for len(wh.events) < cap(wh.events) {
			select {
			case <-ticker.C:
			case <-wh.done:
				t.Fatalf("watch ended before buffer filled: err=%v", wh.Err())
			case <-timeout.C:
				t.Fatalf("timed out waiting for watch buffer to fill (len=%d cap=%d)", len(wh.events), cap(wh.events))
			}
		}
	}

	t.Run("context_cancel_records_timeout_error", func(t *testing.T) {
		sbx := newCustomFsSandbox(t, "0.6.13", nil, makeBurstSvc(), nil)
		watchCtx, cancel := context.WithCancel(context.Background())
		wh, err := sbx.Files.Watch(watchCtx, "/burst", false)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		waitBufferFull(t, wh)
		cancel()

		select {
		case <-wh.done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for watch goroutine to exit after cancel")
		}
		// Drain buffered events and verify channel closes cleanly.
		for range wh.Events() {
		}
		gotErr := wh.Err()
		if _, ok := errors.AsType[*TimeoutError](gotErr); !ok {
			t.Fatalf("want *TimeoutError when ctx canceled with full buffer, got %T: %v", gotErr, gotErr)
		}
		if !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("want error wrapping context.Canceled, got %v", gotErr)
		}
	})

	t.Run("stop_with_full_buffer_leaves_nil_error", func(t *testing.T) {
		sbx := newCustomFsSandbox(t, "0.6.13", nil, makeBurstSvc(), nil)
		wh, err := sbx.Files.Watch(context.Background(), "/burst", false)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		waitBufferFull(t, wh)
		if err := wh.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		for range wh.Events() {
		}
		if wh.Err() != nil {
			t.Fatalf("clean Stop with full buffer should leave Err()==nil, got %v", wh.Err())
		}
	})
}

func TestWatchHandleFsEventFromPBAllTypes(t *testing.T) {
	cases := []struct {
		in   fspb.EventType
		want FilesystemEventType
	}{
		{fspb.EventType_EVENT_TYPE_CREATE, FsEventCreate},
		{fspb.EventType_EVENT_TYPE_WRITE, FsEventWrite},
		{fspb.EventType_EVENT_TYPE_REMOVE, FsEventRemove},
		{fspb.EventType_EVENT_TYPE_RENAME, FsEventRename},
		{fspb.EventType_EVENT_TYPE_CHMOD, FsEventChmod},
		{fspb.EventType_EVENT_TYPE_UNSPECIFIED, FsEventUnspecified},
		{fspb.EventType(999), FsEventUnspecified},
	}
	for _, tc := range cases {
		if got := fsEventFromPB(tc.in); got != tc.want {
			t.Errorf("fsEventFromPB(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFilesystemUploadNilReaderForceMultipartAndLegacyEnvd(t *testing.T) {
	cases := []struct {
		name           string
		envdVer        string
		forceMultipart bool
		wantMultipart  bool
	}{
		{"octet_stream_nil", "0.6.13", false, false},
		{"force_multipart_nil", "0.6.13", true, true},
		{"legacy_envd_nil", "0.5.6", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			var gotCT string
			var gotPartName string
			sbx := newCustomFsSandbox(t, tc.envdVer, nil, nil, func(mux *http.ServeMux) {
				mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
					gotCT = r.Header.Get("Content-Type")
					if strings.HasPrefix(gotCT, "multipart/form-data") {
						_, params, err := mime.ParseMediaType(gotCT)
						if err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						mr := multipart.NewReader(r.Body, params["boundary"])
						part, err := mr.NextPart()
						if err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						gotPartName = part.FormName()
						gotBody, _ = io.ReadAll(part)
						_, _ = io.Copy(io.Discard, r.Body)
					} else {
						gotBody, _ = io.ReadAll(r.Body)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode([]map[string]any{{
						"name": "empty.txt",
						"path": r.URL.Query().Get("path"),
						"type": "file",
					}})
				})
			})

			wi, err := sbx.Files.Write(context.Background(), "/tmp/empty.txt", nil, FsOptions{
				ForceMultipart: tc.forceMultipart,
			})
			if err != nil {
				t.Fatalf("Write(nil): %v", err)
			}
			if wi.Path != "/tmp/empty.txt" {
				t.Fatalf("WriteInfo = %+v", wi)
			}
			if len(gotBody) != 0 {
				t.Fatalf("uploaded %d bytes for nil reader, want 0", len(gotBody))
			}
			if tc.wantMultipart {
				if !strings.HasPrefix(gotCT, "multipart/form-data") || gotPartName != "file" {
					t.Fatalf("expected multipart file part, got Content-Type=%q part=%q", gotCT, gotPartName)
				}
			} else if gotCT != "application/octet-stream" {
				t.Fatalf("Content-Type = %q, want application/octet-stream", gotCT)
			}
		})
	}
}

type raceCheckReader struct {
	remaining   int
	afterReturn bool // mutated by test right after Write returns to catch background Read races under -race
}

func (r *raceCheckReader) Read(p []byte) (int, error) {
	if r.afterReturn {
		return 0, io.EOF
	}
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range n {
		p[i] = 'a'
	}
	r.remaining -= n
	return n, nil
}

func TestFilesystemUploadMultipartEarlyRejection401NoRace(t *testing.T) {
	sbx := newCustomFsSandbox(t, "0.6.13", nil, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("/files", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
		})
	})

	reader := &raceCheckReader{remaining: 8 << 20} // 8 MiB forces multiple pipe writes
	_, err := sbx.Files.Write(context.Background(), "/tmp/big.bin", reader, FsOptions{
		ForceMultipart: true,
	})
	// Immediately mutate reader after Write returns; under -race this catches
	// any background multipart goroutine still reading caller's io.Reader.
	reader.afterReturn = true

	if _, ok := errors.AsType[*AuthenticationError](err); !ok {
		t.Fatalf("want *AuthenticationError on 401, got %T: %v", err, err)
	}
}

func TestFilesystemUploadMultipartEarly401WithBlockingReaderReturnsPromptly(t *testing.T) {
	newReject401Sbx := func(t *testing.T, disableDefaultTimeout bool) *Sandbox {
		t.Helper()
		return newCustomFsSandbox(t, "0.6.13", func(c *Config) {
			c.RequestTimeoutDisabled = disableDefaultTimeout
		}, nil, func(mux *http.ServeMux) {
			mux.HandleFunc("/files", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Connection", "close")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			})
		})
	}

	t.Run("bounded_by_request_timeout", func(t *testing.T) {
		sbx := newReject401Sbx(t, false)
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })

		start := time.Now()
		_, err := sbx.Files.Write(context.Background(), "/tmp/blocked.bin", pr, FsOptions{
			ForceMultipart: true,
			RequestTimeout: 80 * time.Millisecond,
		})
		elapsed := time.Since(start)
		if _, ok := errors.AsType[*AuthenticationError](err); !ok {
			t.Fatalf("want *AuthenticationError, got %T: %v", err, err)
		}
		if elapsed > 1*time.Second {
			t.Fatalf("Write with blocking reader + 401 did not return promptly: %v", elapsed)
		}
	})

	t.Run("bounded_by_pipe_wait_cap_without_deadline", func(t *testing.T) {
		sbx := newReject401Sbx(t, true)
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })

		start := time.Now()
		_, err := sbx.Files.Write(context.Background(), "/tmp/blocked-nodeadline.bin", pr, FsOptions{
			ForceMultipart: true,
		})
		elapsed := time.Since(start)
		if _, ok := errors.AsType[*AuthenticationError](err); !ok {
			t.Fatalf("want *AuthenticationError, got %T: %v", err, err)
		}
		if elapsed > 2500*time.Millisecond {
			t.Fatalf("Write with blocking reader + no deadline hung past cap: %v", elapsed)
		}
	})
}

func TestFilesystemExistsIsDirRemoveMove(t *testing.T) {
	var lastRemoved string
	var lastMoveSrc, lastMoveDst string

	svc := &customWatchFsSvc{
		statFn: func(_ context.Context, req *connect.Request[fspb.StatRequest]) (*connect.Response[fspb.StatResponse], error) {
			switch req.Msg.GetPath() {
			case "/dir":
				return connect.NewResponse(&fspb.StatResponse{
					Entry: &fspb.EntryInfo{Name: "dir", Path: "/dir", Type: fspb.FileType_FILE_TYPE_DIRECTORY},
				}), nil
			case "/file.txt":
				return connect.NewResponse(&fspb.StatResponse{
					Entry: &fspb.EntryInfo{Name: "file.txt", Path: "/file.txt", Type: fspb.FileType_FILE_TYPE_FILE},
				}), nil
			case "/denied":
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
			default:
				return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
			}
		},
		remFn: func(_ context.Context, req *connect.Request[fspb.RemoveRequest]) (*connect.Response[fspb.RemoveResponse], error) {
			if req.Msg.GetPath() == "/missing" {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
			}
			lastRemoved = req.Msg.GetPath()
			return connect.NewResponse(&fspb.RemoveResponse{}), nil
		},
		moveFn: func(_ context.Context, req *connect.Request[fspb.MoveRequest]) (*connect.Response[fspb.MoveResponse], error) {
			if req.Msg.GetSource() == "/missing" {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
			}
			lastMoveSrc = req.Msg.GetSource()
			lastMoveDst = req.Msg.GetDestination()
			return connect.NewResponse(&fspb.MoveResponse{}), nil
		},
	}

	sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
	ctx := context.Background()

	// Exists
	ok, err := sbx.Files.Exists(ctx, "/file.txt", FsOptions{})
	if err != nil || !ok {
		t.Fatalf("Exists(/file.txt) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = sbx.Files.Exists(ctx, "/missing", FsOptions{})
	if err != nil || ok {
		t.Fatalf("Exists(/missing) = (%v, %v), want (false, nil)", ok, err)
	}
	ok, err = sbx.Files.Exists(ctx, "/denied", FsOptions{})
	if ok {
		t.Fatalf("Exists(/denied) should return false")
	}
	if _, isAuth := errors.AsType[*AuthenticationError](err); !isAuth {
		t.Fatalf("Exists(/denied) err = %T: %v, want *AuthenticationError", err, err)
	}

	// IsDir
	isDir, err := sbx.Files.IsDir(ctx, "/dir", FsOptions{})
	if err != nil || !isDir {
		t.Fatalf("IsDir(/dir) = (%v, %v), want (true, nil)", isDir, err)
	}
	isDir, err = sbx.Files.IsDir(ctx, "/file.txt", FsOptions{})
	if err != nil || isDir {
		t.Fatalf("IsDir(/file.txt) = (%v, %v), want (false, nil)", isDir, err)
	}
	if _, err := sbx.Files.IsDir(ctx, "/missing", FsOptions{}); err == nil {
		t.Fatal("IsDir(/missing) should fail")
	} else if _, isFNF := errors.AsType[*FileNotFoundError](err); !isFNF {
		t.Fatalf("IsDir(/missing) err = %T: %v, want *FileNotFoundError", err, err)
	}

	// Remove
	if err := sbx.Files.Remove(ctx, "/file.txt", FsOptions{}); err != nil {
		t.Fatalf("Remove(/file.txt): %v", err)
	}
	if lastRemoved != "/file.txt" {
		t.Fatalf("lastRemoved = %q, want /file.txt", lastRemoved)
	}
	if err := sbx.Files.Remove(ctx, "/missing", FsOptions{}); err == nil {
		t.Fatal("Remove(/missing) should fail")
	} else if _, isFNF := errors.AsType[*FileNotFoundError](err); !isFNF {
		t.Fatalf("Remove(/missing) err = %T: %v, want *FileNotFoundError", err, err)
	}

	// Move
	if err := sbx.Files.Move(ctx, "/file.txt", "/moved.txt", FsOptions{}); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if lastMoveSrc != "/file.txt" || lastMoveDst != "/moved.txt" {
		t.Fatalf("Move sent (%q -> %q), want (/file.txt -> /moved.txt)", lastMoveSrc, lastMoveDst)
	}
	if err := sbx.Files.Move(ctx, "/missing", "/moved.txt", FsOptions{}); err == nil {
		t.Fatal("Move(/missing) should fail")
	} else if _, isFNF := errors.AsType[*FileNotFoundError](err); !isFNF {
		t.Fatalf("Move(/missing) err = %T: %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemListDepthValidation(t *testing.T) {
	var gotDepth uint32
	svc := &customWatchFsSvc{
		listFn: func(_ context.Context, req *connect.Request[fspb.ListDirRequest]) (*connect.Response[fspb.ListDirResponse], error) {
			gotDepth = req.Msg.GetDepth()
			return connect.NewResponse(&fspb.ListDirResponse{}), nil
		},
	}
	sbx := newCustomFsSandbox(t, "0.6.13", nil, svc, nil)
	ctx := context.Background()

	if _, err := sbx.Files.List(ctx, "/d", FsOptions{Depth: 0}); err != nil {
		t.Fatalf("List(Depth=0): %v", err)
	}
	if gotDepth != 1 {
		t.Fatalf("Depth=0 sent %d, want 1", gotDepth)
	}

	if _, err := sbx.Files.List(ctx, "/d", FsOptions{Depth: 3}); err != nil {
		t.Fatalf("List(Depth=3): %v", err)
	}
	if gotDepth != 3 {
		t.Fatalf("Depth=3 sent %d, want 3", gotDepth)
	}

	if _, err := sbx.Files.List(ctx, "/d", FsOptions{Depth: -1}); err == nil {
		t.Fatal("List(Depth=-1) should fail")
	} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("List(Depth=-1) err = %T, want *InvalidArgumentError", err)
	}

	if int64(math.MaxInt) > math.MaxUint32 {
		tooBig := int(uint64(math.MaxUint32) + 1)
		if _, err := sbx.Files.List(ctx, "/d", FsOptions{Depth: tooBig}); err == nil {
			t.Fatal("List(Depth>MaxUint32) should fail")
		} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Fatalf("List(Depth>MaxUint32) err = %T, want *InvalidArgumentError", err)
		}
	}
}

func TestFilesystemBufferedWritesRespectConfigRequestTimeout(t *testing.T) {
	sbx := newCustomFsSandbox(t, "0.6.13", func(c *Config) {
		c.RequestTimeout = 40 * time.Millisecond
	}, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			timer := time.NewTimer(150 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name": "ok.txt",
				"path": r.URL.Query().Get("path"),
				"type": "file",
			}})
		})
	})

	ctx := context.Background()
	if _, err := sbx.Files.WriteBytes(ctx, "/tmp/b.txt", []byte("hi"), FsOptions{}); err == nil {
		t.Fatal("WriteBytes should time out via Config.RequestTimeout")
	} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("WriteBytes err = %T: %v, want *TimeoutError", err, err)
	}

	if _, err := sbx.Files.WriteString(ctx, "/tmp/s.txt", "hi", FsOptions{}); err == nil {
		t.Fatal("WriteString should time out via Config.RequestTimeout")
	} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("WriteString err = %T: %v, want *TimeoutError", err, err)
	}

	if _, err := sbx.Files.WriteFiles(ctx, []WriteEntry{{Path: "/tmp/f.txt", Data: []byte("hi")}}, FsOptions{}); err == nil {
		t.Fatal("WriteFiles should time out via Config.RequestTimeout")
	} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("WriteFiles err = %T: %v, want *TimeoutError", err, err)
	}

	// Streaming Write does not apply default Config.RequestTimeout unless explicit.
	if _, err := sbx.Files.Write(ctx, "/tmp/stream.txt", strings.NewReader("hi"), FsOptions{}); err != nil {
		t.Fatalf("streaming Write without explicit timeout should succeed: %v", err)
	}
	if _, err := sbx.Files.Write(ctx, "/tmp/stream-to.txt", strings.NewReader("hi"), FsOptions{RequestTimeout: 40 * time.Millisecond}); err == nil {
		t.Fatal("streaming Write with explicit RequestTimeout should time out")
	} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("streaming Write explicit timeout err = %T: %v, want *TimeoutError", err, err)
	}
}

type errCloseReadCloser struct {
	io.Reader
	closeErr error
}

func (e *errCloseReadCloser) Close() error { return e.closeErr }

func TestFilesystemGzipStreamReadCloserJoinsErrors(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("hello"))
	_ = gw.Close()

	gz, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	bodyErr := errors.New("body close failed")
	canceled := false
	rc := &gzipStreamReadCloser{
		gz:     gz,
		body:   &errCloseReadCloser{Reader: bytes.NewReader(nil), closeErr: bodyErr},
		cancel: func() { canceled = true },
	}
	err = rc.Close()
	if !canceled {
		t.Fatal("gzipStreamReadCloser.Close did not invoke cancel")
	}
	if !errors.Is(err, bodyErr) {
		t.Fatalf("expected joined error to wrap bodyErr, got %v", err)
	}
}

func TestFilesystemSnapshotConsistencyUnderConcurrentReconnect(t *testing.T) {
	var mismatchCount atomic.Int32
	sbx := newCustomFsSandbox(t, "0.6.13", nil, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			tok := r.Header.Get("X-Access-Token")
			filePath := r.URL.Query().Get("path")
			user := r.URL.Query().Get("username")
			gotSig := r.URL.Query().Get("signature")
			op := SignatureRead
			if r.Method == http.MethodPost {
				op = SignatureWrite
			}
			wantSig, err := GetSignature(filePath, op, tok, SignatureOptions{User: user})
			if err != nil || gotSig != wantSig.Value {
				mismatchCount.Add(1)
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte("ok"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name": "f.txt",
				"path": filePath,
				"type": "file",
			}})
		})
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 60 {
			tok := "envd-tok-a"
			ver := "0.6.13"
			if i%2 == 0 {
				tok = "envd-tok-b"
				ver = "0.3.9"
			}
			dom := "example.com"
			_ = sbx.applyConnectedSandbox(&apiclient.Sandbox{
				SandboxID:       sbx.ID,
				TemplateID:      "base",
				ClientID:        "cli",
				Domain:          &dom,
				EnvdAccessToken: &tok,
				EnvdVersion:     ver,
			})
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 30 {
				if _, err := sbx.Files.ReadBytes(ctx, "/tmp/snap.txt", FsOptions{}); err != nil {
					t.Errorf("ReadBytes: %v", err)
					return
				}
				if _, err := sbx.Files.WriteBytes(ctx, "/tmp/snap.txt", []byte("x"), FsOptions{}); err != nil {
					t.Errorf("WriteBytes: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	if got := mismatchCount.Load(); got != 0 {
		t.Fatalf("observed %d URL-signature vs X-Access-Token snapshot mismatches under concurrent reconnect", got)
	}
}

func TestEnvdHTTPConnectionReuseDrainsBodies(t *testing.T) {
	sbx, _ := newEnvdTestHarness(t, "0.6.13")

	checkReused := func(name string, call func(context.Context) error) {
		t.Helper()
		// Prime connection pool.
		if err := call(context.Background()); err != nil {
			t.Fatalf("%s call 1: %v", name, err)
		}
		var reused bool
		traceCtx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				reused = info.Reused
			},
		})
		if err := call(traceCtx); err != nil {
			t.Fatalf("%s call 2: %v", name, err)
		}
		if !reused {
			t.Errorf("%s did not reuse keep-alive HTTP connection on second call", name)
		}
	}

	checkReused("GetEnvs", func(ctx context.Context) error {
		_, err := sbx.GetEnvs(ctx)
		return err
	})
	checkReused("GetEnvdMetrics", func(ctx context.Context) error {
		_, err := sbx.GetEnvdMetrics(ctx)
		return err
	})
	checkReused("Compose", func(ctx context.Context) error {
		_, err := sbx.Files.Compose(ctx, []string{"/a", "/b"}, "/out", FsOptions{})
		return err
	})
	checkReused("WriteBytes", func(ctx context.Context) error {
		_, err := sbx.Files.WriteBytes(ctx, "/tmp/reuse.txt", []byte("ok"), FsOptions{})
		return err
	})
}

func TestFilesystemReadStreamVsBufferedReadTimeout(t *testing.T) {
	sbx := newCustomFsSandbox(t, "0.6.13", func(c *Config) {
		c.RequestTimeout = 40 * time.Millisecond
	}, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
			timer := time.NewTimer(120 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte("streamed-content"))
		})
	})

	ctx := context.Background()
	if _, err := sbx.Files.Read(ctx, "/tmp/slow.txt", FsOptions{}); err == nil {
		t.Fatal("buffered Read should time out via Config.RequestTimeout")
	} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("buffered Read err = %T: %v, want *TimeoutError", err, err)
	}

	rc, err := sbx.Files.ReadStream(ctx, "/tmp/slow.txt", FsOptions{})
	if err != nil {
		t.Fatalf("ReadStream without explicit RequestTimeout should succeed: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("io.ReadAll(ReadStream): %v", err)
	}
	if string(data) != "streamed-content" {
		t.Fatalf("ReadStream data = %q, want streamed-content", data)
	}
}

func newEnvdHTTPSandbox(t *testing.T, envdVersion string, h http.Handler) *Sandbox {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{Domain: "example.com", SandboxURL: srv.URL}.resolve()
	hc := cfg.DataPlaneHTTPClient()
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: envdVersion,
		cfg: cfg, httpCli: hc,
	}
	envd, err := transport.NewEnvdClients(srv.URL, hc, transport.EnvdAuth{
		User:      resolveEnvdUser(sbx.envdVersionForGating(), ""),
		SandboxID: sbx.ID,
		Headers:   cfg.envdHeaders(),
	})
	if err != nil {
		t.Fatalf("NewEnvdClients: %v", err)
	}
	sbx.envd = envd
	return sbx
}

func TestWriteUsesOctetStreamForNewEnvd(t *testing.T) {
	sbx := newEnvdHTTPSandbox(t, "0.5.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type=%q want application/octet-stream", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello" {
			t.Errorf("body=%q want hello", body)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"name": "p", "path": "/p", "type": "file"},
		})
	}))
	f := &Filesystem{sbx: sbx}
	info, err := f.WriteString(context.Background(), "/p", "hello", FsOptions{})
	if err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if info.Name != "p" || info.Path != "/p" || info.Type != EntryTypeFile {
		t.Fatalf("WriteInfo metadata not populated from response: %+v", info)
	}
}

func TestWriteUsesMultipartForOldEnvd(t *testing.T) {
	sbx := newEnvdHTTPSandbox(t, "0.5.6", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type=%q want multipart/form-data", ct)
		}
		// Destination is carried by the ?path= query param (matching the
		// reference SDKs); the multipart parser strips the directory from the
		// part filename per RFC 7578, so assert on path + payload instead.
		if got := r.URL.Query().Get("path"); got != "/p" {
			t.Errorf("path query=%q want /p", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		fh := r.MultipartForm.File["file"]
		if len(fh) != 1 {
			t.Fatalf("want exactly one multipart file field, got %d", len(fh))
		}
		fr, err := fh[0].Open()
		if err != nil {
			t.Fatalf("open multipart file: %v", err)
		}
		defer func() { _ = fr.Close() }()
		payload, _ := io.ReadAll(fr)
		if string(payload) != "hello" {
			t.Errorf("multipart payload=%q want hello", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	f := &Filesystem{sbx: sbx}
	if _, err := f.WriteString(context.Background(), "/p", "hello", FsOptions{}); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
}

func TestWriteFilesWritesEachEntry(t *testing.T) {
	var got []string
	sbx := newEnvdHTTPSandbox(t, "0.5.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		got = append(got, p)
		name := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			name = p[i+1:]
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"name": name, "path": p, "type": "file"},
		})
	}))
	f := &Filesystem{sbx: sbx}
	res, err := f.WriteFiles(context.Background(), []WriteEntry{
		{Path: "/a", Data: []byte("1")},
		{Path: "/b", Data: []byte("2")},
	}, FsOptions{})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if len(res) != 2 || res[0].Path != "/a" || res[1].Path != "/b" {
		t.Fatalf("results: %+v", res)
	}
	if res[0].Name != "a" || res[0].Type != EntryTypeFile || res[1].Name != "b" {
		t.Fatalf("WriteFiles metadata not populated: %+v", res)
	}
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("server saw paths: %v", got)
	}
}

func TestReadStreamMapsNotFoundToFileNotFoundError(t *testing.T) {
	sbx := newEnvdHTTPSandbox(t, "0.5.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	f := &Filesystem{sbx: sbx}
	_, err := f.Read(context.Background(), "/missing", FsOptions{})
	if err == nil {
		t.Fatal("Read of missing file should error")
	}
	if _, ok := errors.AsType[*FileNotFoundError](err); !ok {
		t.Fatalf("want *FileNotFoundError (file semantics), got %T: %v", err, err)
	}
}

func TestWatchRecursiveRejectedOnOldEnvd(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.1.3", cfg: Config{}}
	f := &Filesystem{sbx: sbx}
	_, err := f.Watch(context.Background(), "/dir", true)
	if err == nil {
		t.Fatal("recursive watch should error on envd<0.1.4")
	}
	if _, ok := errors.AsType[*TemplateError](err); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}
}

func TestListRejectsNegativeDepth(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.5.7", cfg: Config{}}
	f := &Filesystem{sbx: sbx}
	_, err := f.List(context.Background(), "/dir", FsOptions{Depth: -1})
	if err == nil {
		t.Fatal("negative depth should error")
	}
	if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T: %v", err, err)
	}
}
