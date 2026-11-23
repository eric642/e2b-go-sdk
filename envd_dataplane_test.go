package e2b

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
	"github.com/eric642/e2b-go-sdk/internal/envd/filesystem/filesystemconnect"
	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

type envdTestHarness struct {
	Server *httptest.Server

	mu            sync.Mutex
	lastHeaders   http.Header
	lastStartReq  *procpb.StartRequest
	lastWatchReq  *fspb.WatchDirRequest
	lastUpdateReq *procpb.UpdateRequest
	lastSendInput *procpb.SendInputRequest
	lastSignal    procpb.Signal
	streamInputs  []*procpb.ProcessInput
	killedPIDs    []uint32
	existingPIDs  map[uint32]bool
	existingTags  map[string]uint32
	composeBody   map[string]any
	holdStartCh   chan struct{}
	disconnectHit atomic.Bool
	disconnectCh  chan struct{}
}

func newEnvdTestHarness(t *testing.T, envdVer string) (*Sandbox, *envdTestHarness) {
	t.Helper()
	h := &envdTestHarness{
		existingPIDs: map[uint32]bool{42: true},
		existingTags: map[string]uint32{"boot-svc": 42},
		disconnectCh: make(chan struct{}, 1),
	}

	procPath, procHandler := processconnect.NewProcessHandler(&harnessProcSvc{h: h})
	fsPath, fsHandler := filesystemconnect.NewFilesystemHandler(&harnessFsSvc{h: h})

	mux := http.NewServeMux()
	mux.Handle(procPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.recordHeader(r.Header)
		procHandler.ServeHTTP(w, r)
	}))
	mux.Handle(fsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.recordHeader(r.Header)
		fsHandler.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/files", h.handleFiles)
	mux.HandleFunc("/files/compose", h.handleCompose)
	mux.HandleFunc("/envs", h.handleEnvs)
	mux.HandleFunc("/metrics", h.handleMetrics)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h.Server = srv

	cfg := Config{
		APIKey:              "cp-secret-key",
		Domain:              "example.com",
		SandboxURL:          srv.URL,
		Headers:             map[string]string{"X-Control-Only": "leak-me-not", "Authorization": "Bearer cp-bearer"},
		ExtraSandboxHeaders: map[string]string{"X-Sandbox-Extra": "ok", "Authorization": "Bearer injected-sbx"},
	}.Resolve()

	envdAuth := transport.EnvdAuth{
		Token:     "envd-tok-123",
		User:      resolveEnvdUser(envdVer, ""),
		SandboxID: "sbx-dp-1",
		Headers:   cfg.envdHeaders(),
	}
	envdCli, err := transport.NewEnvdClients(srv.URL, cfg.DataPlaneHTTPClient(), envdAuth)
	if err != nil {
		t.Fatal(err)
	}
	sbx := &Sandbox{
		ID:              "sbx-dp-1",
		Domain:          "example.com",
		EnvdVersion:     envdVer,
		EnvdAccessToken: "envd-tok-123",
		cfg:             cfg,
		envd:            envdCli,
		httpCli:         cfg.DataPlaneHTTPClient(),
	}
	sbx.Commands = &Commands{sbx: sbx}
	sbx.Pty = &Pty{sbx: sbx}
	sbx.Files = &Filesystem{sbx: sbx}
	sbx.Git = &Git{cmds: sbx.Commands}
	return sbx, h
}

func (h *envdTestHarness) recordHeader(hdr http.Header) {
	h.mu.Lock()
	h.lastHeaders = hdr.Clone()
	h.mu.Unlock()
}

func (h *envdTestHarness) headerSnapshot() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastHeaders.Clone()
}

func (h *envdTestHarness) handleFiles(w http.ResponseWriter, r *http.Request) {
	h.recordHeader(r.Header)
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("path") == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
			return
		}
		payload := []byte("downloaded-body-payload")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			_, _ = gw.Write(payload)
			_ = gw.Close()
			_, _ = w.Write(buf.Bytes())
			return
		}
		_, _ = w.Write(payload)
	case http.MethodPost:
		_, _ = io.ReadAll(r.Body)
		md := map[string]string{}
		for k, vals := range r.Header {
			if after, ok := strings.CutPrefix(k, "X-Metadata-"); ok && len(vals) > 0 {
				md[strings.ToLower(after)] = vals[0]
			}
		}
		resp := []map[string]any{{
			"name":     "uploaded.txt",
			"path":     r.URL.Query().Get("path"),
			"type":     "file",
			"metadata": md,
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func (h *envdTestHarness) handleCompose(w http.ResponseWriter, r *http.Request) {
	h.recordHeader(r.Header)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.mu.Lock()
	h.composeBody = body
	h.mu.Unlock()

	if body["destination"] == "/missing-src" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"source file not found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": "joined.bin",
		"path": body["destination"],
		"type": "file",
	})
}

func (h *envdTestHarness) handleEnvs(w http.ResponseWriter, r *http.Request) {
	h.recordHeader(r.Header)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"E2B_SANDBOX": "true",
		"APP_ENV":     "test",
	})
}

