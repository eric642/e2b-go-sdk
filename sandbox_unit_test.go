package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

func assertAuthHeader(t *testing.T, r *http.Request, wantAPIKey string) {
	t.Helper()
	if got := r.Header.Get("X-API-Key"); got != wantAPIKey {
		t.Errorf("%s %s: X-API-Key=%q want %q", r.Method, r.URL.Path, got, wantAPIKey)
	}
}

func readJSON(t *testing.T, r *http.Request, into any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, string(body))
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode body: %v", err)
	}
}

func fakeSandboxResponse(id, domain, envdTok, trafficTok string) map[string]any {
	return map[string]any{
		"sandboxID":          id,
		"clientID":           "client-1",
		"templateID":         "base",
		"envdVersion":        "v1.2.3",
		"domain":             domain,
		"envdAccessToken":    envdTok,
		"trafficAccessToken": trafficTok,
	}
}

func TestSandboxCreateSendsFullBody(t *testing.T) {
	var gotBody apiclient.NewSandboxV2
	var gotIdemKey string
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		if r.Method != http.MethodPost || r.URL.Path != "/v2/sandboxes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotIdemKey = r.Header.Get("Idempotency-Key")
		readJSON(t, r, &gotBody)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-xyz", "example.com", "envd-tok", "traffic-tok"))
	}))

	cpus := int32(4)
	_, err := Create(context.Background(), CreateOptions{
		Config:                 mock.Config,
		Template:               "ubuntu-22-04",
		Timeout:                2 * time.Minute,
		IdempotencyKey:         "idem-key-123",
		CPUCount:               &cpus,
		Metadata:               map[string]string{"team": "sdk"},
		Envs:                   map[string]string{"FOO": "bar"},
		AllowInternetAccess:    false,
		AllowInternetAccessSet: true,
		Network: &NetworkOptions{
			AllowOut:           []string{"10.0.0.0/8"},
			DenyOut:            []string{"8.8.8.8/32"},
			AllowPublicTraffic: new(true),
			MaskRequestHost:    "mask.example.com",
		},
		Lifecycle: &LifecycleOptions{OnTimeout: "pause", Mode: SnapshotModeFull, AutoResume: true},
		Iam: &IamOptions{
			Tokens: map[string]IamToken{
				"aws": {Audience: "sts.amazonaws.com", TokenType: "JWT-SVID"},
			},
		},
		VolumeMounts: []VolumeMount{
			{Name: "data", Path: "/mnt/data"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if gotIdemKey != "idem-key-123" {
		t.Fatalf("Idempotency-Key: %q", gotIdemKey)
	}
	if gotBody.TemplateID != "ubuntu-22-04" {
		t.Fatalf("TemplateID: %q", gotBody.TemplateID)
	}
	if gotBody.Timeout == nil || *gotBody.Timeout != 120 {
		t.Fatalf("Timeout: %+v", gotBody.Timeout)
	}
	if gotBody.CpuCount == nil || *gotBody.CpuCount != 4 {
		t.Fatalf("CpuCount: %+v", gotBody.CpuCount)
	}
	if gotBody.Metadata == nil || (*gotBody.Metadata)["team"] != "sdk" {
		t.Fatalf("Metadata: %+v", gotBody.Metadata)
	}
	if gotBody.EnvVars == nil || (*gotBody.EnvVars)["FOO"] != "bar" {
		t.Fatalf("EnvVars: %+v", gotBody.EnvVars)
	}
	if gotBody.AllowInternetAccess == nil || *gotBody.AllowInternetAccess {
		t.Fatalf("AllowInternetAccess: %+v", gotBody.AllowInternetAccess)
	}
	if gotBody.Network == nil ||
		gotBody.Network.AllowOut == nil || (*gotBody.Network.AllowOut)[0] != "10.0.0.0/8" ||
		gotBody.Network.DenyOut == nil || (*gotBody.Network.DenyOut)[0] != "8.8.8.8/32" ||
		gotBody.Network.AllowPublicTraffic == nil || !*gotBody.Network.AllowPublicTraffic ||
		gotBody.Network.MaskRequestHost == nil || *gotBody.Network.MaskRequestHost != "mask.example.com" {
		t.Fatalf("Network: %+v", gotBody.Network)
	}
	if gotBody.AutoPause == nil || !*gotBody.AutoPause {
		t.Fatalf("AutoPause expected true for Lifecycle.OnTimeout=pause: %+v", gotBody.AutoPause)
	}
	if gotBody.AutoPauseMemory == nil || !*gotBody.AutoPauseMemory {
		t.Fatalf("AutoPauseMemory expected true for Mode=full: %+v", gotBody.AutoPauseMemory)
	}
	if gotBody.AutoResume == nil || !gotBody.AutoResume.Enabled {
		t.Fatalf("AutoResume: %+v", gotBody.AutoResume)
	}
	if gotBody.Iam == nil || gotBody.Iam.Tokens == nil || (*gotBody.Iam.Tokens)["aws"].Audience != "sts.amazonaws.com" {
		t.Fatalf("Iam: %+v", gotBody.Iam)
	}
	if gotBody.VolumeMounts == nil || (*gotBody.VolumeMounts)[0].Name != "data" {
		t.Fatalf("VolumeMounts: %+v", gotBody.VolumeMounts)
	}
}

func TestSandboxCreateV1DefaultsSecureTrueAndHonorsInsecure(t *testing.T) {
	var gotBodies []apiclient.NewSandbox
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var b apiclient.NewSandbox
		readJSON(t, r, &b)
		gotBodies = append(gotBodies, b)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-v1", "example.com", "envd-tok", "traffic-tok"))
	}))

	if _, err := CreateV1(context.Background(), CreateOptions{Config: mock.Config}); err != nil {
		t.Fatalf("CreateV1 default: %v", err)
	}
	if _, err := CreateV1(context.Background(), CreateOptions{Config: mock.Config, Insecure: true}); err != nil {
		t.Fatalf("CreateV1 insecure: %v", err)
	}
	if len(gotBodies) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotBodies))
	}
	if gotBodies[0].Secure == nil || !*gotBodies[0].Secure {
		t.Fatalf("CreateV1 must default secure=true, got %+v", gotBodies[0].Secure)
	}
	if gotBodies[1].Secure == nil || *gotBodies[1].Secure {
		t.Fatalf("CreateV1 Insecure=true must send secure=false, got %+v", gotBodies[1].Secure)
	}
}

