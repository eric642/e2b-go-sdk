package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	processpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestAuthSetsAPIKey(t *testing.T) {
	auth := Auth{APIKey: "abc"}
	req := newReq(t)
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-API-Key"); got != "abc" {
		t.Fatalf("X-API-Key=%q", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization should be empty without AccessToken, got %q", req.Header.Get("Authorization"))
	}
}

func TestAuthSetsAccessToken(t *testing.T) {
	auth := Auth{AccessToken: "secret"}
	req := newReq(t)
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization=%q", got)
	}
	if got := req.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("X-API-Key should be empty, got %q", got)
	}
}

func TestAuthAPIKeyWinsOverAccessToken(t *testing.T) {
	auth := Auth{APIKey: "k", AccessToken: "tok"}
	req := newReq(t)
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-API-Key"); got != "k" {
		t.Fatalf("X-API-Key=%q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("both credentials sent: Authorization=%q", got)
	}
}

func TestAuthExtraHeadersDoNotOverwrite(t *testing.T) {
	auth := Auth{
		APIKey: "k",
		Headers: map[string]string{
			"User-Agent":    "e2b-go/x",
			"X-Custom":      "yes",
			"Authorization": "should not overwrite",
			"X-API-Key":     "should not overwrite",
		},
	}
	req := newReq(t)
	req.Header.Set("Authorization", "Pre-set by caller")
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Pre-set by caller" {
		t.Fatalf("pre-existing Authorization overwritten: %q", got)
	}
	if got := req.Header.Get("X-API-Key"); got != "k" {
		t.Fatalf("X-API-Key overwritten by extra headers: %q", got)
	}
	if req.Header.Get("User-Agent") != "e2b-go/x" {
		t.Fatalf("extra headers not applied: %+v", req.Header)
	}
	if req.Header.Get("X-Custom") != "yes" {
		t.Fatalf("custom header not applied: %+v", req.Header)
	}
}

func TestEnvdAuthOmitsAuthorizationWhenNoUser(t *testing.T) {
	a := EnvdAuth{Token: "envd-tok"}
	h := http.Header{}
	applyEnvdHeader(h, a, a.User)
	if h.Get("X-Access-Token") != "envd-tok" {
		t.Fatalf("X-Access-Token=%q", h.Get("X-Access-Token"))
	}
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be omitted when User is empty, got %q", got)
	}
	if got := h.Get("E2b-Sandbox-Id"); got != "" {
		t.Fatalf("E2b-Sandbox-Id should be omitted without SandboxID, got %q", got)
	}
}

func TestEnvdAuthExplicitUser(t *testing.T) {
	a := EnvdAuth{Token: "t", User: "root"}
	h := http.Header{}
	applyEnvdHeader(h, a, a.User)
	if h.Get("Authorization") != "Basic cm9vdDo=" {
		t.Fatalf("Authorization=%q", h.Get("Authorization"))
	}
}

func TestEnvdAuthSandboxRoutingHeaders(t *testing.T) {
	a := EnvdAuth{Token: "t", SandboxID: "sbx-1"}
	h := http.Header{}
	applyEnvdHeader(h, a, "")
	if got := h.Get("E2b-Sandbox-Id"); got != "sbx-1" {
		t.Fatalf("E2b-Sandbox-Id=%q", got)
	}
	if got := h.Get("E2b-Sandbox-Port"); got != "49983" {
		t.Fatalf("E2b-Sandbox-Port=%q", got)
	}
}

func TestEnvdAuthExtraHeadersCannotInjectAuthorization(t *testing.T) {
	for _, key := range []string{"Authorization", "authorization", "AUTHORIZATION"} {
		a := EnvdAuth{
			Token:   "t",
			Headers: map[string]string{key: "Bearer leak", "X-Custom": "c"},
		}
		h := http.Header{}
		applyEnvdHeader(h, a, "")
		if got := h.Get("Authorization"); got != "" {
			t.Fatalf("%s: Authorization must stay omitted on no-user path, got %q", key, got)
		}
		if h.Get("X-Custom") != "c" {
			t.Fatalf("non-reserved extra header should still apply: %q", h.Get("X-Custom"))
		}
	}
}

func TestEnvdAuthExtraHeadersCannotOverrideBasicUser(t *testing.T) {
	a := EnvdAuth{Token: "t", Headers: map[string]string{"Authorization": "Bearer leak"}}
	h := http.Header{}
	applyEnvdHeader(h, a, "user")
	if got := h.Get("Authorization"); got != "Basic dXNlcjo=" {
		t.Fatalf("Authorization=%q, want Basic for resolved user", got)
	}
}