func (h *envdTestHarness) handleMetrics(w http.ResponseWriter, r *http.Request) {
	h.recordHeader(r.Header)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ts":                   1700000123,
		"cpu_count":            4,
		"cpu_possible":         8,
		"cpu_target":           4,
		"cpu_target_attempts":  1,
		"cpu_write_pending_ms": 0,
		"cpu_used_pct":         12.5,
		"mem_total":            1073741824,
		"mem_used":             268435456,
		"mem_cache":            67108864,
		"disk_used":            536870912,
		"disk_total":           10737418240,
		"oom_kills": []map[string]any{
			{"process": "python3", "seq": 7},
		},
	})
}

type harnessProcSvc struct {
	processconnect.UnimplementedProcessHandler
	h *envdTestHarness
}

func (s *harnessProcSvc) Start(ctx context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
	s.h.mu.Lock()
	s.h.lastStartReq = req.Msg
	holdCh := s.h.holdStartCh
	s.h.mu.Unlock()

	if err := stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 42}},
		},
	}); err != nil {
		return err
	}

	if req.Msg.GetPty() != nil {
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{
					Data: &procpb.ProcessEvent_DataEvent{
						Output: &procpb.ProcessEvent_DataEvent_Pty{Pty: []byte("pty-banner\r\n")},
					},
				},
			},
		})
	} else {
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{
					Data: &procpb.ProcessEvent_DataEvent{
						Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("out-chunk")},
					},
				},
			},
		})
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{
					Data: &procpb.ProcessEvent_DataEvent{
						Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte("err-chunk")},
					},
				},
			},
		})
	}

	if holdCh != nil {
		select {
		case <-holdCh:
		case <-ctx.Done():
			s.h.disconnectHit.Store(true)
			select {
			case s.h.disconnectCh <- struct{}{}:
			default:
			}
			return ctx.Err()
		}
	}

	return stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
		},
	})
}

func (s *harnessProcSvc) Connect(_ context.Context, req *connect.Request[procpb.ConnectRequest], stream *connect.ServerStream[procpb.ConnectResponse]) error {
	pid := req.Msg.GetProcess().GetPid()
	if tag := req.Msg.GetProcess().GetTag(); tag != "" {
		s.h.mu.Lock()
		resolved, ok := s.h.existingTags[tag]
		s.h.mu.Unlock()
		if !ok {
			return connect.NewError(connect.CodeNotFound, errors.New("tag not found"))
		}
		pid = resolved
	}
	if err := stream.Send(&procpb.ConnectResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: pid}},
		},
	}); err != nil {
		return err
	}
	_ = stream.Send(&procpb.ConnectResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_Data{
				Data: &procpb.ProcessEvent_DataEvent{
					Output: &procpb.ProcessEvent_DataEvent_Pty{Pty: []byte("reconnected-pty")},
				},
			},
		},
	})
	return stream.Send(&procpb.ConnectResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
		},
	})
}

func (s *harnessProcSvc) List(_ context.Context, _ *connect.Request[procpb.ListRequest]) (*connect.Response[procpb.ListResponse], error) {
	return connect.NewResponse(&procpb.ListResponse{
		Processes: []*procpb.ProcessInfo{{Pid: 42, Config: &procpb.ProcessConfig{Cmd: "sleep"}}},
	}), nil
}

func (s *harnessProcSvc) SendSignal(_ context.Context, req *connect.Request[procpb.SendSignalRequest]) (*connect.Response[procpb.SendSignalResponse], error) {
	pid := req.Msg.GetProcess().GetPid()
	s.h.mu.Lock()
	s.h.lastSignal = req.Msg.GetSignal()
	if tag := req.Msg.GetProcess().GetTag(); tag != "" {
		resolved, ok := s.h.existingTags[tag]
		if !ok {
			s.h.mu.Unlock()
			return nil, connect.NewError(connect.CodeNotFound, errors.New("tag not found"))
		}
		pid = resolved
		delete(s.h.existingTags, tag)
	}
	exists := s.h.existingPIDs[pid]
	if exists {
		s.h.killedPIDs = append(s.h.killedPIDs, pid)
		delete(s.h.existingPIDs, pid)
	}
	s.h.mu.Unlock()
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("process not found"))
	}
	return connect.NewResponse(&procpb.SendSignalResponse{}), nil
}

func (s *harnessProcSvc) SendInput(_ context.Context, req *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
	s.h.mu.Lock()
	s.h.lastSendInput = req.Msg
	s.h.mu.Unlock()
	return connect.NewResponse(&procpb.SendInputResponse{}), nil
}

func (s *harnessProcSvc) CloseStdin(_ context.Context, _ *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error) {
	return connect.NewResponse(&procpb.CloseStdinResponse{}), nil
}

func (s *harnessProcSvc) Update(_ context.Context, req *connect.Request[procpb.UpdateRequest]) (*connect.Response[procpb.UpdateResponse], error) {
	s.h.mu.Lock()
	s.h.lastUpdateReq = req.Msg
	s.h.mu.Unlock()
	return connect.NewResponse(&procpb.UpdateResponse{}), nil
}

func (s *harnessProcSvc) StreamInput(_ context.Context, stream *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error) {
	var targetPID uint32
	for stream.Receive() {
		msg := stream.Msg()
		switch ev := msg.GetEvent().(type) {
		case *procpb.StreamInputRequest_Start:
			targetPID = ev.Start.GetProcess().GetPid()
			s.h.mu.Lock()
			exists := s.h.existingPIDs[targetPID]
			s.h.mu.Unlock()
			if !exists {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("pid not found"))
			}
		case *procpb.StreamInputRequest_Data:
			s.h.mu.Lock()
			s.h.streamInputs = append(s.h.streamInputs, ev.Data.GetInput())
			s.h.mu.Unlock()
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procpb.StreamInputResponse{}), nil
}

