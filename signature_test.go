package e2b

import (
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
	// Expiration should be "now + 60s", allowing for a ±2s skew.
	if *sig.Expiration < before+60-2 || *sig.Expiration > after+60+2 {
		t.Fatalf("expiration %d not within [%d, %d]", *sig.Expiration, before+60, after+60)
	}
}

func TestGetSignatureRequiresToken(t *testing.T) {
	if _, err := GetSignature("/p", SignatureRead, "", SignatureOptions{}); err == nil {
		t.Fatal("expected error for empty token")
	}
}

// TestGetSignatureParity hard-codes a byte-for-byte reference signature
// computed against the Python SDK's get_signature implementation. If this
// breaks, the Go, JS, or Python signature algorithms have diverged and envd
// will reject requests. The Python reference is:
//
//	raw = "/home/user/out.txt:read:user:token-abc"
//	hashlib.sha256(raw.encode()).digest()  → base64 std encoding, strip '='
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
			// Empty user is still valid (empty slot in the raw string).
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
				t.Fatalf("parity drift:\n  got:  %s\n  want: %s\n  (fix Go, JS, or Python — they must all agree)", sig.Value, tc.want)
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
