package e2b

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGetSignatureWithoutExpiration(t *testing.T) {
	sig, err := GetSignature("/home/user/out.txt", SignatureRead, "token-abc", SignatureOptions{User: "user"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(sig.Value, "v1_") {
		t.Fatalf("expected v1_ prefix, got %q", sig.Value)
	}
	if sig.Expiration != nil {
		t.Fatalf("expected no expiration, got %d", *sig.Expiration)
	}
}

func TestGetSignatureWithExpirationIncludesTimestamp(t *testing.T) {
	before := time.Now().Unix()
	sig, err := GetSignature("/p", SignatureWrite, "t", SignatureOptions{ExpirationInSeconds: 60})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Expiration == nil {
		t.Fatalf("expected expiration, got nil")
	}
	after := time.Now().Unix()
	if *sig.Expiration < before+60-2 || *sig.Expiration > after+60+2 {
		t.Fatalf("expiration %d not within [%d, %d]", *sig.Expiration, before+60, after+60)
	}
}

func TestGetSignatureRequiresToken(t *testing.T) {
	if _, err := GetSignature("/p", SignatureRead, "", SignatureOptions{}); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestGetSignatureRejectsNegativeExpiration(t *testing.T) {
	_, err := GetSignature("/p", SignatureRead, "tok", SignatureOptions{ExpirationInSeconds: -5})
	if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestGetSignatureRejectsInvalidOperation(t *testing.T) {
	for _, op := range []SignatureOperation{"", "delete", "READ", "WRITE"} {
		_, err := GetSignature("/p", op, "tok", SignatureOptions{})
		if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Fatalf("GetSignature(op=%q): want *InvalidArgumentError, got %T (%v)", op, err, err)
		}
	}
}

// TestGetSignatureParity hard-codes a byte-for-byte reference signature
// computed against the Python SDK's get_signature implementation. If this
// breaks, the Go, JS, or Python signature algorithms have diverged and envd
// will reject requests.
func TestGetSignatureParity(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		op    SignatureOperation
		token string
		user  string
		want  string
	}{
		{
			name:  "read",
			path:  "/home/user/out.txt",
			op:    SignatureRead,
			token: "token-abc",
			user:  "user",
			want:  "v1_olmGCzVsZrZ0vgD1/UzrvTgJWLcN5sZzoiTkAHQ0k1I",
		},
		{
			name:  "write",
			path:  "/home/user/out.txt",
			op:    SignatureWrite,
			token: "token-abc",
			user:  "user",
			want:  "v1_m/rfMpzS94/bBwZRX7EPGZHVOBFF9Dak0Hu8XGgHCFg",
		},
		{
			name:  "empty-user",
			path:  "/p",
			op:    SignatureRead,
			token: "t",
			user:  "",
			want:  "v1_b98qT/TAVHUCI6Lp8lvyT1TxE2OPdnKb/4USvk5hlNM",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, err := GetSignature(tc.path, tc.op, tc.token, SignatureOptions{User: tc.user})
			if err != nil {
				t.Fatalf("GetSignature: %v", err)
			}
			if sig.Value != tc.want {
				t.Fatalf("parity drift:\n  got:  %s\n  want: %s", sig.Value, tc.want)
			}
		})
	}
}

func TestGetSignatureReadVsWriteDiverge(t *testing.T) {
	read, err := GetSignature("/p", SignatureRead, "t", SignatureOptions{User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	write, err := GetSignature("/p", SignatureWrite, "t", SignatureOptions{User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Value == write.Value {
		t.Fatalf("read and write signatures should differ for same inputs")
	}
}

func TestUploadURLOmitsPathWhenEmpty(t *testing.T) {
	sbx := &Sandbox{
		ID:              "sbx-1",
		Domain:          "example.com",
		EnvdVersion:     "0.6.13",
		EnvdAccessToken: "tok",
		cfg:             Config{Domain: "example.com"},
	}
	raw, err := sbx.UploadURL("", SignatureOptions{})
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Has("path") {
		t.Fatalf("UploadURL(\"\") must omit path query param, got %s", raw)
	}
	if u.Query().Has("username") {
		t.Fatalf("UploadURL on envd >= 0.4.0 must omit default username, got %s", raw)
	}
	wantSig, err := GetSignature("", SignatureWrite, "tok", SignatureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("signature"); got != wantSig.Value {
		t.Fatalf("signature = %q, want %q", got, wantSig.Value)
	}

	unsigned := &Sandbox{
		ID:          "sbx-1",
		Domain:      "example.com",
		EnvdVersion: "0.6.13",
		cfg:         Config{Domain: "example.com"},
	}
	bareURL, err := unsigned.SignedUploadURL("", SignatureOptions{})
	if err != nil {
		t.Fatalf("SignedUploadURL: %v", err)
	}
	if bareURL != "https://49983-sbx-1.example.com/files" {
		t.Fatalf("bareURL = %q, want https://49983-sbx-1.example.com/files", bareURL)
	}
}

func TestSignedURLsRejectExpirationWithoutAccessToken(t *testing.T) {
	sbx := &Sandbox{
		ID:          "sbx-1",
		Domain:      "example.com",
		EnvdVersion: "0.6.13",
		cfg:         Config{Domain: "example.com"},
	}
	for name, fn := range map[string]func() (string, error){
		"UploadURL":         func() (string, error) { return sbx.UploadURL("/a", SignatureOptions{ExpirationInSeconds: 30}) },
		"DownloadURL":       func() (string, error) { return sbx.DownloadURL("/a", SignatureOptions{ExpirationInSeconds: 30}) },
		"SignedUploadURL":   func() (string, error) { return sbx.SignedUploadURL("/a", SignatureOptions{ExpirationInSeconds: 30}) },
		"SignedDownloadURL": func() (string, error) { return sbx.SignedDownloadURL("/a", SignatureOptions{ExpirationInSeconds: 30}) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := fn()
			if got != "" {
				t.Fatalf("expected empty URL on error, got %q", got)
			}
			if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
				t.Fatalf("want *InvalidArgumentError, got %T: %v", err, err)
			}
		})
	}
}

func TestBuildFileURLOmitsUsernameForNewEnvd(t *testing.T) {
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdVersion: "0.5.7",
		EnvdAccessToken: "tok", cfg: Config{Domain: "example.com"},
	}
	raw, err := sbx.buildFileURL("/p", SignatureWrite, SignatureOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
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
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
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
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("username") != "alice" {
		t.Fatalf("explicit username must survive: %s", raw)
	}
}
