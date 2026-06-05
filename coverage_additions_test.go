package e2b

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- Snapshots --------------------------------------------------------------

func TestListSnapshotsPaginatesViaNextToken(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		if r.URL.Path != "/snapshots" {
			t.Errorf("path: %s", r.URL.Path)
		}
		switch r.URL.Query().Get("nextToken") {
		case "":
			w.Header().Set("x-next-token", "tok-2")
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "snap-1:default", "names": []string{"team/snap-1:default"}},
			})
		case "tok-2":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "snap-2:default", "names": []string{}},
			})
		default:
			t.Errorf("unexpected nextToken: %q", r.URL.Query().Get("nextToken"))
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	all, err := c.ListAllSnapshots(context.Background(), SnapshotListOptions{})
	if err != nil {
		t.Fatalf("ListAllSnapshots: %v", err)
	}
	if len(all) != 2 || all[0].SnapshotID != "snap-1:default" || all[1].SnapshotID != "snap-2:default" {
		t.Fatalf("snapshots: %+v", all)
	}
	if len(all[0].Names) != 1 || all[0].Names[0] != "team/snap-1:default" {
		t.Fatalf("names: %+v", all[0].Names)
	}
}

func TestListSnapshotsSendsSandboxIDFilter(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sandboxID"); got != "sbx-42" {
			t.Errorf("sandboxID=%q want sbx-42", got)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Errorf("limit=%q want 10", got)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.ListAllSnapshots(context.Background(), SnapshotListOptions{SandboxID: "sbx-42", Limit: 10}); err != nil {
		t.Fatalf("ListAllSnapshots: %v", err)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool
		errNil bool
	}{
		{"ok", http.StatusOK, true, true},
		{"gone", http.StatusNotFound, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("method=%s want DELETE", r.Method)
				}
				if !strings.HasPrefix(r.URL.Path, "/templates/") {
					t.Errorf("path=%s want /templates/{id}", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			c, err := NewClient(mock.Config)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ok, err := c.DeleteSnapshot(context.Background(), "snap-1:default")
			if (err == nil) != tc.errNil {
				t.Fatalf("err=%v", err)
			}
			if ok != tc.want {
				t.Fatalf("ok=%v want %v", ok, tc.want)
			}
		})
	}
}

// --- GetMetrics time range --------------------------------------------------

func TestGetMetricsSendsStartEndParams(t *testing.T) {
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("start") != "1000" {
			t.Errorf("start=%q want 1000", q.Get("start"))
		}
		if q.Get("end") != "2000" {
			t.Errorf("end=%q want 2000", q.Get("end"))
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	if _, err := sbx.GetMetrics(context.Background(), MetricsOptions{Start: start, End: end}); err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
}

func TestGetMetricsOmitsParamsWhenNoOpts(t *testing.T) {
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("start") != "" || q.Get("end") != "" {
			t.Errorf("unexpected start/end: %v", q)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	if _, err := sbx.GetMetrics(context.Background()); err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
}

// --- MCP --------------------------------------------------------------------

func TestGetMcpURL(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-1", Domain: "example.com", cfg: Config{Domain: "example.com"}}
	got := sbx.GetMcpURL()
	want := "https://50005-sbx-1.example.com/mcp"
	if got != want {
		t.Fatalf("GetMcpURL=%q want %q", got, want)
	}
}

// --- envd version gating: file URL username ---------------------------------

func TestBuildFileURLOmitsUsernameForNewEnvd(t *testing.T) {
	// envd >= 0.4.0 with no explicit user -> username omitted, signature uses
	// empty user.
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: "0.5.7",
		EnvdAccessToken: "tok", cfg: Config{Domain: "example.com"},
	}
	raw, err := sbx.buildFileURL("/p", SignatureWrite, SignatureOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Has("username") {
		t.Fatalf("username should be omitted for envd>=0.4.0: %s", raw)
	}
	if !strings.HasPrefix(u.Query().Get("signature"), "v1_") {
		t.Fatalf("signature still expected: %s", raw)
	}
}

func TestBuildFileURLInjectsUsernameForOldEnvd(t *testing.T) {
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: "0.3.0",
		EnvdAccessToken: "tok", cfg: Config{Domain: "example.com"},
	}
	raw, err := sbx.buildFileURL("/p", SignatureWrite, SignatureOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Get("username") != defaultUser {
		t.Fatalf("username should default to %q for envd<0.4.0: %s", defaultUser, raw)
	}
}

func TestBuildFileURLKeepsExplicitUsername(t *testing.T) {
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: "0.5.7",
		EnvdAccessToken: "tok", cfg: Config{Domain: "example.com"},
	}
	raw, err := sbx.buildFileURL("/p", SignatureWrite, SignatureOptions{User: "alice"}, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Get("username") != "alice" {
		t.Fatalf("explicit username must survive: %s", raw)
	}
}

// --- envd version gating: upload strategy -----------------------------------

// newEnvdHTTPSandbox builds a *Sandbox whose envd /files endpoint is the given
// httptest server (via cfg.SandboxURL), with a real httpCli.
func newEnvdHTTPSandbox(t *testing.T, envdVersion string, h http.Handler) *Sandbox {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{Domain: "example.com", SandboxURL: srv.URL}.resolve()
	return &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: envdVersion,
		cfg: cfg, httpCli: cfg.httpClient(),
	}
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
	// Metadata must come from the envd response, not a synthesized stub.
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
		fr, _ := fh[0].Open()
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
		// Echo back the entry as envd would, deriving a name from the path.
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
	// Each result must carry real metadata from its own response, not zero values.
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
	if _, ok := err.(*FileNotFoundError); !ok {
		t.Fatalf("want *FileNotFoundError (file semantics), got %T: %v", err, err)
	}
}

func TestGetMcpTokenReadsAsRootAndTrims(t *testing.T) {
	sbx := newEnvdHTTPSandbox(t, "0.5.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/files" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		// Token file is root-owned: the read must request user=root explicitly.
		if got := r.URL.Query().Get("username"); got != "root" {
			t.Errorf("username=%q want root", got)
		}
		_, _ = w.Write([]byte("  secret-token\n"))
	}))
	sbx.Files = &Filesystem{sbx: sbx}
	tok, err := sbx.GetMcpToken(context.Background())
	if err != nil {
		t.Fatalf("GetMcpToken: %v", err)
	}
	if tok != "secret-token" {
		t.Fatalf("token=%q want trimmed secret-token", tok)
	}
}

// --- envd version gating: recursive watch & close stdin prechecks -----------

func TestWatchRecursiveRejectedOnOldEnvd(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.1.3", cfg: Config{}}
	f := &Filesystem{sbx: sbx}
	_, err := f.Watch(context.Background(), "/dir", true)
	if err == nil {
		t.Fatal("recursive watch should error on envd<0.1.4")
	}
	if _, ok := err.(*TemplateError); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}
}

func TestCloseStdinRejectedOnOldEnvd(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.5.1", cfg: Config{}}
	c := &Commands{sbx: sbx}
	err := c.CloseStdin(context.Background(), 123)
	if err == nil {
		t.Fatal("CloseStdin should error on envd<0.5.2")
	}
	if _, ok := err.(*TemplateError); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}
}

// --- filesystem List depth --------------------------------------------------

func TestListRejectsNegativeDepth(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.5.7", cfg: Config{}}
	f := &Filesystem{sbx: sbx}
	_, err := f.List(context.Background(), "/dir", FsOptions{Depth: -1})
	if err == nil {
		t.Fatal("negative depth should error")
	}
	if _, ok := err.(*InvalidArgumentError); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T: %v", err, err)
	}
}
