package transport

import (
	"context"
	"net/http"
	"testing"
)

func TestAuthSetsAPIKey(t *testing.T) {
	auth := Auth{APIKey: "abc"}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
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
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization=%q", got)
	}
}

func TestAuthExtraHeadersDoNotOverwrite(t *testing.T) {
	auth := Auth{
		APIKey: "k",
		Headers: map[string]string{
			"User-Agent":    "e2b-go/x",
			"X-Custom":      "yes",
			"Authorization": "should not overwrite",
		},
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req.Header.Set("Authorization", "Pre-set by caller")
	if err := auth.AsRequestEditor()(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Pre-set by caller" {
		t.Fatalf("pre-existing Authorization overwritten: %q", got)
	}
	if req.Header.Get("User-Agent") != "e2b-go/x" {
		t.Fatalf("extra headers not applied: %+v", req.Header)
	}
	if req.Header.Get("X-Custom") != "yes" {
		t.Fatalf("custom header not applied: %+v", req.Header)
	}
}

func TestEnvdAuthOmitsAuthorizationWhenNoUser(t *testing.T) {
	// The version-gating decision now lives in the e2b package: it leaves User
	// empty for envd >= 0.4.0 so the server infers the default user. An empty
	// User therefore means "set X-Access-Token but omit the Authorization
	// header", matching the upstream authenticationHeader behaviour.
	a := EnvdAuth{Token: "envd-tok"}
	h := http.Header{}
	a.applyHeader(h)
	if h.Get("X-Access-Token") != "envd-tok" {
		t.Fatalf("X-Access-Token=%q", h.Get("X-Access-Token"))
	}
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be omitted when User is empty, got %q", got)
	}
}

func TestEnvdAuthExplicitUser(t *testing.T) {
	a := EnvdAuth{Token: "t", User: "root"}
	h := http.Header{}
	a.applyHeader(h)
	// base64("root:") == "cm9vdDo="
	if h.Get("Authorization") != "Basic cm9vdDo=" {
		t.Fatalf("Authorization=%q", h.Get("Authorization"))
	}
}

func TestEnvdAuthExtraHeadersCannotInjectAuthorization(t *testing.T) {
	// On the inferred-user path (User empty, e.g. envd >= 0.4.0) extra headers
	// must not be able to set Authorization: that would defeat default-user
	// inference and could forward a control-plane bearer to the sandbox.
	a := EnvdAuth{
		Token:   "t",
		Headers: map[string]string{"Authorization": "Bearer leak", "X-Custom": "c"},
	}
	h := http.Header{}
	a.applyHeader(h)
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("Authorization must stay omitted on no-user path, got %q", got)
	}
	if h.Get("X-Custom") != "c" {
		t.Fatalf("non-reserved extra header should still apply: %q", h.Get("X-Custom"))
	}
}

func TestEnvdAuthExtraHeadersDoNotOverwrite(t *testing.T) {
	a := EnvdAuth{
		Token:   "t",
		Headers: map[string]string{"X-Access-Token": "override", "X-Custom": "c"},
	}
	h := http.Header{}
	a.applyHeader(h)
	if h.Get("X-Access-Token") != "t" {
		t.Fatalf("X-Access-Token overwritten by Headers: %q", h.Get("X-Access-Token"))
	}
	if h.Get("X-Custom") != "c" {
		t.Fatalf("extra X-Custom missing: %q", h.Get("X-Custom"))
	}
}

func TestBasicUserAuthKnownVector(t *testing.T) {
	// Parity with Python base64.b64encode(b"user:").decode() → "dXNlcjo="
	if got := basicUserAuth("user"); got != "dXNlcjo=" {
		t.Fatalf("basicUserAuth(\"user\") = %q, want dXNlcjo= (parity with Python/JS)", got)
	}
}

func TestNewAPIClientUsesProvidedDoer(t *testing.T) {
	hc := &http.Client{}
	c, err := NewAPIClient("https://api.example.com", hc, Auth{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAPIClient: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
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
}

func TestNewVolumeAPIClientBuildsClient(t *testing.T) {
	hc := &http.Client{}
	c, err := NewVolumeAPIClient("https://api.example.com", hc, "bearer-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}