func TestEnvdAuthExtraHeadersDoNotOverwrite(t *testing.T) {
	a := EnvdAuth{
		Token:     "t",
		SandboxID: "sbx",
		Headers: map[string]string{
			"X-Access-Token":   "override",
			"E2b-Sandbox-Id":   "other",
			"E2b-Sandbox-Port": "1",
			"X-Custom":         "c",
		},
	}
	h := http.Header{}
	applyEnvdHeader(h, a, "")
	if h.Get("X-Access-Token") != "t" {
		t.Fatalf("X-Access-Token overwritten by Headers: %q", h.Get("X-Access-Token"))
	}
	if h.Get("E2b-Sandbox-Id") != "sbx" || h.Get("E2b-Sandbox-Port") != "49983" {
		t.Fatalf("routing headers overwritten: %v", h)
	}
	if h.Get("X-Custom") != "c" {
		t.Fatalf("extra X-Custom missing: %q", h.Get("X-Custom"))
	}
}

func TestApplyEnvdHeadersPerCallUser(t *testing.T) {
	auth := EnvdAuth{Token: "t", User: "user", SandboxID: "sbx", Headers: map[string]string{"User-Agent": "ua"}}
	req := newReq(t)
	req.Header.Set("Authorization", "Bearer stale")
	ApplyEnvdHeaders(req, auth, "root")
	if got := req.Header.Get("Authorization"); got != "Basic cm9vdDo=" {
		t.Fatalf("Authorization=%q", got)
	}
	if req.Header.Get("User-Agent") != "ua" || req.Header.Get("E2b-Sandbox-Id") != "sbx" {
		t.Fatalf("headers missing: %v", req.Header)
	}

	req2 := newReq(t)
	req2.Header.Set("Authorization", "Bearer stale")
	ApplyEnvdHeaders(req2, auth, "")
	if got := req2.Header.Get("Authorization"); got != "" {
		t.Fatalf("empty resolved user must strip Authorization, got %q", got)
	}
}

func TestEnvdUserContextOverride(t *testing.T) {
	a := EnvdAuth{User: "user"}
	if got := a.userFor(context.Background()); got != "user" {
		t.Fatalf("default user=%q", got)
	}
	if got := a.userFor(WithEnvdUser(context.Background(), "root")); got != "root" {
		t.Fatalf("override user=%q", got)
	}
	if got := a.userFor(WithEnvdUser(context.Background(), "")); got != "" {
		t.Fatalf("explicit empty override must win, got %q", got)
	}
}

func TestBasicUserAuthKnownVector(t *testing.T) {
	if got := basicUserAuth("user"); got != "dXNlcjo=" {
		t.Fatalf("basicUserAuth(\"user\") = %q, want dXNlcjo= (parity with Python/JS)", got)
	}
}

func TestNewEnvdClientsWiresBothClients(t *testing.T) {
	hc := &http.Client{}
	clients, err := NewEnvdClients("https://49983-sbx-1.example.com", hc, EnvdAuth{Token: "t"})
	if err != nil {
		t.Fatalf("NewEnvdClients: %v", err)
	}
	if clients.Process == nil || clients.Filesystem == nil || clients.API == nil {
		t.Fatalf("clients not wired: %+v", clients)
	}
	if clients.Auth.Token != "t" {
		t.Fatalf("Auth not retained: %+v", clients.Auth)
	}
}

func TestNewEnvdClientsCopiesHeaders(t *testing.T) {
	hdrs := map[string]string{"X-A": "1"}
	clients, err := NewEnvdClients("https://h", &http.Client{}, EnvdAuth{Headers: hdrs})
	if err != nil {
		t.Fatal(err)
	}
	hdrs["X-A"] = "mutated"
	if clients.Auth.Headers["X-A"] != "1" {
		t.Fatal("NewEnvdClients must not alias the caller's header map")
	}
}

type capture struct {
	mu   sync.Mutex
	reqs []http.Header
	path []string
}

func (c *capture) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.reqs = append(c.reqs, r.Header.Clone())
		c.path = append(c.path, r.URL.Path)
		c.mu.Unlock()
		w.WriteHeader(status)
	}
}

func (c *capture) last(t *testing.T) http.Header {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("no request captured")
	}
	return c.reqs[len(c.reqs)-1]
}

func TestNewEnvdClientsUsesProtoJSON(t *testing.T) {
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	clients, err := NewEnvdClients(server.URL, server.Client(), EnvdAuth{})
	if err != nil {
		t.Fatalf("NewEnvdClients: %v", err)
	}
	_, _ = clients.Process.List(context.Background(), connect.NewRequest(&processpb.ListRequest{}))
	if !strings.Contains(contentType, "json") {
		t.Fatalf("Content-Type=%q, want ProtoJSON", contentType)
	}
}

