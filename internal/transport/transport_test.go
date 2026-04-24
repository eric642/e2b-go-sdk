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
			"User-Agent":      "e2b-go/x",
			"X-Custom":        "yes",
			"Authorization":   "should not overwrite",
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

func TestEnvdAuthDefaultUser(t *testing.T) {
	a := EnvdAuth{Token: "envd-tok"}
	h := http.Header{}
	a.applyHeader(h)
	if h.Get("X-Access-Token") != "envd-tok" {
		t.Fatalf("X-Access-Token=%q", h.Get("X-Access-Token"))
	}
	// Default user "user" → Basic base64("user:") == "dXNlcjo="
	if h.Get("Authorization") != "Basic dXNlcjo=" {
		t.Fatalf("Authorization=%q", h.Get("Authorization"))
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