type harnessFsSvc struct {
	filesystemconnect.UnimplementedFilesystemHandler
	h *envdTestHarness
}

func (s *harnessFsSvc) Stat(ctx context.Context, req *connect.Request[fspb.StatRequest]) (*connect.Response[fspb.StatResponse], error) {
	if req.Msg.GetPath() == "/slow" {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if req.Msg.GetPath() == "/link" {
		target := "/real/file"
		return connect.NewResponse(&fspb.StatResponse{
			Entry: &fspb.EntryInfo{
				Name:          "link",
				Path:          "/link",
				Type:          fspb.FileType_FILE_TYPE_FILE,
				IsSymlink:     true,
				SymlinkTarget: &target,
				Metadata:      map[string]string{"owner-tag": "ci"},
				ModifiedTime:  timestamppb.New(time.Unix(1700000000, 0)),
			},
		}), nil
	}
	return connect.NewResponse(&fspb.StatResponse{
		Entry: &fspb.EntryInfo{
			Name: "file",
			Path: req.Msg.GetPath(),
			Type: fspb.FileType_FILE_TYPE_SYMLINK,
		},
	}), nil
}

func (s *harnessFsSvc) ListDir(_ context.Context, _ *connect.Request[fspb.ListDirRequest]) (*connect.Response[fspb.ListDirResponse], error) {
	return connect.NewResponse(&fspb.ListDirResponse{
		Entries: []*fspb.EntryInfo{
			{Name: "sym", Path: "/dir/sym", Type: fspb.FileType_FILE_TYPE_SYMLINK},
		},
	}), nil
}

func (s *harnessFsSvc) MakeDir(_ context.Context, _ *connect.Request[fspb.MakeDirRequest]) (*connect.Response[fspb.MakeDirResponse], error) {
	return connect.NewResponse(&fspb.MakeDirResponse{}), nil
}

func (s *harnessFsSvc) Move(_ context.Context, req *connect.Request[fspb.MoveRequest]) (*connect.Response[fspb.MoveResponse], error) {
	return connect.NewResponse(&fspb.MoveResponse{
		Entry: &fspb.EntryInfo{
			Name: "renamed.txt",
			Path: req.Msg.GetDestination(),
			Type: fspb.FileType_FILE_TYPE_FILE,
			Size: 64,
		},
	}), nil
}

func (s *harnessFsSvc) Remove(_ context.Context, _ *connect.Request[fspb.RemoveRequest]) (*connect.Response[fspb.RemoveResponse], error) {
	return connect.NewResponse(&fspb.RemoveResponse{}), nil
}

func (s *harnessFsSvc) WatchDir(ctx context.Context, req *connect.Request[fspb.WatchDirRequest], stream *connect.ServerStream[fspb.WatchDirResponse]) error {
	s.h.mu.Lock()
	s.h.lastWatchReq = req.Msg
	s.h.mu.Unlock()

	if err := stream.Send(&fspb.WatchDirResponse{
		Event: &fspb.WatchDirResponse_Start{Start: &fspb.WatchDirResponse_StartEvent{}},
	}); err != nil {
		return err
	}
	var entry *fspb.EntryInfo
	if req.Msg.GetIncludeEntry() {
		entry = &fspb.EntryInfo{
			Name:     "created.txt",
			Path:     req.Msg.GetPath() + "/created.txt",
			Type:     fspb.FileType_FILE_TYPE_FILE,
			Size:     12,
			Metadata: map[string]string{"stage": "prod"},
		}
	}
	if err := stream.Send(&fspb.WatchDirResponse{
		Event: &fspb.WatchDirResponse_Filesystem{
			Filesystem: &fspb.FilesystemEvent{
				Name:  "created.txt",
				Type:  fspb.EventType_EVENT_TYPE_CREATE,
				Entry: entry,
			},
		},
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func assertEnvdIsolationHeaders(t *testing.T, hdr http.Header, wantUser string) {
	t.Helper()
	if got := hdr.Get("E2b-Sandbox-Id"); got != "sbx-dp-1" {
		t.Errorf("E2b-Sandbox-Id = %q, want sbx-dp-1", got)
	}
	if got := hdr.Get("E2b-Sandbox-Port"); got != "49983" {
		t.Errorf("E2b-Sandbox-Port = %q, want 49983", got)
	}
	if got := hdr.Get("X-Access-Token"); got != "envd-tok-123" {
		t.Errorf("X-Access-Token = %q, want envd-tok-123", got)
	}
	if got := hdr.Get("X-Sandbox-Extra"); got != "ok" {
		t.Errorf("X-Sandbox-Extra = %q, want ok", got)
	}
	if got := hdr.Get("X-Control-Only"); got != "" {
		t.Errorf("control-plane Config.Headers leaked to envd: %q", got)
	}
	if got := hdr.Get("X-Api-Key"); got != "" {
		t.Errorf("X-API-Key leaked to envd: %q", got)
	}
	if hdr.Get("User-Agent") == "" {
		t.Error("missing User-Agent on envd request")
	}
	wantAuth := ""
	if wantUser != "" {
		wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(wantUser+":"))
	}
	if got := hdr.Get("Authorization"); got != wantAuth {
		t.Errorf("Authorization = %q, want %q", got, wantAuth)
	}
}

func TestEnvdPerCallUserAndHeaderIsolationAcrossRPCAndFiles(t *testing.T) {
	sbx, h := newEnvdTestHarness(t, "0.6.13")
	ctx := context.Background()

	if _, err := sbx.Files.Stat(ctx, "/link", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "")

	if _, err := sbx.Files.Stat(ctx, "/link", FsOptions{User: "root"}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "root")

	if err := sbx.Files.MakeDir(ctx, "/d", FsOptions{User: "ops"}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "ops")

	if _, err := sbx.Files.WriteBytes(ctx, "/tmp/a.txt", []byte("payload"), FsOptions{}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "")

	if _, err := sbx.Files.WriteBytes(ctx, "/tmp/a.txt", []byte("payload"), FsOptions{User: "deploy"}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "deploy")

	if _, err := sbx.Files.ReadBytes(ctx, "/tmp/a.txt", FsOptions{User: "reader"}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "reader")

	oldSbx, oldH := newEnvdTestHarness(t, "0.3.9")
	if _, err := oldSbx.Files.Stat(ctx, "/x", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, oldH.headerSnapshot(), DefaultUser)
	if _, err := oldSbx.Commands.List(ctx); err != nil {
		t.Fatal(err)
	}
	assertEnvdIsolationHeaders(t, oldH.headerSnapshot(), DefaultUser)
}

func TestFilesystemMetadataGzipSymlinkComposeAndForceMultipart(t *testing.T) {
	ctx := context.Background()

	oldSbx, _ := newEnvdTestHarness(t, "0.6.1")
	_, err := oldSbx.Files.WriteString(ctx, "/a", "hi", FsOptions{Metadata: map[string]string{"env": "dev"}})
	var tplErr *TemplateError
	if !errors.As(err, &tplErr) {
		t.Fatalf("want *TemplateError for metadata on 0.6.1, got %T: %v", err, err)
	}
	_, err = oldSbx.Files.WriteString(ctx, "/a", "hi", FsOptions{Metadata: map[string]string{"bad key": "dev"}})
	var invErr *InvalidArgumentError
	if !errors.As(err, &invErr) {
		t.Fatalf("want *InvalidArgumentError for invalid metadata key, got %T: %v", err, err)
	}

	sbx, h := newEnvdTestHarness(t, "0.6.13")
	wi, err := sbx.Files.WriteBytes(ctx, "/app/data.txt", []byte("abc"), FsOptions{
		Metadata:       map[string]string{"author": "alice"},
		ForceMultipart: true,
	})
	if err != nil {
		t.Fatalf("WriteBytes with metadata: %v", err)
	}
	hdr := h.headerSnapshot()
	if !strings.HasPrefix(hdr.Get("Content-Type"), "multipart/form-data") {
		t.Fatalf("ForceMultipart ignored; Content-Type = %q", hdr.Get("Content-Type"))
	}
	if hdr.Get("X-Metadata-author") != "alice" {
		t.Fatalf("X-Metadata-author = %q, want alice", hdr.Get("X-Metadata-author"))
	}
	if wi.Metadata["author"] != "alice" {
		t.Fatalf("WriteInfo.Metadata = %+v, want author=alice", wi.Metadata)
	}

	text, err := sbx.Files.ReadString(ctx, "/app/data.txt", FsOptions{Gzip: true})
	if err != nil {
		t.Fatalf("ReadString(Gzip): %v", err)
	}
	if text != "downloaded-body-payload" {
		t.Fatalf("gunzipped content = %q, want downloaded-body-payload", text)
	}
	if got := h.headerSnapshot().Get("Accept-Encoding"); got != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want gzip", got)
	}

	statInfo, err := sbx.Files.Stat(ctx, "/link", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if statInfo.Type != EntryTypeSymlink || statInfo.SymlinkTarget != "/real/file" || statInfo.Metadata["owner-tag"] != "ci" {
		t.Fatalf("unexpected symlink EntryInfo: %+v", statInfo)
	}
	entries, err := sbx.Files.List(ctx, "/dir", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type != EntryTypeSymlink {
		t.Fatalf("List symlink entry = %+v", entries)
	}

	preComposeSbx, _ := newEnvdTestHarness(t, "0.5.4")
	if _, err := preComposeSbx.Files.Compose(ctx, []string{"/p1", "/p2"}, "/out", FsOptions{}); !errors.As(err, &tplErr) {
		t.Fatalf("want *TemplateError on envd 0.5.4 Compose, got %T: %v", err, err)
	}
	if _, err := sbx.Files.Compose(ctx, nil, "/out", FsOptions{}); !errors.As(err, &invErr) {
		t.Fatalf("want *InvalidArgumentError for empty sources, got %T: %v", err, err)
	}
	composed, err := sbx.Files.Compose(ctx, []string{"/part1", "/part2"}, "/joined.bin", FsOptions{User: "builder"})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "builder")
	if composed.Path != "/joined.bin" || composed.Name != "joined.bin" || composed.Type != EntryTypeFile {
		t.Fatalf("Compose EntryInfo = %+v", composed)
	}
	var fnf *FileNotFoundError
	if _, err := sbx.Files.Compose(ctx, []string{"/p1"}, "/missing-src", FsOptions{}); !errors.As(err, &fnf) {
		t.Fatalf("want *FileNotFoundError on 404 Compose, got %T: %v", err, err)
	}

	var toErr *TimeoutError
	if _, err := sbx.Files.Stat(ctx, "/slow", FsOptions{RequestTimeout: 30 * time.Millisecond}); !errors.As(err, &toErr) {
		t.Fatalf("want *TimeoutError on RequestTimeout, got %T: %v", err, err)
	}
}

func TestWatchWithOptionsGatesAndIncludeEntry(t *testing.T) {
	ctx := context.Background()
	var tplErr *TemplateError

	sbx062, _ := newEnvdTestHarness(t, "0.6.2")
	if _, err := sbx062.Files.WatchWithOptions(ctx, "/w", WatchOptions{IncludeEntry: true}); !errors.As(err, &tplErr) {
		t.Fatalf("want *TemplateError for IncludeEntry on 0.6.2, got %T: %v", err, err)
	}

	sbx063, _ := newEnvdTestHarness(t, "0.6.3")
	if _, err := sbx063.Files.WatchWithOptions(ctx, "/w", WatchOptions{AllowNetworkMounts: true}); !errors.As(err, &tplErr) {
		t.Fatalf("want *TemplateError for AllowNetworkMounts on 0.6.3, got %T: %v", err, err)
	}

	sbx, h := newEnvdTestHarness(t, "0.6.13")
	wh, err := sbx.Files.WatchWithOptions(ctx, "/watched", WatchOptions{
		Recursive:          true,
		IncludeEntry:       true,
		AllowNetworkMounts: true,
		User:               "watcher",
	})
	if err != nil {
		t.Fatalf("WatchWithOptions: %v", err)
	}

	select {
	case ev := <-wh.Events():
		if ev.Name != "created.txt" || ev.Type != FsEventCreate {
			t.Fatalf("unexpected watch event: %+v", ev)
		}
		if ev.Entry == nil || ev.Entry.Size != 12 || ev.Entry.Metadata["stage"] != "prod" {
			t.Fatalf("WatchEvent.Entry not populated: %+v", ev.Entry)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}

	hdr := h.headerSnapshot()
	assertEnvdIsolationHeaders(t, hdr, "watcher")
	if hdr.Get("Keepalive-Ping-Interval") != "50" {
		t.Fatalf("Keepalive-Ping-Interval = %q, want 50", hdr.Get("Keepalive-Ping-Interval"))
	}

	h.mu.Lock()
	gotReq := h.lastWatchReq
	h.mu.Unlock()
	if !gotReq.GetRecursive() || !gotReq.GetIncludeEntry() || !gotReq.GetAllowNetworkMounts() {
		t.Fatalf("WatchDirRequest flags not forwarded: %+v", gotReq)
	}

	if err := wh.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if wh.Err() != nil {
		t.Fatalf("clean Stop should leave Err()==nil, got %v", wh.Err())
	}
}

func TestCommandsShellRetainOutputKillDisconnectCloseStdinAndStreamInput(t *testing.T) {
	ctx := context.Background()
	sbx, h := newEnvdTestHarness(t, "0.6.13")

	var gotStdout, gotStderr string
	retainFalse := false
	handle, err := sbx.Commands.Run(ctx, "echo hi && uname", RunOptions{
		Shell:        "/bin/bash",
		User:         "runner",
		RetainOutput: &retainFalse,
		OnStdout:     func(b []byte) { gotStdout += string(b) },
		OnStderr:     func(b []byte) { gotStderr += string(b) },
	})
	if err != nil {
		t.Fatalf("Commands.Run: %v", err)
	}
	res, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Stdout != "" || res.Stderr != "" {
		t.Fatalf("RetainOutput=false should leave Result.Stdout/Stderr empty, got %+v", res)
	}
	if gotStdout != "out-chunk" || gotStderr != "err-chunk" {
		t.Fatalf("callbacks missed output: stdout=%q stderr=%q", gotStdout, gotStderr)
	}
	h.mu.Lock()
	startCfg := h.lastStartReq.GetProcess()
	h.mu.Unlock()
	if startCfg.GetCmd() != "/bin/bash" || !slices.Equal(startCfg.GetArgs(), []string{"-l", "-c", "echo hi && uname"}) {
		t.Fatalf("Shell start config = cmd=%q args=%v", startCfg.GetCmd(), startCfg.GetArgs())
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "runner")

	killed, err := sbx.Commands.Kill(ctx, 42)
	if err != nil || !killed {
		t.Fatalf("first Kill(42) = (%v, %v), want (true, nil)", killed, err)
	}
	killedAgain, err := sbx.Commands.Kill(ctx, 42)
	if err != nil || killedAgain {
		t.Fatalf("second Kill(42) on missing PID = (%v, %v), want (false, nil)", killedAgain, err)
	}
	ptyKilled, err := sbx.Pty.Kill(ctx, 9999)
	if err != nil || ptyKilled {
		t.Fatalf("Pty.Kill(9999) on missing PID = (%v, %v), want (false, nil)", ptyKilled, err)
	}

	h.mu.Lock()
	h.existingPIDs[77] = true
	h.mu.Unlock()

	inStream, err := sbx.Commands.StreamInput(ctx, 77, CommandRequestOptions{User: "streamer"})
	if err != nil {
		t.Fatalf("Commands.StreamInput: %v", err)
	}
	if err := inStream.SendData([]byte("first\n")); err != nil {
		t.Fatalf("SendData 1: %v", err)
	}
	if err := inStream.SendData([]byte("second\n")); err != nil {
		t.Fatalf("SendData 2: %v", err)
	}
	if err := inStream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := inStream.Close(); err != nil {
		t.Fatalf("idempotent second Close: %v", err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "streamer")

	ptyStream, err := sbx.Pty.StreamInput(ctx, 77)
	if err != nil {
		t.Fatalf("Pty.StreamInput: %v", err)
	}
	if err := ptyStream.SendPty([]byte("ls\r")); err != nil {
		t.Fatalf("SendPty: %v", err)
	}
	if err := ptyStream.Close(); err != nil {
		t.Fatalf("PtyInputStream.Close: %v", err)
	}

	h.mu.Lock()
	inputs := append([]*procpb.ProcessInput(nil), h.streamInputs...)
	h.mu.Unlock()
	if len(inputs) != 3 ||
		string(inputs[0].GetStdin()) != "first\n" ||
		string(inputs[1].GetStdin()) != "second\n" ||
		string(inputs[2].GetPty()) != "ls\r" {
		t.Fatalf("unexpected streamed inputs: %+v", inputs)
	}

	missingStream, err := sbx.Commands.StreamInput(ctx, 8888)
	if err == nil {
		err = missingStream.Close()
	}
	if _, ok := errors.AsType[*FileNotFoundError](err); !ok {
		t.Fatalf("want *FileNotFoundError for missing PID StreamInput, got %T: %v", err, err)
	}

	holdCh := make(chan struct{})
	h.mu.Lock()
	h.holdStartCh = holdCh
	h.killedPIDs = nil
	h.mu.Unlock()

	bgHandle, err := sbx.Commands.Start(ctx, "sleep", RunOptions{Args: []string{"60"}, Background: true})
	if err != nil {
		t.Fatalf("Start background: %v", err)
	}
	waitPIDCtx, waitPIDCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitPIDCancel()
	if _, err := bgHandle.WaitPID(waitPIDCtx); err != nil {
		t.Fatalf("WaitPID: %v", err)
	}
	if err := bgHandle.CloseStdin(ctx); err != nil {
		t.Fatalf("CommandHandle.CloseStdin: %v", err)
	}
	bgHandle.Disconnect()
	bgHandle.Disconnect()

	select {
	case <-h.disconnectCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Disconnect to cancel server stream")
	}
	close(holdCh)
	h.mu.Lock()
	killedAfterDisconnect := len(h.killedPIDs)
	h.holdStartCh = nil
	h.mu.Unlock()
	if killedAfterDisconnect != 0 {
		t.Fatalf("Disconnect must not kill remote process; killedPIDs=%v", h.killedPIDs)
	}
}

func TestPtyCreateDefaultsAndConnect(t *testing.T) {
	ctx := context.Background()
	sbx, h := newEnvdTestHarness(t, "0.6.13")

	callerEnvs := map[string]string{"LANG": "en_US.UTF-8", "CUSTOM": "1"}
	var createData bytes.Buffer
	ptyHandle, err := sbx.Pty.Create(ctx, PtyOptions{
		User:   "termuser",
		Envs:   callerEnvs,
		OnData: func(b []byte) { createData.Write(b) },
	})
	if err != nil {
		t.Fatalf("Pty.Create: %v", err)
	}
	if _, err := ptyHandle.Wait(ctx); err != nil {
		t.Fatalf("Pty Wait: %v", err)
	}
	if _, mutated := callerEnvs["TERM"]; mutated {
		t.Fatal("Pty.Create mutated caller's Envs map")
	}

	h.mu.Lock()
	pCfg := h.lastStartReq.GetProcess()
	ptySize := h.lastStartReq.GetPty().GetSize()
	h.mu.Unlock()

	if pCfg.GetCmd() != "/bin/bash" || !slices.Equal(pCfg.GetArgs(), []string{"-i", "-l"}) {
		t.Fatalf("Pty.Create default cmd/args = %q %v", pCfg.GetCmd(), pCfg.GetArgs())
	}
	if ptySize.GetCols() != 80 || ptySize.GetRows() != 24 {
		t.Fatalf("Pty.Create default size = %dx%d", ptySize.GetCols(), ptySize.GetRows())
	}
	envs := pCfg.GetEnvs()
	if envs["TERM"] != "xterm-256color" || envs["LANG"] != "en_US.UTF-8" || envs["LC_ALL"] != "C.UTF-8" || envs["CUSTOM"] != "1" {
		t.Fatalf("Pty.Create merged envs = %v", envs)
	}
	if createData.String() != "pty-banner\r\n" {
		t.Fatalf("Pty.Create OnData = %q", createData.String())
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "termuser")

	var connData bytes.Buffer
	connHandle, err := sbx.Pty.Connect(ctx, 42, PtyConnectOptions{
		User:   "termuser",
		OnData: func(b []byte) { connData.Write(b) },
	})
	if err != nil {
		t.Fatalf("Pty.Connect: %v", err)
	}
	if _, err := connHandle.Wait(ctx); err != nil {
		t.Fatalf("Pty.Connect Wait: %v", err)
	}
	if connData.String() != "reconnected-pty" {
		t.Fatalf("Pty.Connect OnData = %q, want reconnected-pty", connData.String())
	}
}

func TestStreamInputSendDataFailureFollowedByClose(t *testing.T) {
	ctx := context.Background()
	sbx, h := newEnvdTestHarness(t, "0.6.13")
	h.mu.Lock()
	h.existingPIDs[99] = true
	h.mu.Unlock()

	stream, err := sbx.Commands.StreamInput(ctx, 99)
	if err != nil {
		t.Fatalf("StreamInput: %v", err)
	}
	if err := stream.SendData([]byte("ok\n")); err != nil {
		t.Fatalf("initial SendData: %v", err)
	}

	h.Server.CloseClientConnections()
	h.Server.Close()

	var sendErr error
	for range 10 {
		sendErr = stream.SendData(bytes.Repeat([]byte("x"), 64*1024))
		if sendErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sendErr == nil {
		t.Fatal("expected SendData to fail after server closed")
	}

	select {
	case <-stream.s.keepaliveDone:
	case <-time.After(time.Second):
		t.Fatal("keepaliveLoop leaked after failed SendData")
	}

	closeErr1 := stream.Close()
	closeErr2 := stream.Close()
	if !errors.Is(closeErr1, sendErr) || !errors.Is(closeErr2, sendErr) {
		t.Fatalf("Close() errors (%v, %v) did not match cached sendErr (%v)", closeErr1, closeErr2, sendErr)
	}
	if afterErr := stream.SendData([]byte("after")); afterErr == nil {
		t.Fatal("expected error when calling SendData on closed stream")
	}
}

func TestCommandsRunForegroundTimeoutDisconnectsStream(t *testing.T) {
	sbx, h := newEnvdTestHarness(t, "0.6.13")
	holdCh := make(chan struct{})
	defer close(holdCh)

	h.mu.Lock()
	h.holdStartCh = holdCh
	h.disconnectHit.Store(false)
	h.mu.Unlock()

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	go func() {
		_, _ = pw.Write([]byte("chunk-1\n"))
	}()

	_, err := sbx.Commands.Run(context.Background(), "sleep", RunOptions{
		Args:      []string{"60"},
		TimeoutMs: 40,
		Stdin:     pr,
	})
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want *TimeoutError from foreground TimeoutMs, got %T: %v", err, err)
	}

	_ = pw.Close()
	select {
	case <-h.disconnectCh:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground Commands.Run timeout leaked server stream instead of calling Disconnect()")
	}
	if !h.disconnectHit.Load() {
		t.Fatal("foreground Commands.Run timeout leaked server stream instead of calling Disconnect()")
	}
}

func TestConcurrentApplyConnectedSandboxWithDataPlaneUnderRace(t *testing.T) {
	ctx := context.Background()
	sbx, _ := newEnvdTestHarness(t, "0.6.13")

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			for j := range 20 {
				dom := "example.com"
				tok := "envd-tok-123"
				ver := "0.6.13"
				if (i+j)%2 == 0 {
					ver = "0.6.4"
				}
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
	}
	for range 6 {
		wg.Go(func() {
			for range 20 {
				_, _ = sbx.Files.Stat(ctx, "/link", FsOptions{User: "root"})
				_, _ = sbx.Files.WriteBytes(ctx, "/tmp/race.txt", []byte("ok"), FsOptions{})
				_, _ = sbx.Files.Compose(ctx, []string{"/a", "/b"}, "/out", FsOptions{})
				_, _ = sbx.Commands.List(ctx)
				_, _ = sbx.GetEnvs(ctx)
				_, _ = sbx.GetEnvdMetrics(ctx)
				_, _ = sbx.UploadURL("/tmp/race.txt", SignatureOptions{ExpirationInSeconds: 30})
			}
		})
	}
	wg.Wait()
}

func TestCommandHandleSymmetryTagOperationsAndEnvdHTTP(t *testing.T) {
	ctx := context.Background()
	sbx, h := newEnvdTestHarness(t, "0.6.13")

	oldSbx, _ := newEnvdTestHarness(t, "0.1.4")
	if _, err := oldSbx.GetEnvdMetrics(ctx); err == nil {
		t.Fatal("expected *TemplateError on envd 0.1.4 GetEnvdMetrics")
	} else if _, ok := errors.AsType[*TemplateError](err); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}

	envs, err := sbx.GetEnvs(ctx, CommandRequestOptions{User: "inspector"})
	if err != nil {
		t.Fatalf("GetEnvs: %v", err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "inspector")
	if envs["E2B_SANDBOX"] != "true" || envs["APP_ENV"] != "test" {
		t.Fatalf("unexpected envs: %+v", envs)
	}

	metrics, err := sbx.GetEnvdMetrics(ctx)
	if err != nil {
		t.Fatalf("GetEnvdMetrics: %v", err)
	}
	if metrics.CPUCount != 4 || metrics.CPUPossible != 8 || metrics.MemCache != 67108864 || len(metrics.OOMKills) != 1 || metrics.OOMKills[0].Process != "python3" {
		t.Fatalf("unexpected EnvdMetrics: %+v", metrics)
	}

	info, err := sbx.Files.GetInfo(ctx, "/link", FsOptions{})
	if err != nil || info.Type != EntryTypeSymlink {
		t.Fatalf("GetInfo = %+v, err = %v", info, err)
	}
	renamed, err := sbx.Files.Rename(ctx, "/old.txt", "/dir/renamed.txt", FsOptions{User: "mover"})
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	assertEnvdIsolationHeaders(t, h.headerSnapshot(), "mover")
	if renamed.Path != "/dir/renamed.txt" || renamed.Size != 64 || renamed.Type != EntryTypeFile {
		t.Fatalf("unexpected Rename EntryInfo: %+v", renamed)
	}

	holdCh := make(chan struct{})
	h.mu.Lock()
	h.holdStartCh = holdCh
	h.existingPIDs[42] = true
	h.mu.Unlock()

	handle, err := sbx.Commands.Start(ctx, "cat", RunOptions{
		Background:  true,
		EnableStdin: true,
		User:        "worker",
	})
	if err != nil {
		t.Fatalf("Start with EnableStdin: %v", err)
	}
	pid, err := handle.WaitPID(ctx)
	if err != nil || pid != 42 {
		t.Fatalf("WaitPID = (%d, %v), want (42, nil)", pid, err)
	}
	h.mu.Lock()
	stdinFlag := h.lastStartReq.GetStdin()
	h.mu.Unlock()
	if !stdinFlag {
		t.Fatal("RunOptions.EnableStdin=true did not set StartRequest.Stdin=true")
	}
	if _, exited := handle.ExitCode(); exited {
		t.Fatal("ExitCode should report exited=false while running")
	}

	if err := handle.SendStdin(ctx, []byte("ping\n")); err != nil {
		t.Fatalf("handle.SendStdin: %v", err)
	}
	cmdIn, err := handle.StreamInput(ctx)
	if err != nil {
		t.Fatalf("handle.StreamInput: %v", err)
	}
	if err := cmdIn.Send([]byte("ordered\n")); err != nil {
		t.Fatalf("cmdIn.Send: %v", err)
	}
	if err := cmdIn.Close(); err != nil {
		t.Fatalf("cmdIn.Close: %v", err)
	}

	if err := handle.SendPty(ctx, []byte("ls\r")); err != nil {
		t.Fatalf("handle.SendPty: %v", err)
	}
	ptyIn, err := handle.StreamPtyInput(ctx)
	if err != nil {
		t.Fatalf("handle.StreamPtyInput: %v", err)
	}
	if err := ptyIn.SendData([]byte("pwd\r")); err != nil {
		t.Fatalf("ptyIn.SendData: %v", err)
	}
	if err := ptyIn.Close(); err != nil {
		t.Fatalf("ptyIn.Close: %v", err)
	}
	if err := handle.ResizePty(ctx, 132, 43); err != nil {
		t.Fatalf("handle.ResizePty: %v", err)
	}
	h.mu.Lock()
	upd := h.lastUpdateReq
	h.mu.Unlock()
	if upd.GetProcess().GetPid() != 42 || upd.GetPty().GetSize().GetCols() != 132 || upd.GetPty().GetSize().GetRows() != 43 {
		t.Fatalf("unexpected UpdateRequest from ResizePty: %+v", upd)
	}

	signaled, err := handle.SendSignal(ctx, SignalSIGTERM)
	if err != nil || !signaled {
		t.Fatalf("handle.SendSignal(SIGTERM) = (%v, %v), want (true, nil)", signaled, err)
	}
	h.mu.Lock()
	gotSig := h.lastSignal
	h.mu.Unlock()
	if gotSig != procpb.Signal_SIGNAL_SIGTERM {
		t.Fatalf("SendSignal sent %v, want SIGNAL_SIGTERM", gotSig)
	}

	close(holdCh)
	h.mu.Lock()
	h.holdStartCh = nil
	h.mu.Unlock()
	if _, err := handle.Wait(ctx); err != nil {
		t.Fatalf("handle.Wait: %v", err)
	}
	if code, exited := handle.ExitCode(); !exited || code != 0 {
		t.Fatalf("ExitCode after exit = (%d, %v), want (0, true)", code, exited)
	}

	h.mu.Lock()
	h.existingPIDs[42] = true
	h.existingTags["boot-svc"] = 42
	h.existingTags["pty-svc"] = 42
	h.mu.Unlock()

	tagHandle, err := sbx.Commands.ConnectByTag(ctx, "boot-svc")
	if err != nil {
		t.Fatalf("ConnectByTag: %v", err)
	}
	if tagPID, err := tagHandle.WaitPID(ctx); err != nil || tagPID != 42 {
		t.Fatalf("ConnectByTag WaitPID = (%d, %v), want (42, nil)", tagPID, err)
	}
	if _, err := tagHandle.Wait(ctx); err != nil {
		t.Fatalf("ConnectByTag Wait: %v", err)
	}

	ptyTagHandle, err := sbx.Pty.ConnectByTag(ctx, "pty-svc", PtyConnectOptions{})
	if err != nil {
		t.Fatalf("Pty.ConnectByTag: %v", err)
	}
	if _, err := ptyTagHandle.Wait(ctx); err != nil {
		t.Fatalf("Pty.ConnectByTag Wait: %v", err)
	}

	killedTag, err := sbx.Commands.KillByTag(ctx, "boot-svc")
	if err != nil || !killedTag {
		t.Fatalf("KillByTag = (%v, %v), want (true, nil)", killedTag, err)
	}
	killedMissingTag, err := sbx.Commands.KillByTag(ctx, "boot-svc")
	if err != nil || killedMissingTag {
		t.Fatalf("KillByTag missing = (%v, %v), want (false, nil)", killedMissingTag, err)
	}
}
