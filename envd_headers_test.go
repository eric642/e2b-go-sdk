package e2b

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSandboxEnvdHeaderIsolation(t *testing.T) {
	clearEnv(t)
	var (
		mu  sync.Mutex
		got http.Header
	)
	envd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer envd.Close()

	api := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Control") != "cp" {
			t.Errorf("control-plane request missing Config.Headers: %v", r.Header)
		}
		resp := fakeSandboxResponse("sbx-42", "example.com", "envd-tok", "")
		resp["envdVersion"] = "0.5.0"
		writeJSON(t, w, http.StatusCreated, resp)
	}))
	cfg := api.Config
	cfg.SandboxURL = envd.URL
	cfg.Headers = map[string]string{"X-Control": "cp", "Authorization": "Bearer control-plane"}
	cfg.ExtraSandboxHeaders = map[string]string{"X-Sbx": "1", "Authorization": "Bearer injected"}

	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sbx, err := c.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sbx.envd.API.GetHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	for k, want := range map[string]string{
		"X-Access-Token":   "envd-tok",
		"E2b-Sandbox-Id":   "sbx-42",
		"E2b-Sandbox-Port": "49983",
		"X-Sbx":            "1",
		"X-Control":        "",
		"X-Api-Key":        "",
		"Authorization":    "",
	} {
		if v := got.Get(k); v != want {
			t.Errorf("envd header %s=%q, want %q", k, v, want)
		}
	}
	if got.Get("User-Agent") == "" {
		t.Error("envd request missing User-Agent")
	}
}
