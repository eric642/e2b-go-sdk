package e2b

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// fakeListedSandbox builds a ListedSandbox JSON body element. Times are
// RFC3339 so the generated time.Time fields parse.
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

	// Mutating the returned Config must not leak into the Client's live maps,
	// which the request editor reads on every call.
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
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-"+itoa(n), "example.com", "", ""))
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
	if sbx1.apiCli != c.apiCli || sbx1.httpCli != c.httpCli {
		t.Error("sandbox clients should be the Client's shared clients")
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
	// A bogus APIURL in opts.Config must be ignored: the request still hits
	// the mock server backing the Client.
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
			// no x-next-token header -> last page
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
		// State is form/explode=false -> comma-joined single value.
		if states := q["state"]; len(states) == 0 || !strings.Contains(strings.Join(states, ","), "running") {
			t.Errorf("state=%v want running", states)
		}
		// metadata arrives URL-decoded once by net/url; the inner per-pair
		// encoding survives, so we still see "k=v" joined by "&".
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
		{http.StatusUnauthorized, func(e error) bool { var t *AuthenticationError; return errors.As(e, &t) }},
		{http.StatusTooManyRequests, func(e error) bool { var t *RateLimitError; return errors.As(e, &t) }},
		{http.StatusInternalServerError, func(e error) bool { var t *SandboxError; return errors.As(e, &t) }},
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
	// Fields absent from ListedSandbox must stay zero.
	if info.Domain != "" || info.EnvdAccessToken != "" || info.Network != nil || info.Lifecycle != nil {
		t.Fatalf("absent fields should be zero: %+v", info)
	}
}
