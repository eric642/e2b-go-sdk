package e2b

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

func fakeListedSandbox(id string) map[string]any {
	return map[string]any{
		"sandboxID":   id,
		"clientID":    "client-1",
		"templateID":  "base",
		"envdVersion": "v1.2.3",
		"cpuCount":    2,
		"memoryMB":    512,
		"diskSizeMB":  1024,
		"startedAt":   "2026-01-01T00:00:00Z",
		"endAt":       "2026-01-01T01:00:00Z",
		"state":       "running",
	}
}

func TestNewClientResolvesConfig(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Config().APIURL == "" {
		t.Fatal("resolved APIURL should be non-empty")
	}
	if ua := c.Config().Headers["User-Agent"]; ua == "" {
		t.Fatal("resolved config should set a default User-Agent header")
	}
}

func TestConfigReturnsDeepCopyOfMaps(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mock.Config.Headers = map[string]string{"X-Custom": "v1"}
	mock.Config.ExtraSandboxHeaders = map[string]string{"X-Sbx": "s1"}

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	got := c.Config()
	got.Headers["X-Custom"] = "tampered"
	got.Headers["X-Injected"] = "nope"
	delete(got.ExtraSandboxHeaders, "X-Sbx")

	if c.cfg.Headers["X-Custom"] != "v1" {
		t.Errorf("live Headers mutated: %q", c.cfg.Headers["X-Custom"])
	}
	if _, ok := c.cfg.Headers["X-Injected"]; ok {
		t.Error("live Headers gained an injected key")
	}
	if c.cfg.ExtraSandboxHeaders["X-Sbx"] != "s1" {
		t.Error("live ExtraSandboxHeaders was modified")
	}
}

func TestClientReusesAPIClientAcrossCreates(t *testing.T) {
	var n int
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		n++
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-"+strconv.Itoa(n), "example.com", "", ""))
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sbx1, err := c.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	sbx2, err := c.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatalf("Create #2: %v", err)
	}

	if sbx1.apiCli != sbx2.apiCli {
		t.Error("two sandboxes from one Client should share the same *apiclient.Client")
	}
	if sbx1.httpCli != sbx2.httpCli {
		t.Error("two sandboxes from one Client should share the same *http.Client")
	}
	if sbx1.apiCli != c.apiCli || sbx1.httpCli != c.dataCli {
		t.Error("sandbox clients should reuse Client's shared apiCli and dataCli")
	}
	if c.HTTPClient() != c.httpCli {
		t.Error("Client.HTTPClient() should return control-plane HTTP client")
	}
	if c.HTTPClient().Timeout != DefaultRequestTimeout {
		t.Errorf("control-plane HTTP client timeout = %v, want %v", c.HTTPClient().Timeout, DefaultRequestTimeout)
	}
	if sbx1.httpCli.Timeout != 0 {
		t.Errorf("data-plane HTTP client timeout = %v, want 0 (unbounded streams)", sbx1.httpCli.Timeout)
	}
}