func TestSandboxCreatePopulatesFields(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-abc", "custom.dev", "envd-xyz", "traffic-xyz"))
	}))
	sbx, err := Create(context.Background(), CreateOptions{Config: mock.Config})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sbx.ID != "sbx-abc" {
		t.Fatalf("ID: %q", sbx.ID)
	}
	if sbx.Domain != "custom.dev" {
		t.Fatalf("Domain: %q", sbx.Domain)
	}
	if sbx.EnvdAccessToken != "envd-xyz" || sbx.TrafficAccessToken != "traffic-xyz" {
		t.Fatalf("tokens: %+v", sbx)
	}
	if sbx.EnvdVersion == "" {
		t.Fatalf("EnvdVersion should be populated")
	}
	if sbx.Commands == nil || sbx.Files == nil || sbx.Git == nil || sbx.Pty == nil {
		t.Fatalf("sub-clients not wired: %+v", sbx)
	}
}

func TestSandboxCreateDefaultsTemplateAndTimeout(t *testing.T) {
	var gotBody apiclient.NewSandbox
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &gotBody)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx", "example.com", "", ""))
	}))
	if _, err := Create(context.Background(), CreateOptions{Config: mock.Config}); err != nil {
		t.Fatal(err)
	}
	if gotBody.TemplateID != DefaultTemplate {
		t.Fatalf("default TemplateID should be %q, got %q", DefaultTemplate, gotBody.TemplateID)
	}
	if gotBody.Timeout == nil || *gotBody.Timeout != int32(DefaultSandboxTimeout/time.Second) {
		t.Fatalf("default Timeout wrong: %+v", gotBody.Timeout)
	}
	if gotBody.AllowInternetAccess != nil {
		t.Fatalf("AllowInternetAccess should be nil when AllowInternetAccessSet=false, got %+v", gotBody.AllowInternetAccess)
	}
}

