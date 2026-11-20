package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
)

func TestControlPlaneZeroHiddenFallback(t *testing.T) {
	var paths []string
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"endpoint not found"}`))
	}))
	mock.Config.Retries = new(0)
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	checkSingleCall := func(name, want string, fn func() error) {
		t.Helper()
		paths = nil
		err := fn()
		if err == nil {
			t.Fatalf("%s: expected error on 404", name)
		}
		if len(paths) != 1 || paths[0] != want {
			t.Fatalf("%s: expected single call [%s] with ZERO fallback, got %v", name, want, paths)
		}
	}

	checkSingleCall("Create", "POST /v2/sandboxes", func() error {
		_, err := c.Create(ctx, CreateOptions{})
		return err
	})
	checkSingleCall("CreateV1", "POST /sandboxes", func() error {
		_, err := c.CreateV1(ctx, CreateOptions{})
		return err
	})
	checkSingleCall("Connect", "POST /v2/sandboxes/sbx-1/connect", func() error {
		_, err := c.Connect(ctx, "sbx-1", ConnectOptions{})
		return err
	})
	checkSingleCall("ConnectV1", "POST /sandboxes/sbx-1/connect", func() error {
		_, err := c.ConnectV1(ctx, "sbx-1", ConnectOptions{})
		return err
	})
	checkSingleCall("Resume", "POST /sandboxes/sbx-1/resume", func() error {
		_, err := c.Resume(ctx, "sbx-1")
		return err
	})
	checkSingleCall("ResumeV1", "POST /sandboxes/sbx-1/resume", func() error {
		_, err := c.ResumeV1(ctx, "sbx-1", ResumeOptions{})
		return err
	})
	checkSingleCall("List", "GET /v2/sandboxes", func() error {
		_, err := c.ListAll(ctx, SandboxListOptions{})
		return err
	})
	checkSingleCall("ListV1", "GET /sandboxes", func() error {
		_, err := c.ListV1(ctx, SandboxListV1Options{})
		return err
	})
	checkSingleCall("GetLogs", "GET /v2/sandboxes/sbx-1/logs", func() error {
		_, err := c.GetLogs(ctx, "sbx-1")
		return err
	})
	checkSingleCall("GetLogsV1", "GET /sandboxes/sbx-1/logs", func() error {
		_, err := c.GetLogsV1(ctx, "sbx-1")
		return err
	})
}

func TestCreateEnvdFloorGateKillsSandboxAndReturnsTemplateError(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		name := "Create"
		if v1 {
			name = "CreateV1"
		}
		t.Run(name, func(t *testing.T) {
			var killed atomic.Bool
			mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && (r.URL.Path == "/v2/sandboxes" || r.URL.Path == "/sandboxes"):
					resp := fakeSandboxResponse("sbx-old", "example.com", "tok", "")
					resp["envdVersion"] = "0.0.9"
					writeJSON(t, w, http.StatusCreated, resp)
				case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sbx-old":
					killed.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))

			c, err := NewClient(mock.Config)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			var createErr error
			if v1 {
				_, createErr = c.CreateV1(context.Background(), CreateOptions{})
			} else {
				_, createErr = c.Create(context.Background(), CreateOptions{})
			}
			if _, ok := errors.AsType[*TemplateError](createErr); !ok {
				t.Fatalf("want *TemplateError on envd 0.0.9, got %T: %v", createErr, createErr)
			}
			if !killed.Load() {
				t.Fatal("Create must kill newly created sandbox when envd < 0.1.0")
			}
		})
	}
}

func TestCreateValidationErrors(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected network call: %s %s", r.Method, r.URL.Path)
	}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	zeroCPU := int32(0)
	badPort := int32(70000)
	cases := []struct {
		name string
		opts CreateOptions
	}{
		{"UppercaseIdempotencyKey", CreateOptions{IdempotencyKey: "BAD_KEY"}},
		{"ZeroCPUCount", CreateOptions{CPUCount: &zeroCPU}},
		{"ModeWithoutPause", CreateOptions{Lifecycle: &LifecycleOptions{OnTimeout: "kill", Mode: "filesystem"}}},
		{"AutoResumeWithKill", CreateOptions{Lifecycle: &LifecycleOptions{OnTimeout: "kill", AutoResume: true}}},
		{"AutoResumeWithFilesystemMode", CreateOptions{Lifecycle: &LifecycleOptions{OnTimeout: "pause", Mode: "filesystem", AutoResume: true}}},
		{"BadOnTimeout", CreateOptions{Lifecycle: &LifecycleOptions{OnTimeout: "hibernate"}}},
		{"BadIamTokenName", CreateOptions{Iam: &IamOptions{Tokens: map[string]IamToken{"bad}name": {Audience: "a", TokenType: "JWT-SVID"}}}}},
		{"EmptyEgressProxyAddress", CreateOptions{Network: &NetworkOptions{EgressProxy: &EgressProxyOptions{}}}},
		{"InvalidHTTPSPort", CreateOptions{Network: &NetworkOptions{HTTPSPorts: []int32{badPort}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Create(ctx, tc.opts)
			if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
				t.Fatalf("want *InvalidArgumentError, got %T: %v", err, err)
			}
		})
	}
}

func TestSandboxConnectAndConnectV1RefreshTokensAndOnResume(t *testing.T) {
	var v2Body apiclient.ConnectSandboxV2
	var v1Body apiclient.ConnectSandbox
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/sandboxes":
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-1", "old.domain", "old-envd", "old-traffic"))
		case "/v2/sandboxes/sbx-1/connect":
			readJSON(t, r, &v2Body)
			resp := fakeSandboxResponse("sbx-1", "new.domain", "new-envd", "new-traffic")
			resp["envdVersion"] = "0.6.13"
			writeJSON(t, w, http.StatusOK, resp)
		case "/sandboxes/sbx-1/connect":
			readJSON(t, r, &v1Body)
			resp := fakeSandboxResponse("sbx-1", "v1.domain", "v1-envd", "v1-traffic")
			resp["envdVersion"] = "0.5.7"
			writeJSON(t, w, http.StatusOK, resp)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sbx, err := c.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := sbx.Connect(context.Background(), ConnectOptions{Timeout: 90 * time.Second, OnResume: "reboot"}); err != nil {
		t.Fatalf("sbx.Connect: %v", err)
	}
	if v2Body.Timeout == nil || *v2Body.Timeout != 90 || v2Body.Memory == nil || *v2Body.Memory {
		t.Fatalf("v2 Connect body mismatch: %+v", v2Body)
	}
	if sbx.Domain != "new.domain" || sbx.EnvdAccessToken != "new-envd" || sbx.TrafficAccessToken != "new-traffic" || sbx.EnvdVersion != "0.6.13" {
		t.Fatalf("sbx fields not refreshed after Connect: %+v", sbx)
	}

	if err := sbx.ConnectV1(context.Background(), ConnectOptions{OnResume: "restore"}); err != nil {
		t.Fatalf("sbx.ConnectV1: %v", err)
	}
	if v1Body.Timeout != 300 || v1Body.Memory == nil || !*v1Body.Memory {
		t.Fatalf("v1 Connect body mismatch: %+v", v1Body)
	}
	if sbx.Domain != "v1.domain" || sbx.EnvdAccessToken != "v1-envd" || sbx.TrafficAccessToken != "v1-traffic" || sbx.EnvdVersion != "0.5.7" {
		t.Fatalf("sbx fields not refreshed after ConnectV1: %+v", sbx)
	}
}

func TestClientResumeV1AndConflict409(t *testing.T) {
	var gotBody apiclient.ResumedSandbox
	var call int
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx-paused/resume" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		call++
		if call == 1 {
			readJSON(t, r, &gotBody)
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-paused", "example.com", "tok", ""))
			return
		}
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"code":       409,
			"error_code": "sandbox_already_running",
			"message":    "already running",
		})
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cpus := int32(2)
	sbx, err := c.ResumeV1(context.Background(), "sbx-paused", ResumeOptions{
		Timeout:   3 * time.Minute,
		AutoPause: new(true),
		OnResume:  "reboot",
		CPUCount:  &cpus,
	})
	if err != nil {
		t.Fatalf("ResumeV1: %v", err)
	}
	if sbx.ID != "sbx-paused" {
		t.Fatalf("ID: %q", sbx.ID)
	}
	if gotBody.Timeout == nil || *gotBody.Timeout != 180 ||
		gotBody.AutoPause == nil || !*gotBody.AutoPause ||
		gotBody.Memory == nil || *gotBody.Memory ||
		gotBody.CpuCount == nil || *gotBody.CpuCount != 2 {
		t.Fatalf("ResumeV1 body: %+v", gotBody)
	}

	_, err = c.ResumeV1(context.Background(), "sbx-paused", ResumeOptions{})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.ErrorCode() != "sandbox_already_running" {
		t.Fatalf("want *ConflictError with code sandbox_already_running, got %T: %v", err, err)
	}
}

func TestClientSandboxAndPackageResume(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sandboxes":
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-res", "d1.example.com", "tok-1", "tr-1"))
		case "/sandboxes/sbx-res/resume":
			resp := fakeSandboxResponse("sbx-res", "d2.example.com", "tok-2", "tr-2")
			resp["envdVersion"] = "0.6.13"
			writeJSON(t, w, http.StatusCreated, resp)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	resumedFromClient, err := c.Resume(ctx, "sbx-res")
	if err != nil || resumedFromClient.ID != "sbx-res" || resumedFromClient.Domain != "d2.example.com" {
		t.Fatalf("Client.Resume: sbx=%+v err=%v", resumedFromClient, err)
	}

	resumedFromPkg, err := Resume(ctx, "sbx-res", ResumeOptions{Config: mock.Config})
	if err != nil || resumedFromPkg.ID != "sbx-res" || resumedFromPkg.EnvdAccessToken != "tok-2" {
		t.Fatalf("e2b.Resume: sbx=%+v err=%v", resumedFromPkg, err)
	}

	sbx, err := c.CreateV1(ctx, CreateOptions{})
	if err != nil {
		t.Fatalf("CreateV1: %v", err)
	}
	if err := sbx.Resume(ctx, ResumeOptions{OnResume: "restore"}); err != nil {
		t.Fatalf("Sandbox.Resume: %v", err)
	}
	if sbx.Domain != "d2.example.com" || sbx.EnvdAccessToken != "tok-2" || sbx.TrafficAccessToken != "tr-2" || sbx.EnvdVersion != "0.6.13" {
		t.Fatalf("Sandbox.Resume did not refresh fields: %+v", sbx)
	}
}

func TestPauseOmitsBodyWhenZeroAndSendsOptionsWhenSet(t *testing.T) {
	var rawBodies []string
	var contentTypes []string
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxes/sbx-1/pause" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		rawBodies = append(rawBodies, string(b))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusNoContent)
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ok, err := c.Pause(context.Background(), "sbx-1")
	if err != nil || !ok {
		t.Fatalf("Pause empty: ok=%v err=%v", ok, err)
	}
	ok, err = c.Pause(context.Background(), "sbx-1", PauseOptions{
		Memory:              new(false),
		RequireHealthyGuest: new(true),
	})
	if err != nil || !ok {
		t.Fatalf("Pause with options: ok=%v err=%v", ok, err)
	}

	if rawBodies[0] != "" || contentTypes[0] != "" {
		t.Fatalf("zero Pause must send empty body and no Content-Type, got body=%q ct=%q", rawBodies[0], contentTypes[0])
	}
	if !strings.Contains(rawBodies[1], `"memory":false`) || !strings.Contains(rawBodies[1], `"requireHealthyGuest":true`) {
		t.Fatalf("Pause with options body = %s", rawBodies[1])
	}
}

func TestClientSetTimeoutAndRefreshTimeoutV1(t *testing.T) {
	var timeoutSec int32
	var refreshDurs []int
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/sandboxes":
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-1", "test.example.com", "tok", "tr"))
		case "/sandboxes/sbx-1/timeout":
			var b apiclient.SandboxTimeoutRequest
			readJSON(t, r, &b)
			timeoutSec = b.Timeout
			w.WriteHeader(http.StatusNoContent)
		case "/sandboxes/sbx-1/refreshes":
			var b apiclient.SandboxRefreshRequest
			readJSON(t, r, &b)
			if b.Duration != nil {
				refreshDurs = append(refreshDurs, *b.Duration)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sbx, err := c.Create(context.Background())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.SetTimeout(context.Background(), "sbx-1", 45*time.Second); err != nil {
		t.Fatalf("Client.SetTimeout: %v", err)
	}
	if err := c.RefreshTimeout(context.Background(), "sbx-1", 90*time.Second); err != nil {
		t.Fatalf("Client.RefreshTimeout: %v", err)
	}
	if err := c.RefreshTimeoutV1(context.Background(), "sbx-1", 120*time.Second); err != nil {
		t.Fatalf("Client.RefreshTimeoutV1: %v", err)
	}
	if err := sbx.RefreshTimeout(context.Background(), 150*time.Second); err != nil {
		t.Fatalf("Sandbox.RefreshTimeout: %v", err)
	}
	if err := sbx.RefreshTimeoutV1(context.Background(), 180*time.Second); err != nil {
		t.Fatalf("Sandbox.RefreshTimeoutV1: %v", err)
	}
	if timeoutSec != 45 || len(refreshDurs) != 4 || refreshDurs[0] != 90 || refreshDurs[1] != 120 || refreshDurs[2] != 150 || refreshDurs[3] != 180 {
		t.Fatalf("timeoutSec=%d refreshDurs=%v", timeoutSec, refreshDurs)
	}
}

func TestGetInfoNormalizesLegacyEmptyStateAndParsesRichFields(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sandboxes/sbx-legacy":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"sandboxID":   "sbx-legacy",
				"clientID":    "c1",
				"templateID":  "base",
				"envdVersion": "0.1.2",
				"cpuCount":    2,
				"memoryMB":    512,
				"diskSizeMB":  1024,
				"startedAt":   "2026-01-01T00:00:00Z",
				"endAt":       "2026-01-01T01:00:00Z",
			})
		case "/sandboxes/sbx-modern":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"sandboxID":               "sbx-modern",
				"clientID":                "c1",
				"templateID":              "base",
				"envdVersion":             "0.6.13",
				"state":                   "paused",
				"pauseMode":               "filesystem",
				"envdUnresponsiveAtPause": true,
				"cpuCount":                4,
				"memoryMB":                2048,
				"diskSizeMB":              4096,
				"startedAt":               "2026-01-01T00:00:00Z",
				"endAt":                   "2026-01-01T01:00:00Z",
				"network": map[string]any{
					"allowOut":   []string{"api.example.com"},
					"dnsServers": []string{"1.1.1.1"},
					"httpsPorts": []uint32{8443},
					"egressProxy": map[string]any{
						"address":  "proxy.example.com:1080",
						"username": "u1",
						"tls":      map[string]any{"enabled": true, "serverName": "sni.example.com"},
					},
					"rules": map[string]any{
						"api.example.com": []map[string]any{
							{"transform": map[string]any{"headers": map[string]string{"Authorization": "Bearer ${e2b.secrets.api-key}"}}},
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	legacy, err := c.GetInfo(context.Background(), "sbx-legacy")
	if err != nil {
		t.Fatalf("GetInfo legacy: %v", err)
	}
	if legacy.State != SandboxStateRunning {
		t.Fatalf("legacy empty state must normalize to running, got %q", legacy.State)
	}

	modern, err := c.GetInfo(context.Background(), "sbx-modern")
	if err != nil {
		t.Fatalf("GetInfo modern: %v", err)
	}
	if modern.PauseMode != "filesystem" || !modern.EnvdUnresponsiveAtPause {
		t.Fatalf("PauseMode=%q EnvdUnresponsiveAtPause=%v", modern.PauseMode, modern.EnvdUnresponsiveAtPause)
	}
	if modern.Network == nil ||
		len(modern.Network.DNSServers) != 1 || modern.Network.DNSServers[0] != "1.1.1.1" ||
		len(modern.Network.HTTPSPorts) != 1 || modern.Network.HTTPSPorts[0] != 8443 ||
		modern.Network.EgressProxy == nil || modern.Network.EgressProxy.TLS == nil || modern.Network.EgressProxy.TLS.ServerName != "sni.example.com" ||
		len(modern.Network.Rules["api.example.com"]) != 1 {
		t.Fatalf("modern Network not fully parsed: %+v", modern.Network)
	}
}

func TestSandboxIsRunningAdaptiveModes(t *testing.T) {
	t.Run("EnvdHealth204And502AndHeaders", func(t *testing.T) {
		var status atomic.Int32
		status.Store(http.StatusNoContent)
		var gotToken, gotSbxID, gotSbxPort string
		envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("unexpected envd path %s", r.URL.Path)
			}
			gotToken = r.Header.Get("X-Access-Token")
			gotSbxID = r.Header.Get("E2b-Sandbox-Id")
			gotSbxPort = r.Header.Get("E2b-Sandbox-Port")
			w.WriteHeader(int(status.Load()))
		}))
		t.Cleanup(envdSrv.Close)

		var apiCalls atomic.Int32
		apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/sandboxes" {
				writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-h", "example.com", "secret-tok", ""))
				return
			}
			apiCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		apiMock.Config.SandboxURL = envdSrv.URL

		c, err := NewClient(apiMock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		sbx, err := c.Create(context.Background(), CreateOptions{})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		ok, err := sbx.IsRunning(context.Background())
		if err != nil || !ok {
			t.Fatalf("204 should report running=true, got (%v, %v)", ok, err)
		}
		if gotToken != "secret-tok" || gotSbxID != "sbx-h" || gotSbxPort != "49983" {
			t.Fatalf("envd headers missing: token=%q id=%q port=%q", gotToken, gotSbxID, gotSbxPort)
		}

		status.Store(http.StatusBadGateway)
		ok, err = sbx.IsRunning(context.Background())
		if err != nil || ok {
			t.Fatalf("502 should report running=false, got (%v, %v)", ok, err)
		}
		if apiCalls.Load() != 0 {
			t.Fatalf("direct 204/502 envd probes should not hit control plane, got %d calls", apiCalls.Load())
		}
	})

	t.Run("AutoResumePausedDoesNotWakeEnvd", func(t *testing.T) {
		var envdHits atomic.Int32
		envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			envdHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(envdSrv.Close)

		apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/sandboxes":
				writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-ar", "example.com", "tok", ""))
			case "/sandboxes/sbx-ar":
				writeJSON(t, w, http.StatusOK, map[string]any{
					"sandboxID": "sbx-ar", "clientID": "c", "templateID": "base",
					"envdVersion": "0.6.13", "state": "paused",
					"cpuCount": 1, "memoryMB": 512, "diskSizeMB": 1024,
					"startedAt": "2026-01-01T00:00:00Z", "endAt": "2026-01-01T01:00:00Z",
				})
			}
		}))
		apiMock.Config.SandboxURL = envdSrv.URL

		c, err := NewClient(apiMock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		sbx, err := c.Create(context.Background(), CreateOptions{
			Lifecycle: &LifecycleOptions{OnTimeout: "pause", AutoResume: true},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ok, err := sbx.IsRunning(context.Background())
		if err != nil || ok {
			t.Fatalf("paused autoResume sandbox must return (false, nil), got (%v, %v)", ok, err)
		}
		if envdHits.Load() != 0 {
			t.Fatalf("IsRunning must NOT hit envd /health on a paused autoResume sandbox (got %d hits)", envdHits.Load())
		}
	})

	t.Run("LegacyProxy404FallsBackToGetInfo", func(t *testing.T) {
		envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(envdSrv.Close)

		apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sandboxes":
				writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-oldproxy", "example.com", "tok", ""))
			case "/sandboxes/sbx-oldproxy":
				writeJSON(t, w, http.StatusOK, map[string]any{
					"sandboxID": "sbx-oldproxy", "clientID": "c", "templateID": "base",
					"envdVersion": "0.2.0",
					"cpuCount":    1, "memoryMB": 512, "diskSizeMB": 1024,
					"startedAt": "2026-01-01T00:00:00Z", "endAt": "2026-01-01T01:00:00Z",
				})
			}
		}))
		apiMock.Config.SandboxURL = envdSrv.URL

		c, err := NewClient(apiMock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		sbx, err := c.CreateV1(context.Background(), CreateOptions{})
		if err != nil {
			t.Fatalf("CreateV1: %v", err)
		}
		ok, err := sbx.IsRunning(context.Background())
		if err != nil || !ok {
			t.Fatalf("404 on /health should fall back to GetInfo (legacy empty state -> running), got (%v, %v)", ok, err)
		}
	})

	t.Run("DebugModeProbesEnvdOnly", func(t *testing.T) {
		var status atomic.Int32
		status.Store(http.StatusOK)
		envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(int(status.Load()))
		}))
		t.Cleanup(envdSrv.Close)

		cfg := Config{Debug: true, SandboxURL: envdSrv.URL}.resolve()
		sbx := &Sandbox{ID: "dbg", cfg: cfg, httpCli: cfg.DataPlaneHTTPClient()}

		ok, err := sbx.IsRunning(context.Background())
		if err != nil || !ok {
			t.Fatalf("Debug 200 want (true,nil), got (%v,%v)", ok, err)
		}
		status.Store(http.StatusNotFound)
		ok, err = sbx.IsRunning(context.Background())
		if err != nil || ok {
			t.Fatalf("Debug 404 want (false,nil), got (%v,%v)", ok, err)
		}
		envdSrv.Close()
		ok, err = sbx.IsRunning(context.Background())
		if err != nil || ok {
			t.Fatalf("Debug dial error want (false,nil), got (%v,%v)", ok, err)
		}
	})
}

func TestClientIsRunning(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sandboxes/sbx-run":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"sandboxID": "sbx-run", "clientID": "c", "templateID": "base",
				"envdVersion": "0.6.13", "state": "running",
				"cpuCount": 1, "memoryMB": 512, "diskSizeMB": 1024,
				"startedAt": "2026-01-01T00:00:00Z", "endAt": "2026-01-01T01:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ok, err := c.IsRunning(context.Background(), "sbx-run")
	if err != nil || !ok {
		t.Fatalf("sbx-run want (true,nil), got (%v,%v)", ok, err)
	}
	ok, err = c.IsRunning(context.Background(), "sbx-missing")
	if err != nil || ok {
		t.Fatalf("sbx-missing want (false,nil), got (%v,%v)", ok, err)
	}
}

type fakeMCPProcessHandler struct {
	processconnect.UnimplementedProcessHandler
	exitCode  int32
	stderr    string
	gotCmd    string
	gotArgs   []string
	gotEnvs   map[string]string
	gotAuthHd string
}

func (f *fakeMCPProcessHandler) Start(_ context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
	f.gotAuthHd = req.Header().Get("Authorization")
	if p := req.Msg.GetProcess(); p != nil {
		f.gotCmd = p.GetCmd()
		f.gotArgs = p.GetArgs()
		f.gotEnvs = p.GetEnvs()
	}
	if err := stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 42}},
		},
	}); err != nil {
		return err
	}
	if f.stderr != "" {
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{Data: &procpb.ProcessEvent_DataEvent{
					Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte(f.stderr)},
				}},
			},
		})
	}
	var errPtr *string
	if f.exitCode != 0 {
		msg := "exit status 1"
		errPtr = &msg
	}
	return stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{
				ExitCode: f.exitCode,
				Exited:   true,
				Error:    errPtr,
			}},
		},
	})
}

func TestMCPBootstrapSuccessAndFailureCleanup(t *testing.T) {
	t.Run("SuccessDefaultsTemplateCachesToken", func(t *testing.T) {
		proc := &fakeMCPProcessHandler{}
		mux := http.NewServeMux()
		path, handler := processconnect.NewProcessHandler(proc)
		mux.Handle(path, handler)
		envdSrv := httptest.NewServer(mux)
		t.Cleanup(envdSrv.Close)

		var gotTemplate string
		apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body apiclient.NewSandboxV2
			readJSON(t, r, &body)
			gotTemplate = body.TemplateID
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-mcp", "example.com", "envd-tok", ""))
		}))
		apiMock.Config.SandboxURL = envdSrv.URL

		c, err := NewClient(apiMock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		sbx, err := c.Create(context.Background(), CreateOptions{
			Mcp: map[string]any{"github": map[string]any{"token": "abc'def"}},
		})
		if err != nil {
			t.Fatalf("Create with Mcp: %v", err)
		}
		if gotTemplate != DefaultMCPTemplate {
			t.Fatalf("TemplateID = %q, want %q", gotTemplate, DefaultMCPTemplate)
		}
		if proc.gotCmd != "/bin/bash" || len(proc.gotArgs) != 3 || !strings.HasPrefix(proc.gotArgs[2], "mcp-gateway --config ") {
			t.Fatalf("unexpected mcp command: cmd=%q args=%v", proc.gotCmd, proc.gotArgs)
		}
		if proc.gotAuthHd != "Basic cm9vdDo=" {
			t.Fatalf("expected Basic auth for root, got %q", proc.gotAuthHd)
		}
		tok, err := sbx.GetMcpToken(context.Background())
		if err != nil {
			t.Fatalf("GetMcpToken: %v", err)
		}
		if len(tok) != 36 || tok != proc.gotEnvs["GATEWAY_ACCESS_TOKEN"] {
			t.Fatalf("cached token = %q, env GATEWAY_ACCESS_TOKEN = %q", tok, proc.gotEnvs["GATEWAY_ACCESS_TOKEN"])
		}
	})

	t.Run("FailureKillsSandbox", func(t *testing.T) {
		proc := &fakeMCPProcessHandler{exitCode: 1, stderr: "bad config"}
		mux := http.NewServeMux()
		path, handler := processconnect.NewProcessHandler(proc)
		mux.Handle(path, handler)
		envdSrv := httptest.NewServer(mux)
		t.Cleanup(envdSrv.Close)

		var killed atomic.Bool
		apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
				writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-mcp-fail", "example.com", "envd-tok", ""))
			case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sbx-mcp-fail":
				killed.Store(true)
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		apiMock.Config.SandboxURL = envdSrv.URL

		c, err := NewClient(apiMock.Config)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = c.CreateV1(context.Background(), CreateOptions{
			Mcp: map[string]any{"bad": true},
		})
		var se *SandboxError
		if !errors.As(err, &se) || !strings.Contains(se.Error(), "bad config") {
			t.Fatalf("want *SandboxError containing stderr, got %T: %v", err, err)
		}
		if !killed.Load() {
			t.Fatal("failed MCP bootstrap must kill the newly created sandbox")
		}
	})
}

func TestSandboxForkPartialSuccessAndValidation(t *testing.T) {
	var gotPayload map[string]any
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx-src/fork" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		readJSON(t, r, &gotPayload)
		writeJSON(t, w, http.StatusCreated, []map[string]any{
			{"sandbox": fakeSandboxResponse("sbx-fork-1", "example.com", "tok-1", "")},
			{"error": map[string]any{"code": 429, "error_code": "sandbox_capacity_unavailable", "message": "rate limited"}},
			{"error": map[string]any{"code": 404, "message": "snapshot missing"}},
		})
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Fork(context.Background(), "sbx-src", ForkOptions{Count: 25}); err == nil {
		t.Fatal("Fork Count > 20 should fail validation")
	}

	results, err := c.Fork(context.Background(), "sbx-src", ForkOptions{
		Count:     3,
		Timeout:   2 * time.Minute,
		Metadata:  map[string]string{"env": "test"},
		Envs:      map[string]string{"K": "V"},
		Lifecycle: &LifecycleOptions{OnTimeout: "pause", AutoResume: true},
	})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 ForkResults, got %d", len(results))
	}
	if results[0].Err != nil || results[0].Sandbox == nil || results[0].Sandbox.ID != "sbx-fork-1" {
		t.Fatalf("result[0]: %+v", results[0])
	}
	var rle *RateLimitError
	if !errors.As(results[1].Err, &rle) || rle.ErrorCode() != "sandbox_capacity_unavailable" {
		t.Fatalf("result[1] want *RateLimitError, got %T: %v", results[1].Err, results[1].Err)
	}
	if _, ok := errors.AsType[*NotFoundError](results[2].Err); !ok {
		t.Fatalf("result[2] want *NotFoundError, got %T: %v", results[2].Err, results[2].Err)
	}
	if gotPayload["count"] != float64(3) || gotPayload["timeout"] != float64(120) {
		t.Fatalf("fork payload: %+v", gotPayload)
	}
}

func TestUpdateNetworkSendsFullEgressPolicy(t *testing.T) {
	var got apiclient.SandboxNetworkUpdateConfig
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/sandboxes/sbx-1/network" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		readJSON(t, r, &got)
		w.WriteHeader(http.StatusNoContent)
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = c.UpdateNetwork(context.Background(), "sbx-1", NetworkUpdateOptions{
		AllowOut:            []string{"api.openai.com"},
		DenyOut:             []string{AllTraffic},
		DNSServers:          []string{"1.1.1.1"},
		AllowInternetAccess: new(true),
		EgressProxy: &EgressProxyOptions{
			Address:  "proxy.example.com:1080",
			Username: "u",
			Password: "p",
			TLS:      &EgressProxyTLSOptions{Enabled: true, ServerName: "proxy.example.com"},
		},
		Rules: map[string][]NetworkRule{
			"api.openai.com": {
				{Transform: &NetworkTransform{Headers: map[string]string{
					"Authorization": "Bearer " + SecretPlaceholder("openai"),
					"X-Workload":    IamTokenPlaceholder("aws"),
				}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if got.EgressProxy == nil || got.EgressProxy.Tls == nil || !got.EgressProxy.Tls.Enabled {
		t.Fatalf("EgressProxy: %+v", got.EgressProxy)
	}
	hdrs := *(*got.Rules)["api.openai.com"][0].Transform.Headers
	if hdrs["Authorization"] != "Bearer ${e2b.secrets.openai}" || hdrs["X-Workload"] != "${e2b.identity.tokens.aws}" {
		t.Fatalf("Rule headers: %+v", hdrs)
	}
}

func TestListExtendedFiltersAndListV1(t *testing.T) {
	startedAfter := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/sandboxes":
			q := r.URL.Query()
			if q.Get("order") != "asc" || q.Get("template") != "python" || q.Get("startedAfter") == "" {
				t.Errorf("v2 list query missing filters: %v", q)
			}
			item := fakeListedSandbox("sbx-v2")
			item["envdUnresponsiveAtPause"] = true
			writeJSON(t, w, http.StatusOK, []map[string]any{item})
		case "/sandboxes":
			if !strings.Contains(r.URL.Query().Get("metadata"), "env=legacy") {
				t.Errorf("v1 list metadata=%q", r.URL.Query().Get("metadata"))
			}
			item := fakeListedSandbox("sbx-v1")
			delete(item, "state")
			writeJSON(t, w, http.StatusOK, []map[string]any{item})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	v2Items, err := c.ListAll(context.Background(), SandboxListOptions{
		Order:        ListOrderAsc,
		StartedAfter: &startedAfter,
		Template:     "python",
	})
	if err != nil || len(v2Items) != 1 || !v2Items[0].EnvdUnresponsiveAtPause {
		t.Fatalf("v2 list: items=%+v err=%v", v2Items, err)
	}

	v1Items, err := c.ListV1(context.Background(), SandboxListV1Options{
		Metadata: map[string]string{"env": "legacy"},
	})
	if err != nil || len(v1Items) != 1 || v1Items[0].State != SandboxStateRunning {
		t.Fatalf("v1 list: items=%+v err=%v", v1Items, err)
	}
}

func TestMetricsEnvdGateMemCacheBatchAndLogsV1V2(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sandboxes/sbx-1/metrics":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{
					"cpuCount": 2, "cpuUsedPct": 50.0,
					"diskTotal": 1000, "diskUsed": 200,
					"memTotal": 4096, "memUsed": 1024, "memCache": 256,
					"timestamp": "2026-01-01T00:00:00Z", "timestampUnix": 1767225600,
				},
			})
		case "/sandboxes/metrics":
			if got := strings.Join(r.URL.Query()["sandbox_ids"], ","); !strings.Contains(got, "sbx-1") {
				t.Errorf("batch sandbox_ids=%q", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"sandboxes": map[string]any{
					"sbx-1": map[string]any{
						"cpuCount": 4, "cpuUsedPct": 25.0,
						"diskTotal": 2000, "diskUsed": 400,
						"memTotal": 8192, "memUsed": 2048, "memCache": 512,
						"timestamp": "2026-01-01T00:00:00Z", "timestampUnix": 1767225600,
					},
				},
			})
		case "/v2/sandboxes/sbx-1/logs":
			q := r.URL.Query()
			if q.Get("direction") != "forward" || q.Get("level") != "warn" || q.Get("limit") != "25" || q.Get("cursor") != "1000" || q.Get("search") != "OOM" {
				t.Errorf("v2 logs query=%v", q)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"logs": []map[string]any{
					{"timestamp": "2026-01-01T00:00:01Z", "level": "warn", "message": "OOM warning", "fields": map[string]string{"pid": "9"}},
				},
			})
		case "/sandboxes/sbx-1/logs":
			q := r.URL.Query()
			if q.Get("start") != "500" || q.Get("limit") != "10" {
				t.Errorf("v1 logs query=%v", q)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"logs":       []map[string]any{{"timestamp": "2026-01-01T00:00:01Z", "line": "boot ok"}},
				"logEntries": []map[string]any{{"timestamp": "2026-01-01T00:00:01Z", "level": "info", "message": "boot ok", "fields": map[string]string{}}},
			})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	oldSbx := &Sandbox{ID: "sbx-1", EnvdVersion: "0.1.4", apiCli: c.apiCli}
	if _, err := oldSbx.GetMetrics(context.Background()); err == nil {
		t.Fatal("GetMetrics on envd 0.1.4 must fail with *TemplateError")
	} else if _, ok := errors.AsType[*TemplateError](err); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}

	newSbx := &Sandbox{ID: "sbx-1", EnvdVersion: "0.1.5", apiCli: c.apiCli}
	metrics, err := newSbx.GetMetrics(context.Background())
	if err != nil || len(metrics) != 1 || metrics[0].MemCache != 256 {
		t.Fatalf("GetMetrics: %+v err=%v", metrics, err)
	}

	batch, err := c.GetBatchMetrics(context.Background(), []string{"sbx-1"})
	if err != nil || batch["sbx-1"].MemCache != 512 {
		t.Fatalf("GetBatchMetrics: %+v err=%v", batch, err)
	}

	cursor := int64(1000)
	v2Logs, err := newSbx.GetLogs(context.Background(), SandboxLogsOptions{
		Cursor:    &cursor,
		Limit:     25,
		Direction: LogsDirectionForward,
		Level:     SandboxLogLevelWarn,
		Search:    "OOM",
	})
	if err != nil || len(v2Logs) != 1 || v2Logs[0].Message != "OOM warning" || v2Logs[0].Fields["pid"] != "9" {
		t.Fatalf("GetLogs v2: %+v err=%v", v2Logs, err)
	}

	start := int64(500)
	v1Logs, err := newSbx.GetLogsV1(context.Background(), SandboxLogsV1Options{
		Start: &start,
		Limit: 10,
	})
	if err != nil || len(v1Logs.Logs) != 1 || v1Logs.Logs[0].Line != "boot ok" || len(v1Logs.LogEntries) != 1 {
		t.Fatalf("GetLogsV1: %+v err=%v", v1Logs, err)
	}
}

func TestCreateSnapshotOptionsAndSandboxListSnapshots(t *testing.T) {
	var gotBody apiclient.SandboxSnapshotRequest
	var gotIdem string
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-1/snapshots":
			gotIdem = r.Header.Get("Idempotency-Key")
			readJSON(t, r, &gotBody)
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"snapshotID": "team/my-snap:v1",
				"names":      []string{"team/my-snap:v1"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/snapshots":
			q := r.URL.Query()
			if q.Get("sandboxID") != "sbx-1" || q.Get("name") != "my-snap" {
				t.Errorf("snapshots query = %v", q)
			}
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "team/my-snap:v1", "names": []string{"team/my-snap:v1"}},
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sbx := &Sandbox{ID: "sbx-1", apiCli: c.apiCli}
	snap, err := sbx.CreateSnapshot(context.Background(), CreateSnapshotOptions{
		Name:           "my-snap",
		Mode:           SnapshotModeFilesystem,
		IdempotencyKey: "snap-key-1",
	})
	if err != nil || snap.SnapshotID != "team/my-snap:v1" {
		t.Fatalf("CreateSnapshot: %+v err=%v", snap, err)
	}
	if gotIdem != "snap-key-1" || gotBody.Name == nil || *gotBody.Name != "my-snap" || gotBody.Memory == nil || *gotBody.Memory {
		t.Fatalf("snapshot request mismatch: idem=%q body=%+v", gotIdem, gotBody)
	}

	listed, err := sbx.ListAllSnapshots(context.Background(), SnapshotListOptions{Name: "my-snap"})
	if err != nil || len(listed) != 1 || listed[0].SnapshotID != "team/my-snap:v1" {
		t.Fatalf("Sandbox.ListAllSnapshots: %+v err=%v", listed, err)
	}
}

func TestSecretsCRUDAndPagination(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	exists := true
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/secrets":
			var b apiclient.NewSecret
			readJSON(t, r, &b)
			if b.Name != "openai-key" || b.Value != "sk-1" {
				t.Errorf("create secret body: %+v", b)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"secretID": "sec_1", "name": b.Name, "currentVersion": 1,
				"metadata": b.Metadata, "createdAt": now, "updatedAt": now,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/secrets/openai-key":
			var b apiclient.SecretUpdate
			readJSON(t, r, &b)
			writeJSON(t, w, http.StatusOK, map[string]any{
				"secretID": "sec_1", "name": "openai-key", "currentVersion": 2,
				"metadata": map[string]string{"rot": "true"}, "createdAt": now, "updatedAt": now,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/secrets/openai-key":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
				return
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"secretID": "sec_1", "name": "openai-key", "currentVersion": 2,
				"metadata": map[string]string{"rot": "true"}, "createdAt": now, "updatedAt": now,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/secrets":
			switch r.URL.Query().Get("nextToken") {
			case "":
				w.Header().Set("x-next-token", "p2")
				writeJSON(t, w, http.StatusOK, []map[string]any{
					{"secretID": "sec_1", "name": "openai-key", "currentVersion": 2, "metadata": map[string]string{}, "createdAt": now, "updatedAt": now},
				})
			case "p2":
				writeJSON(t, w, http.StatusOK, []map[string]any{
					{"secretID": "sec_2", "name": "anthropic-key", "currentVersion": 1, "metadata": map[string]string{}, "createdAt": now, "updatedAt": now},
				})
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/secrets/openai-key":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			exists = false
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	if _, err := c.CreateSecret(ctx, "bad{name", "v"); err == nil {
		t.Fatal("CreateSecret with brace in name must fail")
	}

	created, err := c.CreateSecret(ctx, "openai-key", "sk-1", SecretOptions{Metadata: map[string]string{"env": "dev"}})
	if err != nil || created.SecretID != "sec_1" || created.CurrentVersion != 1 {
		t.Fatalf("CreateSecret: %+v err=%v", created, err)
	}
	updated, err := c.UpdateSecret(ctx, "openai-key", "sk-2", SecretOptions{Metadata: map[string]string{"rot": "true"}})
	if err != nil || updated.CurrentVersion != 2 || updated.Metadata["rot"] != "true" {
		t.Fatalf("UpdateSecret: %+v err=%v", updated, err)
	}
	ok, err := c.SecretExists(ctx, "openai-key")
	if err != nil || !ok {
		t.Fatalf("SecretExists(true): ok=%v err=%v", ok, err)
	}
	all, err := c.ListAllSecrets(ctx, SecretListOptions{Limit: 1})
	if err != nil || len(all) != 2 || all[0].Name != "openai-key" || all[1].Name != "anthropic-key" {
		t.Fatalf("ListAllSecrets: %+v err=%v", all, err)
	}
	deleted, err := c.DeleteSecret(ctx, "openai-key")
	if err != nil || !deleted {
		t.Fatalf("DeleteSecret(1st): deleted=%v err=%v", deleted, err)
	}
	deleted, err = c.DeleteSecret(ctx, "openai-key")
	if err != nil || deleted {
		t.Fatalf("DeleteSecret(2nd 404): deleted=%v err=%v", deleted, err)
	}
	ok, err = c.SecretExists(ctx, "openai-key")
	if err != nil || ok {
		t.Fatalf("SecretExists(false): ok=%v err=%v", ok, err)
	}

	if b, mErr := json.Marshal(updated); mErr != nil || len(b) == 0 {
		t.Fatalf("marshal SecretInfo: %v", mErr)
	}
}

func TestSandboxConcurrentConnectAndReadsNoRace(t *testing.T) {
	envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(envdSrv.Close)

	var seq atomic.Int32
	apiMock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/sandboxes":
			writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-race", "d0.example.com", "tok-0", "tr-0"))
		case "/v2/sandboxes/sbx-race/connect", "/sandboxes/sbx-race/connect", "/sandboxes/sbx-race/resume":
			n := int(seq.Add(1))
			ns := strconv.Itoa(n)
			resp := fakeSandboxResponse("sbx-race", "d"+ns+".example.com", "tok-"+ns, "tr-"+ns)
			resp["envdVersion"] = "0.6.13"
			writeJSON(t, w, http.StatusCreated, resp)
		case "/sandboxes/sbx-race/metrics":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{
					"cpuCount": 2, "cpuUsedPct": 10.0,
					"diskTotal": 1000, "diskUsed": 100,
					"memTotal": 2048, "memUsed": 512, "memCache": 64,
					"timestamp": "2026-01-01T00:00:00Z", "timestampUnix": 1767225600,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	apiMock.Config.SandboxURL = envdSrv.URL

	c, err := NewClient(apiMock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sbx, err := c.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := context.Background()
	const iters = 25
	var wg sync.WaitGroup

	wg.Go(func() {
		for range iters {
			if err := sbx.Connect(ctx); err != nil {
				t.Errorf("Connect: %v", err)
			}
		}
	})
	wg.Go(func() {
		for range iters {
			if err := sbx.ConnectV1(ctx); err != nil {
				t.Errorf("ConnectV1: %v", err)
			}
		}
	})
	wg.Go(func() {
		for range iters {
			if err := sbx.Resume(ctx); err != nil {
				t.Errorf("Resume: %v", err)
			}
		}
	})
	wg.Go(func() {
		for range iters {
			ok, err := sbx.IsRunning(ctx)
			if err != nil || !ok {
				t.Errorf("IsRunning: ok=%v err=%v", ok, err)
			}
		}
	})
	wg.Go(func() {
		for range iters {
			if h := sbx.GetHost(8080); h == "" {
				t.Error("empty GetHost")
			}
			if u := sbx.GetMcpURL(); u == "" {
				t.Error("empty GetMcpURL")
			}
		}
	})
	wg.Go(func() {
		for range iters {
			if _, err := sbx.GetMetrics(ctx); err != nil {
				t.Errorf("GetMetrics: %v", err)
			}
		}
	})

	wg.Wait()
}

func TestClientHealthPackageSymmetryAndVariadics(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)

	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			if healthy.Load() {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("Health check successful"))
			} else {
				writeJSON(t, w, http.StatusServiceUnavailable, map[string]any{
					"code":    503,
					"message": "Service is unavailable",
				})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes/sbx-1/connect":
			writeJSON(t, w, http.StatusOK, fakeSandboxResponse("sbx-1", "test.example.com", "tok", "tr"))
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-1/fork":
			writeJSON(t, w, http.StatusCreated, []map[string]any{
				{"sandbox": fakeSandboxResponse("sbx-fork-1", "test.example.com", "tok-f", "tr-f")},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sbx-1/pause":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/sandboxes":
			w.Header().Set("X-Total-Running", "7")
			writeJSON(t, w, http.StatusOK, []map[string]any{fakeListedSandbox("sbx-1")})
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes":
			item := fakeListedSandbox("sbx-1")
			delete(item, "state")
			writeJSON(t, w, http.StatusOK, []map[string]any{item})
		case r.Method == http.MethodGet && r.URL.Path == "/snapshots":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "snap-1:default", "names": []string{"team/snap-1:default"}},
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	ctx := context.Background()
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := c.Health(ctx); err != nil {
		t.Fatalf("Health (200 OK): %v", err)
	}
	healthy.Store(false)
	noRetryCfg := mock.Config
	noRetryCfg.Retries = new(0)
	cNoRetry, err := NewClient(noRetryCfg)
	if err != nil {
		t.Fatalf("NewClient noRetry: %v", err)
	}
	if err := cNoRetry.Health(ctx); err == nil {
		t.Fatal("expected error from unhealthy GET /health")
	}
	healthy.Store(true)

	sbx, err := c.Connect(ctx, "sbx-1")
	if err != nil {
		t.Fatalf("Client.Connect zero opts: %v", err)
	}
	forks, err := sbx.Fork(ctx)
	if err != nil || len(forks) != 1 || forks[0].Sandbox == nil || forks[0].Sandbox.ID != "sbx-fork-1" {
		t.Fatalf("Sandbox.Fork zero opts: forks=%+v err=%v", forks, err)
	}

	paused, err := Pause(ctx, "sbx-1", PauseOptions{Config: mock.Config})
	if err != nil || !paused {
		t.Fatalf("package Pause: paused=%v err=%v", paused, err)
	}

	pag := c.List(ctx)
	if pag.TotalRunning() != nil {
		t.Fatalf("TotalRunning before NextItems = %v, want nil", pag.TotalRunning())
	}
	items, err := pag.NextItems(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List NextItems: len=%d err=%v", len(items), err)
	}
	if tr := pag.TotalRunning(); tr == nil || *tr != 7 {
		t.Fatalf("TotalRunning = %v, want 7", tr)
	}

	v1Items, err := ListV1(ctx, mock.Config)
	if err != nil || len(v1Items) != 1 || v1Items[0].State != SandboxStateRunning {
		t.Fatalf("package ListV1: items=%+v err=%v", v1Items, err)
	}

	snaps, err := c.ListAllSnapshots(ctx)
	if err != nil || len(snaps) != 1 || snaps[0].SnapshotID != "snap-1:default" {
		t.Fatalf("Client.ListAllSnapshots zero opts: snaps=%+v err=%v", snaps, err)
	}
}