func TestEnvdUnaryRPCHeaders(t *testing.T) {
	c := &capture{}
	server := httptest.NewServer(c.handler(http.StatusBadRequest))
	defer server.Close()

	auth := EnvdAuth{
		Token:     "tok",
		SandboxID: "sbx-9",
		Headers:   map[string]string{"User-Agent": "ua/1", "Authorization": "Bearer leak", "X-Extra": "e"},
	}
	clients, err := NewEnvdClients(server.URL, server.Client(), auth)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = clients.Process.List(context.Background(), connect.NewRequest(&processpb.ListRequest{}))
	h := c.last(t)
	for k, want := range map[string]string{
		"X-Access-Token":   "tok",
		"E2b-Sandbox-Id":   "sbx-9",
		"E2b-Sandbox-Port": "49983",
		"User-Agent":       "ua/1",
		"X-Extra":          "e",
		"Authorization":    "",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	if got := h.Get("Keepalive-Ping-Interval"); got != "" {
		t.Errorf("unary call should not request keepalive, got %q", got)
	}

	ctx := WithEnvdUser(context.Background(), "root")
	_, _ = clients.Process.List(ctx, connect.NewRequest(&processpb.ListRequest{}))
	if got := c.last(t).Get("Authorization"); got != "Basic cm9vdDo=" {
		t.Errorf("per-call Authorization=%q", got)
	}
}

func TestEnvdStreamingRPCHeaders(t *testing.T) {
	c := &capture{}
	server := httptest.NewServer(c.handler(http.StatusBadRequest))
	defer server.Close()

	clients, err := NewEnvdClients(server.URL, server.Client(), EnvdAuth{Token: "tok", User: "user", SandboxID: "sbx"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := clients.Process.Start(context.Background(), connect.NewRequest(&processpb.StartRequest{
		Process: &processpb.ProcessConfig{Cmd: "true"},
	}))
	if err == nil {
		for stream.Receive() {
		}
		_ = stream.Close()
	}
	h := c.last(t)
	if got := h.Get("Keepalive-Ping-Interval"); got != "50" {
		t.Errorf("Keepalive-Ping-Interval=%q, want 50", got)
	}
	if got := h.Get("Authorization"); got != "Basic dXNlcjo=" {
		t.Errorf("Authorization=%q", got)
	}
	if got := h.Get("E2b-Sandbox-Id"); got != "sbx" {
		t.Errorf("E2b-Sandbox-Id=%q", got)
	}
}

func TestEnvdAPIClientHeaders(t *testing.T) {
	c := &capture{}
	server := httptest.NewServer(c.handler(http.StatusNoContent))
	defer server.Close()

	clients, err := NewEnvdClients(server.URL, server.Client(), EnvdAuth{
		Token: "tok", SandboxID: "sbx", Headers: map[string]string{"Authorization": "Bearer leak"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clients.API.GetHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	h := c.last(t)
	if h.Get("X-Access-Token") != "tok" || h.Get("E2b-Sandbox-Id") != "sbx" || h.Get("E2b-Sandbox-Port") != "49983" {
		t.Fatalf("envdapi headers missing: %v", h)
	}
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("Authorization leaked to envd: %q", got)
	}

	resp, err = clients.API.GetHealth(WithEnvdUser(context.Background(), "root"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := c.last(t).Get("Authorization"); got != "Basic cm9vdDo=" {
		t.Fatalf("per-call Authorization=%q", got)
	}
}

func TestNewVolumeAPIClientAuthorizationIsolation(t *testing.T) {
	c := &capture{}
	server := httptest.NewServer(c.handler(http.StatusNotFound))
	defer server.Close()

	extra := map[string]string{"Authorization": "Bearer control-plane", "X-Extra": "x"}

	withToken, err := NewVolumeAPIClient(server.URL, server.Client(), "vol-jwt", extra)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := withToken.DeleteVolumecontentVolumeIDPath(context.Background(), "vol", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	h := c.last(t)
	if got := h.Get("Authorization"); got != "Bearer vol-jwt" {
		t.Fatalf("Authorization=%q, want volume bearer", got)
	}
	if h.Get("X-Extra") != "x" {
		t.Fatalf("extra header missing: %v", h)
	}

	noToken, err := NewVolumeAPIClient(server.URL, server.Client(), "", extra)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = noToken.DeleteVolumecontentVolumeIDPath(context.Background(), "vol", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := c.last(t).Get("Authorization"); got != "" {
		t.Fatalf("Authorization must be stripped without token, got %q", got)
	}
}