func TestSandboxCreateLifecycleKillMapsToAutoPauseFalse(t *testing.T) {
	var gotBody apiclient.NewSandbox
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &gotBody)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx", "e2b.app", "", ""))
	}))
	_, err := Create(context.Background(), CreateOptions{
		Config:    mock.Config,
		Lifecycle: &LifecycleOptions{OnTimeout: "kill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.AutoPause == nil || *gotBody.AutoPause {
		t.Fatalf("AutoPause should be false for kill, got %+v", gotBody.AutoPause)
	}
}

func TestSandboxCreateMapsHTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		check  func(t *testing.T, err error)
	}{
		{http.StatusTooManyRequests, func(t *testing.T, err error) {
			var rle *RateLimitError
			if !errors.As(err, &rle) {
				t.Fatalf("want RateLimitError, got %T: %v", err, err)
			}
		}},
		{http.StatusUnauthorized, func(t *testing.T, err error) {
			var ae *AuthenticationError
			if !errors.As(err, &ae) {
				t.Fatalf("want AuthenticationError, got %T: %v", err, err)
			}
		}},
		{http.StatusInternalServerError, func(t *testing.T, err error) {
			var se *SandboxError
			if !errors.As(err, &se) {
				t.Fatalf("want *SandboxError, got %T: %v", err, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"code":` + strconv.Itoa(tc.status) + `,"message":"nope"}`))
			}))
			_, err := Create(context.Background(), CreateOptions{Config: mock.Config})
			if err == nil {
				t.Fatal("expected error")
			}
			tc.check(t, err)
		})
	}
}

func TestSandboxConnectOmitsDefaultTimeoutOnV2(t *testing.T) {
	var gotBody apiclient.ConnectSandboxV2
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sandboxes/sbx-1/connect" {
			t.Errorf("path: %s", r.URL.Path)
		}
		readJSON(t, r, &gotBody)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-1", "example.com", "", ""))
	}))
	sbx, err := Connect(context.Background(), "sbx-1", ConnectOptions{Config: mock.Config})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.Timeout != nil {
		t.Fatalf("v2 Connect should omit timeout when <= 0 so server default applies, got %v", *gotBody.Timeout)
	}
	if gotBody.Memory != nil {
		t.Fatalf("v2 Connect should omit memory when OnResume is empty, got %v", *gotBody.Memory)
	}
	if sbx.ID != "sbx-1" {
		t.Fatalf("id: %q", sbx.ID)
	}
}

func TestSandboxConnectV1UsesDefaultTimeout(t *testing.T) {
	var gotBody apiclient.ConnectSandbox
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxes/sbx-1/connect" {
			t.Errorf("path: %s", r.URL.Path)
		}
		readJSON(t, r, &gotBody)
		writeJSON(t, w, http.StatusCreated, fakeSandboxResponse("sbx-1", "example.com", "", ""))
	}))
	sbx, err := ConnectV1(context.Background(), "sbx-1", ConnectOptions{Config: mock.Config})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.Timeout != int32(DefaultSandboxTimeout/time.Second) {
		t.Fatalf("default timeout should be %d seconds, got %d", int(DefaultSandboxTimeout/time.Second), gotBody.Timeout)
	}
	if sbx.ID != "sbx-1" {
		t.Fatalf("id: %q", sbx.ID)
	}
}

func TestSandboxConnectAcceptsOKResponse(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sandboxes/sbx-1/connect" {
			t.Errorf("path: %s", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, fakeSandboxResponse("sbx-1", "example.com", "", ""))
	}))
	sbx, err := Connect(context.Background(), "sbx-1", ConnectOptions{Config: mock.Config})
	if err != nil {
		t.Fatal(err)
	}
	if sbx.ID != "sbx-1" {
		t.Fatalf("id: %q", sbx.ID)
	}
}

func TestSandboxConnect404(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	_, err := Connect(context.Background(), "sbx-missing", ConnectOptions{Config: mock.Config})
	var sne *SandboxNotFoundError
	if !errors.As(err, &sne) {
		t.Fatalf("want *SandboxNotFoundError, got %T: %v", err, err)
	}
	if sne.SandboxID != "sbx-missing" {
		t.Fatalf("SandboxID: %q", sne.SandboxID)
	}
}

func TestPkgKillSuccess(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/sandboxes/sbx-1" {
			t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	ok, err := Kill(context.Background(), "sbx-1", ConnectOptions{Config: mock.Config})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Kill should return true on 200")
	}
}

func TestPkgKillNotFound(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	ok, err := Kill(context.Background(), "sbx-gone", ConnectOptions{Config: mock.Config})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Kill should return false on 404")
	}
}

func TestPkgKillServerError(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	_, err := Kill(context.Background(), "sbx", ConnectOptions{Config: mock.Config})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestPkgKillDebugShortCircuits(t *testing.T) {
	ok, err := Kill(context.Background(), "sbx", ConnectOptions{Config: Config{Debug: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Debug Kill should return true without I/O")
	}
}

func TestSandboxSetTimeoutSendsBody(t *testing.T) {
	var gotBody apiclient.PostSandboxesSandboxIDTimeoutJSONRequestBody
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx-1/timeout" {
			t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
		}
		readJSON(t, r, &gotBody)
		w.WriteHeader(204)
	}))
	if err := sbx.SetTimeout(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if gotBody.Timeout != 10 {
		t.Fatalf("Timeout: %d", gotBody.Timeout)
	}
}

func TestSandboxSetTimeoutDebugNoop(t *testing.T) {
	sbx := &Sandbox{cfg: Config{Debug: true}}
	if err := sbx.SetTimeout(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxKillBranches(t *testing.T) {
	cases := []struct {
		name   string
		status int
		expErr bool
	}{
		{"Running", 200, false},
		{"NotFound", 404, false},
		{"InternalError", 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			err := sbx.Kill(context.Background())
			if tc.expErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.expErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSandboxPause(t *testing.T) {
	cases := []struct {
		status int
		wantOK bool
		expErr bool
	}{
		{200, true, false},
		{409, false, false},
		{500, false, true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx-1/pause" {
					t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			ok, err := sbx.Pause(context.Background())
			if tc.expErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("wantOK=%v, got %v", tc.wantOK, ok)
			}
		})
	}
}

func TestSandboxCreateSnapshot(t *testing.T) {
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx-1/snapshots" {
			t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"snapshotID": "snap-1",
			"names":      []string{"team/my-snap:v1", "team/my-snap:latest"},
		})
	}))
	info, err := sbx.CreateSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.SnapshotID != "snap-1" || len(info.Names) != 2 {
		t.Fatalf("unexpected snapshot info: %+v", info)
	}
}

func TestSandboxGetInfoParsesBody(t *testing.T) {
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes/sbx-1" {
			t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"sandboxID":   "sbx-1",
			"clientID":    "client-x",
			"templateID":  "base",
			"envdVersion": "v1",
			"state":       "running",
			"cpuCount":    1,
			"memoryMB":    128,
			"diskSizeMB":  512,
			"startedAt":   time.Unix(1, 0).UTC(),
			"endAt":       time.Unix(2, 0).UTC(),
		})
	}))
	info, err := sbx.GetInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.SandboxID != "sbx-1" || info.State != SandboxStateRunning {
		t.Fatalf("info: %+v", info)
	}
}

func TestSandboxIsRunning(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
		wantErr bool
	}{
		{
			"running",
			func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, 200, map[string]any{
					"sandboxID": "sbx-1", "clientID": "c", "templateID": "b", "envdVersion": "v",
					"state": "running", "cpuCount": 1, "memoryMB": 128, "diskSizeMB": 512,
					"startedAt": time.Unix(0, 0), "endAt": time.Unix(1, 0),
				})
			},
			true, false,
		},
		{
			"paused",
			func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, 200, map[string]any{
					"sandboxID": "sbx-1", "clientID": "c", "templateID": "b", "envdVersion": "v",
					"state": "paused", "cpuCount": 1, "memoryMB": 128, "diskSizeMB": 512,
					"startedAt": time.Unix(0, 0), "endAt": time.Unix(1, 0),
				})
			},
			false, false,
		},
		{
			"not-found",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) },
			false, false,
		},
		{
			"error",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
			false, true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sbx := newFakeSandboxWithServer(t, tc.handler)
			ok, err := sbx.IsRunning(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.want {
				t.Fatalf("want %v, got %v", tc.want, ok)
			}
		})
	}
}

func TestSandboxGetMetricsParsesArray(t *testing.T) {
	sbx := newFakeSandboxWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, []map[string]any{
			{
				"cpuCount": 2, "cpuUsedPct": 12.5,
				"diskTotal": 1024, "diskUsed": 256,
				"memTotal": 2048, "memUsed": 512,
				"timestamp":     time.Unix(1000, 0).UTC(),
				"timestampUnix": 1000,
			},
		})
	}))
	m, err := sbx.GetMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].CPUCount != 2 || m[0].DiskUsed != 256 {
		t.Fatalf("metrics: %+v", m)
	}
}

func TestSandboxGetMetricsDebugNoop(t *testing.T) {
	sbx := &Sandbox{cfg: Config{Debug: true}}
	m, err := sbx.GetMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("debug GetMetrics should return nil, got %+v", m)
	}
}

func TestSandboxGetHostDebug(t *testing.T) {
	sbx := &Sandbox{ID: "sbx", Domain: "example.com", cfg: Config{Debug: true}}
	if got := sbx.GetHost(3000); got != "localhost:3000" {
		t.Fatalf("Debug GetHost: %s", got)
	}
}

func TestSandboxGetHostNonDebug(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-1", Domain: "example.com", cfg: Config{Domain: "example.com"}}
	if got := sbx.GetHost(3000); got != "3000-sbx-1.example.com" {
		t.Fatalf("GetHost: %s", got)
	}
}

func TestSandboxUploadURLIncludesSignature(t *testing.T) {
	sbx := &Sandbox{
		ID:              "sbx-1",
		Domain:          "example.com",
		EnvdAccessToken: "tok",
		cfg:             Config{Domain: "example.com"},
	}
	raw, err := sbx.UploadURL("/home/user/foo.txt", SignatureOptions{User: "user"})
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if q.Get("path") != "/home/user/foo.txt" {
		t.Fatalf("path missing: %s", raw)
	}
	if q.Get("username") != "user" {
		t.Fatalf("username missing: %s", raw)
	}
	if !strings.HasPrefix(q.Get("signature"), "v1_") {
		t.Fatalf("signature missing: %s", raw)
	}
	if q.Get("signature_expiration") != "" {
		t.Fatalf("unexpected expiration with ExpirationInSeconds=0: %s", raw)
	}
}

func TestSandboxUploadURLWithExpiration(t *testing.T) {
	sbx := &Sandbox{
		ID: "sbx-1", Domain: "example.com", EnvdAccessToken: "tok",
		cfg: Config{Domain: "example.com"},
	}
	raw, err := sbx.UploadURL("/p", SignatureOptions{User: "user", ExpirationInSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Get("signature_expiration") == "" {
		t.Fatal("expected signature_expiration query parameter")
	}
}

func TestSandboxDownloadURLOmitsSignatureWhenNoToken(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-1", Domain: "example.com", cfg: Config{Domain: "example.com"}}
	raw, err := sbx.DownloadURL("/p", SignatureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Query().Get("signature") != "" {
		t.Fatalf("signature must be omitted when EnvdAccessToken is empty: %s", raw)
	}
	if u.Query().Get("username") != defaultUser {
		t.Fatalf("default username expected: %s", raw)
	}
}

func TestSandboxURLUsesExplicitSandboxURL(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-1", Domain: "example.com", cfg: Config{SandboxURL: "https://tunnel.dev"}}
	u, err := sbx.buildFileURL("/p", SignatureRead, SignatureOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://tunnel.dev/files?") {
		t.Fatalf("explicit SandboxURL not honored: %s", u)
	}
}

func newFakeSandboxWithServer(t *testing.T, h http.Handler) *Sandbox {
	t.Helper()
	mock := newRESTMock(t, h)
	sbx := &Sandbox{
		ID:     "sbx-1",
		Domain: "example.com",
		cfg:    mock.Config.resolve(),
	}
	hc := sbx.cfg.httpClient()
	apiCli, err := apiclient.NewClient(
		mock.Server.URL,
		apiclient.WithHTTPClient(hc),
		apiclient.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			if sbx.cfg.APIKey != "" {
				r.Header.Set("X-API-Key", sbx.cfg.APIKey)
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	sbx.apiCli = apiCli
	sbx.httpCli = hc
	return sbx
}