func TestClientCreateIgnoresOptsConfig(t *testing.T) {
	hit := false
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-1", "example.com", "", ""))
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Create(context.Background(), CreateOptions{
		Config: Config{APIURL: "http://wrong.invalid"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !hit {
		t.Fatal("request should have hit the Client's server, not opts.Config.APIURL")
	}
}

func TestListPaginatesViaNextTokenHeader(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		if r.URL.Path != "/v2/sandboxes" {
			t.Errorf("path: %s", r.URL.Path)
		}
		switch r.URL.Query().Get("nextToken") {
		case "":
			w.Header().Set("x-next-token", "tok-2")
			writeJSON(t, w, http.StatusOK, []map[string]any{fakeListedSandbox("sbx-1")})
		case "tok-2":
			writeJSON(t, w, http.StatusOK, []map[string]any{fakeListedSandbox("sbx-2")})
		default:
			t.Errorf("unexpected nextToken: %q", r.URL.Query().Get("nextToken"))
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	p := c.List(context.Background(), SandboxListOptions{})

	if !p.HasNext() {
		t.Fatal("fresh paginator should report HasNext")
	}
	page1, err := p.NextItems(context.Background())
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 1 || page1[0].SandboxID != "sbx-1" {
		t.Fatalf("page1: %+v", page1)
	}
	if !p.HasNext() {
		t.Fatal("should have a second page (x-next-token was set)")
	}
	page2, err := p.NextItems(context.Background())
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 || page2[0].SandboxID != "sbx-2" {
		t.Fatalf("page2: %+v", page2)
	}
	if p.HasNext() {
		t.Fatal("should be exhausted after the last page")
	}
	tail, err := p.NextItems(context.Background())
	if err != nil || tail != nil {
		t.Fatalf("exhausted NextItems should return (nil,nil), got (%+v,%v)", tail, err)
	}
}

func TestListAllDrainsPages(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("nextToken") {
		case "":
			w.Header().Set("x-next-token", "tok-2")
			writeJSON(t, w, http.StatusOK, []map[string]any{fakeListedSandbox("sbx-1")})
		case "tok-2":
			writeJSON(t, w, http.StatusOK, []map[string]any{fakeListedSandbox("sbx-2")})
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	all, err := c.ListAll(context.Background(), SandboxListOptions{})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 || all[0].SandboxID != "sbx-1" || all[1].SandboxID != "sbx-2" {
		t.Fatalf("ListAll: %+v", all)
	}
}

func TestListSendsMetadataAndStateParams(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("limit"); got != "50" {
			t.Errorf("limit=%q want 50", got)
		}
		if states := q["state"]; len(states) == 0 || !strings.Contains(strings.Join(states, ","), "running") {
			t.Errorf("state=%v want running", states)
		}
		md := q.Get("metadata")
		if !strings.Contains(md, "user=abc") || !strings.Contains(md, "app=prod") {
			t.Errorf("metadata=%q want user=abc & app=prod", md)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.ListAll(context.Background(), SandboxListOptions{
		Metadata: map[string]string{"user": "abc", "app": "prod"},
		State:    []SandboxState{SandboxStateRunning},
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
}

func TestListMapsHTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		check  func(error) bool
	}{
		{http.StatusUnauthorized, func(e error) bool { _, ok := errors.AsType[*AuthenticationError](e); return ok }},
		{http.StatusTooManyRequests, func(e error) bool { _, ok := errors.AsType[*RateLimitError](e); return ok }},
		{http.StatusInternalServerError, func(e error) bool { _, ok := errors.AsType[*SandboxError](e); return ok }},
	}
	for _, tc := range cases {
		status := tc.status
		mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		c, err := NewClient(mock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = c.ListAll(context.Background(), SandboxListOptions{})
		if err == nil || !tc.check(err) {
			t.Errorf("status %d: unexpected error %T: %v", status, err, err)
		}
	}
}

func TestListEmptyBody(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	p := c.List(context.Background(), SandboxListOptions{})
	page, err := p.NextItems(context.Background())
	if err != nil {
		t.Fatalf("NextItems: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("expected empty page, got %+v", page)
	}
	if p.HasNext() {
		t.Fatal("no x-next-token -> should be exhausted")
	}
}

func TestSandboxInfoFromListed(t *testing.T) {
	alias := "my-alias"
	md := apiclient.SandboxMetadata{"team": "sdk"}
	mounts := []apiclient.SandboxVolumeMount{{Name: "data", Path: "/mnt/data"}}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)

	d := &apiclient.ListedSandbox{
		SandboxID:    "sbx-9",
		TemplateID:   "base",
		State:        apiclient.SandboxState("running"),
		CpuCount:     2,
		MemoryMB:     512,
		DiskSizeMB:   1024,
		StartedAt:    started,
		EndAt:        ended,
		EnvdVersion:  "v1",
		Alias:        &alias,
		Metadata:     &md,
		VolumeMounts: &mounts,
	}
	info := sandboxInfoFromListed(d)
	if info.SandboxID != "sbx-9" || info.TemplateID != "base" || info.State != SandboxStateRunning {
		t.Fatalf("core fields: %+v", info)
	}
	if info.CPUCount != 2 || info.MemoryMB != 512 || info.DiskSizeMB != 1024 {
		t.Fatalf("sizes: %+v", info)
	}
	if !info.StartedAt.Equal(started) || !info.EndAt.Equal(ended) {
		t.Fatalf("times: %+v", info)
	}
	if info.Alias != "my-alias" || info.Metadata["team"] != "sdk" {
		t.Fatalf("alias/metadata: %+v", info)
	}
	if len(info.VolumeMounts) != 1 || info.VolumeMounts[0].Name != "data" {
		t.Fatalf("mounts: %+v", info.VolumeMounts)
	}
	if info.Domain != "" || info.EnvdAccessToken != "" || info.Network != nil || info.Lifecycle != nil {
		t.Fatalf("absent fields should be zero: %+v", info)
	}
}

func TestListClonesOptionsSnapshot(t *testing.T) {
	origTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var gotMetadata, gotStartedAfter string
	var gotStates []string
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotMetadata = q.Get("metadata")
		gotStates = q["state"]
		gotStartedAfter = q.Get("startedAfter")
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	md := map[string]string{"env": "prod"}
	states := []SandboxState{SandboxStateRunning}
	ts := origTime
	p := c.List(context.Background(), SandboxListOptions{
		Metadata:     md,
		State:        states,
		StartedAfter: &ts,
	})

	// Mutate caller inputs after creating paginator.
	md["env"] = "mutated"
	md["injected"] = "yes"
	states[0] = SandboxStatePaused
	ts = origTime.Add(24 * time.Hour)

	if _, err := p.NextItems(context.Background()); err != nil {
		t.Fatalf("NextItems: %v", err)
	}
	if gotMetadata != "env=prod" {
		t.Fatalf("metadata = %q, want env=prod", gotMetadata)
	}
	if strings.Join(gotStates, ",") != "running" {
		t.Fatalf("state = %v, want [running]", gotStates)
	}
	if gotStartedAfter != origTime.Format(time.RFC3339) {
		t.Fatalf("startedAfter = %q, want %q", gotStartedAfter, origTime.Format(time.RFC3339))
	}
}

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

func TestGetMcpURL(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-1", Domain: "example.com", cfg: Config{Domain: "example.com"}}
	got := sbx.GetMcpURL()
	want := "https://50005-sbx-1.example.com/mcp"
	if got != want {
		t.Fatalf("GetMcpURL=%q want %q", got, want)
	}
}

func TestGetMcpTokenReadsAsRootAndTrims(t *testing.T) {
	sbx := newEnvdHTTPSandbox(t, "0.5.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/files" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
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
