package e2b

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestIntegrationSignatureRoundTrip uploads a file via the signed URL then
// downloads it back. Exercises the full envd /files path including signature
// verification on the server side.
func TestIntegrationSignatureRoundTrip(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{Secure: true})
	ctx, cancel := integrationContext(t)
	defer cancel()
	_ = ctx

	uploadURL, err := sbx.UploadURL("/tmp/signed.txt", SignatureOptions{User: "user", ExpirationInSeconds: 60})
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, uploadURL, strings.NewReader("signed-payload"))
	if err != nil {
		t.Fatal(err)
	}
	if sbx.EnvdAccessToken != "" {
		req.Header.Set("X-Access-Token", sbx.EnvdAccessToken)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("upload status %d: %s", resp.StatusCode, string(body))
	}
	resp.Body.Close()

	downloadURL, err := sbx.DownloadURL("/tmp/signed.txt", SignatureOptions{User: "user"})
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	dreq, _ := http.NewRequest(http.MethodGet, downloadURL, nil)
	if sbx.EnvdAccessToken != "" {
		dreq.Header.Set("X-Access-Token", sbx.EnvdAccessToken)
	}
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode >= 300 {
		body, _ := io.ReadAll(dresp.Body)
		t.Fatalf("download status %d: %s", dresp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(dresp.Body)
	if !bytes.Equal(body, []byte("signed-payload")) {
		t.Fatalf("round-trip body: %q", body)
	}
}
