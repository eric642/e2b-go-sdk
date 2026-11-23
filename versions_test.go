package e2b

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
	"github.com/eric642/e2b-go-sdk/internal/envd/filesystem/filesystemconnect"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

func TestCompareEnvdVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.4.0", "0.4.0", 0},
		{"0.4.0", "0.5.7", -1},
		{"0.5.7", "0.4.0", 1},
		{"0.4", "0.4.0", 0},
		{"0.5", "0.4.0", 1},
		{"0.4.0", "0.4.1", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.5.7-rc1", "0.5.7", 0},
		{"0.5.7+meta", "0.5.7", 0},
		{"99.99.99", "0.5.7", 1},
		{"", "0.1.4", -1},
		{"", "", 0},
		{"v1.2.3", "1.2.3", 0},
		{"v1.0.0", "0.5.7", 1},
		{"V2", "1.9.9", 1},
		{"v1.2.3", "0.5.7", 1},
		{"envd-v0.6.13", "0.6.4", -1},
		{"v0.6.13", "0.6.4", 1},
	}
	for _, c := range cases {
		if got := compareEnvdVersions(c.a, c.b); got != c.want {
			t.Errorf("compareEnvdVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestEnvdGateThresholds(t *testing.T) {
	gates := []struct {
		name  string
		gate  string
		below string
		at    string
	}{
		{"recursiveWatch", envdRecursiveWatch, "0.1.3", "0.1.4"},
		{"metrics", envdMetrics, "0.1.4", "0.1.5"},
		{"defaultUser", envdDefaultUser, "0.3.9", "0.4.0"},
		{"closeStdin", envdEnvdClose, "0.5.1", "0.5.2"},
		{"fileCompose", envdFileCompose, "0.5.4", "0.5.5"},
		{"octetStreamUpload", envdOctetStreamUpload, "0.5.6", "0.5.7"},
		{"fileMetadata", envdFileMetadata, "0.6.1", "0.6.2"},
		{"watchIncludeEntry", envdWatchIncludeEntry, "0.6.2", "0.6.3"},
		{"watchNetworkMounts", envdWatchNetworkMounts, "0.6.3", "0.6.4"},
	}
	for _, tc := range gates {
		t.Run(tc.name, func(t *testing.T) {
			if compareEnvdVersions(tc.below, tc.gate) >= 0 {
				t.Errorf("%s should be < %s (%s)", tc.below, tc.name, tc.gate)
			}
			if compareEnvdVersions(tc.at, tc.gate) < 0 {
				t.Errorf("%s should be >= %s (%s)", tc.at, tc.name, tc.gate)
			}
			if compareEnvdVersions("0.6.13", tc.gate) < 0 {
				t.Errorf("stable envd-v0.6.13 should satisfy %s (%s)", tc.name, tc.gate)
			}
		})
	}
}

func TestResolveEnvdUser(t *testing.T) {
	if got := resolveEnvdUser("0.3.9", ""); got != DefaultUser {
		t.Fatalf("envd < 0.4.0 empty user = %q, want %q", got, DefaultUser)
	}
	if got := resolveEnvdUser("0.4.0", ""); got != "" {
		t.Fatalf("envd >= 0.4.0 empty user = %q, want empty", got)
	}
	if got := resolveEnvdUser("0.6.13", "root"); got != "root" {
		t.Fatalf("explicit user = %q, want root", got)
	}
	if got := resolveEnvdUser("0.2.0", "alice"); got != "alice" {
		t.Fatalf("explicit user on old envd = %q, want alice", got)
	}
}

func TestEnvdVersionForGating(t *testing.T) {
	var nilSbx *Sandbox
	if got := nilSbx.envdVersionForGating(); got != "" {
		t.Errorf("nil sandbox gating version = %q, want empty", got)
	}
	sbx := &Sandbox{EnvdVersion: "0.2.0"}
	if got := sbx.envdVersionForGating(); got != "0.2.0" {
		t.Errorf("non-debug gating version = %q want 0.2.0", got)
	}
	dbg := &Sandbox{EnvdVersion: "0.2.0", cfg: Config{Debug: true}}
	if got := dbg.envdVersionForGating(); got != envdDebugFallback {
		t.Errorf("debug gating version = %q want %q", got, envdDebugFallback)
	}
}

func TestCheckAndRequireEnvdVersion(t *testing.T) {
	if err := checkEnvdVersion("0.5.5", envdFileCompose, "composing files"); err != nil {
		t.Fatalf("at minVer should succeed, got %v", err)
	}
	if err := checkEnvdVersion("0.6.13", envdFileCompose, "composing files"); err != nil {
		t.Fatalf("above minVer should succeed, got %v", err)
	}

	err := checkEnvdVersion("0.5.4", envdFileCompose, "composing files")
	te, ok := errors.AsType[*TemplateError](err)
	if !ok {
		t.Fatalf("below minVer err=%T, want *TemplateError", err)
	}
	wantMsg := "composing files requires envd >= 0.5.5, but this sandbox runs 0.5.4; rebuild the template to use it"
	if te.Message != wantMsg {
		t.Fatalf("Message=%q, want %q", te.Message, wantMsg)
	}

	sbxOld := &Sandbox{EnvdVersion: "0.5.1"}
	if err := sbxOld.requireEnvdVersion(envdEnvdClose, "closing stdin"); err == nil {
		t.Fatal("old sandbox should fail requireEnvdVersion")
	} else if te, ok := errors.AsType[*TemplateError](err); !ok || te.Message != "closing stdin requires envd >= 0.5.2, but this sandbox runs 0.5.1; rebuild the template to use it" {
		t.Fatalf("unexpected error: %#v", err)
	}

	sbxDebug := &Sandbox{EnvdVersion: "0.1.0", cfg: Config{Debug: true}}
	if err := sbxDebug.requireEnvdVersion(envdWatchNetworkMounts, "watching network mounts"); err != nil {
		t.Fatalf("debug sandbox should bypass version gate, got %v", err)
	}

	var nilSbx *Sandbox
	if _, ok := errors.AsType[*TemplateError](nilSbx.requireEnvdVersion(envdEnvdClose, "closing stdin")); !ok {
		t.Fatal("nil sandbox should fail version gate with *TemplateError")
	}
}

type authEchoFsHandler struct {
	filesystemconnect.UnimplementedFilesystemHandler
}

func (h *authEchoFsHandler) Stat(_ context.Context, _ *connect.Request[fspb.StatRequest]) (*connect.Response[fspb.StatResponse], error) {
	return connect.NewResponse(&fspb.StatResponse{Entry: &fspb.EntryInfo{Name: "x", Path: "/x"}}), nil
}

func TestWithEnvdCallOptsInjectsUserAndTimeout(t *testing.T) {
	fsSvc := &authEchoFsHandler{}
	_, handler := filesystemconnect.NewFilesystemHandler(fsSvc)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	envdCli, err := transport.NewEnvdClients(srv.URL, srv.Client(), transport.EnvdAuth{
		Token:     "tok",
		User:      "",
		SandboxID: "sbx-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sbx := &Sandbox{
		ID:          "sbx-1",
		EnvdVersion: "0.6.13",
		envd:        envdCli,
	}

	ctx1, cancel1 := withEnvdCallOpts(context.Background(), sbx, "", 0)
	defer cancel1()
	if _, hasDeadline := ctx1.Deadline(); hasDeadline {
		t.Fatal("expected no deadline when reqTimeout == 0")
	}
	if _, err := sbx.envd.Filesystem.Stat(ctx1, connect.NewRequest(&fspb.StatRequest{Path: "/x"})); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty on envd >= 0.4.0", gotAuth)
	}

	ctx2, cancel2 := withEnvdCallOpts(context.Background(), sbx, "root", 250*time.Millisecond)
	defer cancel2()
	if _, hasDeadline := ctx2.Deadline(); !hasDeadline {
		t.Fatal("expected deadline when reqTimeout > 0")
	}
	if _, err := sbx.envd.Filesystem.Stat(ctx2, connect.NewRequest(&fspb.StatRequest{Path: "/x"})); err != nil {
		t.Fatal(err)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("root:"))
	if gotAuth != wantBasic {
		t.Fatalf("Authorization = %q, want %q", gotAuth, wantBasic)
	}
}
