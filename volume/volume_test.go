package volume_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/volume"
)

type routeKey struct {
	method string
	path   string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newVolumeMock(t *testing.T, routes map[routeKey]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[routeKey{r.Method, r.URL.Path}]; ok {
			h(w, r)
			return
		}
		t.Errorf("unhandled %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func cfg(url string) e2b.Config {
	return e2b.Config{
		APIKey:       "test-key",
		APIURL:       url,
		VolumeAPIURL: url,
		Domain:       "example.com",
		Retries:      new(0),
	}
}

func TestVolumeCreateReturnsTokenisedHandle(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") != "test-key" {
				t.Errorf("missing auth header")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "vol1" {
				t.Errorf("name: %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"volumeID": "vol-123",
				"name":     "vol1",
				"token":    "jwt-abc",
			})
		},
	})

	v, err := volume.Create(context.Background(), "vol1", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.ID != "vol-123" || v.Name != "vol1" || v.Token() != "jwt-abc" || v.Domain != "example.com" {
		t.Fatalf("Volume: %+v tok=%q", v, v.Token())
	}
}

func TestVolumeConnectValidatesExistence(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumes/vol-123"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-123", "name": "vol1"})
		},
	})
	v, err := volume.Connect(context.Background(), "vol-123", "token-x", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "vol-123" || v.Token() != "token-x" {
		t.Fatalf("Volume: %+v tok=%q", v, v.Token())
	}
}

func TestVolumeConnectPropagates404(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumes/missing"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":       404,
				"error_code": "volume_not_found",
				"message":    "volume missing not found",
			})
		},
	})
	_, err := volume.Connect(context.Background(), "missing", "t", volume.Options{Config: cfg(srv.URL)})
	if err == nil {
		t.Fatal("expected error")
	}
	ve, ok := errors.AsType[*e2b.VolumeError](err)
	if !ok {
		t.Fatalf("want *e2b.VolumeError, got %T: %v", err, err)
	}
	if ve.HTTPStatus() != http.StatusNotFound || ve.ErrorCode() != "volume_not_found" {
		t.Fatalf("status=%d code=%q", ve.HTTPStatus(), ve.ErrorCode())
	}
	if _, ok := errors.AsType[*e2b.NotFoundError](err); !ok {
		t.Fatalf("want underlying *e2b.NotFoundError, got %v", err)
	}
}

func TestVolumeListDecodesArray(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"volumeID": "a", "name": "A"},
				{"volumeID": "b", "name": "B"},
			})
		},
	})
	list, err := volume.List(context.Background(), volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "A" || list[1].ID != "b" {
		t.Fatalf("list: %+v", list)
	}
}

func TestVolumeDeleteSwallows404(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-1", "name": "v", "token": "t"})
		},
		{http.MethodDelete, "/volumes/vol-1"}: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})
	v, err := volume.Create(context.Background(), "v", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(context.Background()); err != nil {
		t.Fatalf("Delete should swallow 404, got %v", err)
	}
}

func TestVolumeDeletePropagates500(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-1", "name": "v", "token": "t"})
		},
		{http.MethodDelete, "/volumes/vol-1"}: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	v, err := volume.Create(context.Background(), "v", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	err = v.Delete(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*e2b.VolumeError](err); !ok {
		t.Fatalf("want *e2b.VolumeError, got %T", err)
	}
}

func TestVolumeWriteAndReadRoundTrip(t *testing.T) {
	var gotBody []byte
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-1", "name": "v", "token": "jwt-z"})
		},
		{http.MethodPut, "/volumecontent/vol-1/file"}: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != "/p/foo.txt" {
				t.Errorf("path query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("Authorization") != "Bearer jwt-z" {
				t.Errorf("Authorization: %q", r.Header.Get("Authorization"))
			}
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		},
		{http.MethodGet, "/volumecontent/vol-1/file"}: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != "/p/foo.txt" {
				t.Errorf("path query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte("hello world"))
		},
	})
	v, err := volume.Create(context.Background(), "v", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFile(context.Background(), "/p/foo.txt", []byte("hello world")); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != "hello world" {
		t.Fatalf("server body: %q", gotBody)
	}
	data, err := v.ReadFile(context.Background(), "/p/foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("read back: %q", data)
	}
}

func TestVolumeRemoveAndMakeDir(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-1", "name": "v", "token": "t"})
		},
		{http.MethodDelete, "/volumecontent/vol-1/path"}: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != "/p/bar" {
				t.Errorf("path: %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusNoContent)
		},
		{http.MethodPost, "/volumecontent/vol-1/dir"}: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != "/p/baz" {
				t.Errorf("path: %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusOK)
		},
	})
	v, err := volume.Create(context.Background(), "v", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Remove(context.Background(), "/p/bar"); err != nil {
		t.Fatal(err)
	}
	if err := v.MakeDir(context.Background(), "/p/baz"); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeReadFilePropagatesHTTPError(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "vol-1", "name": "v", "token": "t"})
		},
		{http.MethodGet, "/volumecontent/vol-1/file"}: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nope"))
		},
	})
	v, err := volume.Create(context.Background(), "v", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.ReadFile(context.Background(), "/missing.txt")
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*e2b.VolumeError](err); !ok {
		t.Fatalf("want *e2b.VolumeError, got %T", err)
	}
	fnf, ok := errors.AsType[*e2b.FileNotFoundError](err)
	if !ok {
		t.Fatalf("want *e2b.FileNotFoundError, got %v", err)
	}
	if fnf.Path != "/missing.txt" || fnf.HTTPStatus() != http.StatusNotFound {
		t.Fatalf("fnf: %+v", fnf)
	}
}

func TestClientEnvResolutionAndConfigIsolation(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-API-Key"); got != "env-api-key" {
				t.Errorf("X-API-Key = %q, want env-api-key", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		},
		{http.MethodGet, "/volumecontent/vol-env/path"}: func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer jwt-env" {
				t.Errorf("Authorization = %q, want Bearer jwt-env", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  "data",
				"type":  "directory",
				"path":  "/data",
				"size":  0,
				"mode":  0o755,
				"uid":   0,
				"gid":   0,
				"atime": "2026-01-01T00:00:00Z",
				"mtime": "2026-01-01T00:00:00Z",
				"ctime": "2026-01-01T00:00:00Z",
			})
		},
	})

	t.Setenv("E2B_API_KEY", "env-api-key")
	t.Setenv("E2B_API_URL", srv.URL)
	t.Setenv("E2B_VOLUME_API_URL", srv.URL)
	t.Setenv("E2B_DOMAIN", "env.example.org")

	callerHeaders := map[string]string{"X-Custom": "one"}
	cli, err := volume.NewClient(e2b.Config{Headers: callerHeaders})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	callerHeaders["X-Custom"] = "mutated"
	snap := cli.Config()
	if snap.Domain != "env.example.org" || snap.APIKey != "env-api-key" {
		t.Fatalf("resolved config = %+v", snap)
	}
	snap.Headers["X-Custom"] = "mutated-again"
	if cli.Config().Headers["X-Custom"] != "one" {
		t.Fatalf("Client.Config leaked map alias: %+v", cli.Config().Headers)
	}

	if _, err := cli.List(context.Background()); err != nil {
		t.Fatalf("List via env config: %v", err)
	}
	vol, err := cli.ConnectWithToken("vol-env", "jwt-env")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	stat, err := vol.Stat(context.Background(), "/data")
	if err != nil {
		t.Fatalf("Stat via E2B_VOLUME_API_URL: %v", err)
	}
	if stat.Type != volume.EntryTypeDirectory || stat.Path != "/data" {
		t.Fatalf("stat = %+v", stat)
	}
}

func TestClientRejectsInvalidProxyOrHTTPVersion(t *testing.T) {
	if _, err := volume.NewClient(e2b.Config{Proxy: "ftp://bad-proxy:1234"}); err == nil {
		t.Fatal("expected invalid proxy error")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("want *e2b.InvalidArgumentError, got %T: %v", err, err)
	}

	if _, err := volume.NewClient(e2b.Config{HTTPVersion: "3"}); err == nil {
		t.Fatal("expected invalid HTTPVersion error")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("want *e2b.InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestBYOCDomainRoutingAndFallback(t *testing.T) {
	var lastContentHost string
	var lastContentScheme string
	var lastAuth string

	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodGet && r.URL.Host == "api.cloud.e2b.test" && r.URL.Path == "/volumes/vol-byoc":
				body, _ := json.Marshal(map[string]any{
					"volumeID": "vol-byoc",
					"name":     "byoc-vol",
					"token":    "minted-byoc-jwt",
					"domain":   "us-west.byoc.customer.internal",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil

			case r.Method == http.MethodPost && r.URL.Host == "api.cloud.e2b.test" && r.URL.Path == "/volumes":
				body, _ := json.Marshal(map[string]any{
					"volumeID": "vol-legacy",
					"name":     "legacy-vol",
					"token":    "minted-legacy-jwt",
				})
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil

			case strings.HasPrefix(r.URL.Path, "/volumecontent/"):
				lastContentScheme = r.URL.Scheme
				lastContentHost = r.URL.Host
				lastAuth = r.Header.Get("Authorization")
				body, _ := json.Marshal(map[string]any{
					"name":  "hello.txt",
					"type":  "file",
					"path":  "/hello.txt",
					"size":  5,
					"mode":  0o644,
					"uid":   1000,
					"gid":   1000,
					"atime": "2026-01-01T00:00:00Z",
					"mtime": "2026-01-01T00:00:00Z",
					"ctime": "2026-01-01T00:00:00Z",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			return nil, nil
		}),
	}

	baseCfg := e2b.Config{
		APIKey:     "cp-secret-key",
		Domain:     "cloud.e2b.test",
		HTTPClient: hc,
		Retries:    new(0),
		Headers: map[string]string{
			"Authorization": "Bearer should-not-leak",
		},
	}

	byocVol, err := volume.ConnectByID(context.Background(), "vol-byoc", baseCfg)
	if err != nil {
		t.Fatalf("ConnectByID: %v", err)
	}
	if byocVol.Domain != "us-west.byoc.customer.internal" || byocVol.Token() != "minted-byoc-jwt" {
		t.Fatalf("byocVol = %+v, token=%q", byocVol, byocVol.Token())
	}
	if _, err := byocVol.Stat(context.Background(), "/hello.txt"); err != nil {
		t.Fatalf("byoc Stat: %v", err)
	}
	if lastContentScheme != "https" || lastContentHost != "api.us-west.byoc.customer.internal" {
		t.Fatalf("BYOC content target = %s://%s, want https://api.us-west.byoc.customer.internal", lastContentScheme, lastContentHost)
	}
	if lastAuth != "Bearer minted-byoc-jwt" {
		t.Fatalf("BYOC Authorization = %q, want Bearer minted-byoc-jwt", lastAuth)
	}

	cli, err := volume.NewClient(baseCfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	legacyVol, err := cli.Create(context.Background(), "legacy-vol")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if legacyVol.Domain != "cloud.e2b.test" {
		t.Fatalf("legacyVol.Domain = %q, want cloud.e2b.test", legacyVol.Domain)
	}
	if _, err := legacyVol.Stat(context.Background(), "/hello.txt"); err != nil {
		t.Fatalf("legacy Stat: %v", err)
	}
	if lastContentScheme != "https" || lastContentHost != "api.cloud.e2b.test" {
		t.Fatalf("fallback content target = %s://%s, want https://api.cloud.e2b.test", lastContentScheme, lastContentHost)
	}
	if lastAuth != "Bearer minted-legacy-jwt" {
		t.Fatalf("fallback Authorization = %q", lastAuth)
	}
}

func TestDebugModeUsesLocalhost3000And8080(t *testing.T) {
	var sawCP, sawContent string
	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/volumes/vol-dbg" {
				sawCP = r.URL.Scheme + "://" + r.URL.Host
				body, _ := json.Marshal(map[string]any{
					"volumeID": "vol-dbg",
					"name":     "dbg",
					"token":    "dbg-token",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}
			if strings.HasPrefix(r.URL.Path, "/volumecontent/") {
				sawContent = r.URL.Scheme + "://" + r.URL.Host
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Header:     http.Header{},
					Body:       io.NopCloser(bytes.NewReader(nil)),
				}, nil
			}
			t.Fatalf("unexpected request %s", r.URL.String())
			return nil, nil
		}),
	}

	cli, err := volume.NewClient(e2b.Config{
		Debug:      true,
		HTTPClient: hc,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	vol, err := cli.Connect(context.Background(), "vol-dbg")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := vol.Remove(context.Background(), "/tmp"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if sawCP != "http://localhost:3000" {
		t.Fatalf("control-plane debug URL = %q, want http://localhost:3000", sawCP)
	}
	if sawContent != "http://localhost:8080" {
		t.Fatalf("content debug URL = %q, want http://localhost:8080", sawContent)
	}
}

func TestConnectWithTokenZeroRTTAndGetInfoAndDestroy(t *testing.T) {
	var cpCalls atomic.Int32
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumes/vol-info"}: func(w http.ResponseWriter, r *http.Request) {
			cpCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"volumeID": "vol-info",
				"name":     "info-name",
				"token":    "info-jwt",
				"domain":   "byoc.domain.dev",
			})
		},
		{http.MethodDelete, "/volumes/vol-exists"}: func(w http.ResponseWriter, r *http.Request) {
			cpCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		},
		{http.MethodDelete, "/volumes/vol-gone"}: func(w http.ResponseWriter, r *http.Request) {
			cpCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		},
	})

	cli, err := volume.NewClient(cfg(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	v, err := cli.ConnectWithToken("vol-zero", "pre-jwt", "custom.byoc.dev")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	vPkg, err := volume.ConnectWithToken("vol-zero-pkg", "pre-jwt-pkg", volume.Options{Config: cfg(srv.URL)}, "custom.byoc.dev")
	if err != nil {
		t.Fatalf("package ConnectWithToken: %v", err)
	}
	if cpCalls.Load() != 0 {
		t.Fatalf("ConnectWithToken made %d control-plane calls, want 0", cpCalls.Load())
	}
	if v.ID != "vol-zero" || v.Token() != "pre-jwt" || v.Domain != "custom.byoc.dev" {
		t.Fatalf("ConnectWithToken volume = %+v", v)
	}
	if vPkg.ID != "vol-zero-pkg" || vPkg.Token() != "pre-jwt-pkg" || vPkg.Domain != "custom.byoc.dev" {
		t.Fatalf("package ConnectWithToken volume = %+v", vPkg)
	}

	info, err := volume.GetInfo(context.Background(), "vol-info", volume.Options{Config: cfg(srv.URL)})
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.ID != "vol-info" || info.Name != "info-name" || info.Token != "info-jwt" || info.Domain != "byoc.domain.dev" {
		t.Fatalf("info = %+v", info)
	}
	vInfoHandle, err := cli.ConnectWithToken("vol-info", "info-jwt")
	if err != nil {
		t.Fatalf("ConnectWithToken(vol-info): %v", err)
	}
	handleInfo, err := vInfoHandle.GetInfo(context.Background())
	if err != nil || handleInfo.Name != "info-name" || handleInfo.Domain != "byoc.domain.dev" {
		t.Fatalf("(*Volume).GetInfo = %+v, err=%v", handleInfo, err)
	}

	ok, err := cli.Destroy(context.Background(), "vol-exists")
	if err != nil || !ok {
		t.Fatalf("Destroy(existing) = (%v, %v), want (true, nil)", ok, err)
	}
	vGone, err := cli.ConnectWithToken("vol-gone", "tok")
	if err != nil {
		t.Fatalf("ConnectWithToken(vol-gone): %v", err)
	}
	ok, err = vGone.Destroy(context.Background())
	if err != nil || ok {
		t.Fatalf("(*Volume).Destroy(missing) = (%v, %v), want (false, nil)", ok, err)
	}
	ok, err = volume.Destroy(context.Background(), "vol-gone", volume.Options{Config: cfg(srv.URL)})
	if err != nil || ok {
		t.Fatalf("Destroy(missing) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestVolumeContentFullOperations(t *testing.T) {
	ts := time.Date(2026, 5, 10, 12, 30, 0, 0, time.UTC)
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodGet, "/volumecontent/vol-full/path"}: func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Query().Get("path")
			if p == "/missing" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":    "NOT_FOUND",
					"message": "entry does not exist",
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":   "link",
				"type":   "symlink",
				"path":   p,
				"size":   12,
				"mode":   0o777,
				"uid":    1001,
				"gid":    1002,
				"atime":  ts.Format(time.RFC3339),
				"mtime":  ts.Format(time.RFC3339),
				"ctime":  ts.Format(time.RFC3339),
				"target": "/real/file.txt",
			})
		},
		{http.MethodPatch, "/volumecontent/vol-full/path"}: func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("path"); got != "/file.txt" {
				t.Errorf("PATCH path = %q", got)
			}
			var body struct {
				UID  *uint32 `json:"uid"`
				GID  *uint32 `json:"gid"`
				Mode *uint32 `json:"mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode PATCH body: %v", err)
			}
			if body.UID == nil || *body.UID != 500 || body.GID == nil || *body.GID != 501 || body.Mode == nil || *body.Mode != 0o600 {
				t.Fatalf("unexpected PATCH body: %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  "file.txt",
				"type":  "file",
				"path":  "/file.txt",
				"size":  42,
				"mode":  0o600,
				"uid":   500,
				"gid":   501,
				"atime": ts.Format(time.RFC3339),
				"mtime": ts.Format(time.RFC3339),
				"ctime": ts.Format(time.RFC3339),
			})
		},
		{http.MethodGet, "/volumecontent/vol-full/dir"}: func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("path") != "/dir" {
				t.Errorf("dir path = %q", q.Get("path"))
			}
			depth := q.Get("depth")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"name":  "depth-" + depth,
					"type":  "file",
					"path":  "/dir/a",
					"size":  7,
					"mode":  0o644,
					"uid":   0,
					"gid":   0,
					"atime": ts.Format(time.RFC3339),
					"mtime": ts.Format(time.RFC3339),
					"ctime": ts.Format(time.RFC3339),
				},
			})
		},
		{http.MethodPost, "/volumecontent/vol-full/dir"}: func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("path") != "/a/b/c" || q.Get("uid") != "1000" || q.Get("gid") != "1000" || q.Get("mode") != "493" || q.Get("force") != "true" {
				t.Errorf("mkdir query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  "c",
				"type":  "directory",
				"path":  "/a/b/c",
				"size":  0,
				"mode":  0o755,
				"uid":   1000,
				"gid":   1000,
				"atime": ts.Format(time.RFC3339),
				"mtime": ts.Format(time.RFC3339),
				"ctime": ts.Format(time.RFC3339),
			})
		},
		{http.MethodPut, "/volumecontent/vol-full/file"}: func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("path") != "/stream.bin" || q.Get("uid") != "1001" || q.Get("gid") != "1002" || q.Get("mode") != "420" || q.Get("force") != "true" {
				t.Errorf("PUT file query = %s", r.URL.RawQuery)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q", ct)
			}
			payload, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  "stream.bin",
				"type":  "file",
				"path":  "/stream.bin",
				"size":  len(payload),
				"mode":  0o644,
				"uid":   1001,
				"gid":   1002,
				"atime": ts.Format(time.RFC3339),
				"mtime": ts.Format(time.RFC3339),
				"ctime": ts.Format(time.RFC3339),
			})
		},
		{http.MethodGet, "/volumecontent/vol-full/file"}: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") == "/missing-stream" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":    "NOT_FOUND",
					"message": "stream target missing",
				})
				return
			}
			flusher, _ := w.(http.Flusher)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("chunk-1|"))
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("chunk-2"))
		},
	})

	cli, err := volume.NewClient(cfg(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	v, err := cli.ConnectWithToken("vol-full", "tok-full")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}

	st, err := v.Stat(context.Background(), "/link")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Type != volume.EntryTypeSymlink || st.Target != "/real/file.txt" || st.UID != 1001 || st.GID != 1002 {
		t.Fatalf("Stat = %+v", st)
	}

	ok, err := v.Exists(context.Background(), "/link")
	if err != nil || !ok {
		t.Fatalf("Exists(/link) = (%v, %v)", ok, err)
	}
	ok, err = v.Exists(context.Background(), "/missing")
	if err != nil || ok {
		t.Fatalf("Exists(/missing) = (%v, %v), want (false, nil)", ok, err)
	}

	updated, err := v.UpdateMetadata(context.Background(), "/file.txt", volume.MetadataOptions{
		UID:  new(uint32(500)),
		GID:  new(uint32(501)),
		Mode: new(uint32(0o600)),
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if updated.UID != 500 || updated.GID != 501 || updated.Mode != 0o600 {
		t.Fatalf("updated = %+v", updated)
	}

	itemsDefault, err := v.List(context.Background(), "/dir")
	if err != nil || len(itemsDefault) != 1 || itemsDefault[0].Name != "depth-" {
		t.Fatalf("List default = %+v, err=%v", itemsDefault, err)
	}
	itemsDepth, err := v.List(context.Background(), "/dir", volume.ListOptions{Depth: 3})
	if err != nil || len(itemsDepth) != 1 || itemsDepth[0].Name != "depth-3" {
		t.Fatalf("List depth=3 = %+v, err=%v", itemsDepth, err)
	}
	if _, err := v.List(context.Background(), "/dir", volume.ListOptions{Depth: -1}); err == nil {
		t.Fatal("expected error for negative depth")
	} else if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("want *e2b.InvalidArgumentError, got %v", err)
	}

	dirStat, err := v.MakeDirWithOptions(context.Background(), "/a/b/c", volume.MakeDirOptions{
		UID:     new(uint32(1000)),
		GID:     new(uint32(1000)),
		Mode:    new(uint32(0o755)),
		Parents: new(true),
	})
	if err != nil {
		t.Fatalf("MakeDirWithOptions: %v", err)
	}
	if dirStat.Type != volume.EntryTypeDirectory || dirStat.Path != "/a/b/c" {
		t.Fatalf("dirStat = %+v", dirStat)
	}

	writtenBuf, err := v.WriteFileWithOptions(context.Background(), "/stream.bin", []byte("buffered-payload"), volume.WriteOptions{
		UID:   new(uint32(1001)),
		GID:   new(uint32(1002)),
		Mode:  new(uint32(0o644)),
		Force: new(true),
	})
	if err != nil || writtenBuf.Size != int64(len("buffered-payload")) {
		t.Fatalf("WriteFileWithOptions = %+v, err=%v", writtenBuf, err)
	}
	written, err := v.WriteFileStream(context.Background(), "/stream.bin", strings.NewReader("streamed-payload"), volume.WriteOptions{
		UID:   new(uint32(1001)),
		GID:   new(uint32(1002)),
		Mode:  new(uint32(0o644)),
		Force: new(true),
	})
	if err != nil {
		t.Fatalf("WriteFileStream: %v", err)
	}
	if written.Size != int64(len("streamed-payload")) || written.UID != 1001 {
		t.Fatalf("written = %+v", written)
	}

	strVal, err := v.ReadFileString(context.Background(), "/stream.bin")
	if err != nil || strVal != "chunk-1|chunk-2" {
		t.Fatalf("ReadFileString = %q, err=%v", strVal, err)
	}
	rc, err := v.ReadFileStream(context.Background(), "/stream.bin")
	if err != nil {
		t.Fatalf("ReadFileStream: %v", err)
	}
	streamBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(ReadFileStream): %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close(ReadFileStream): %v", err)
	}
	if string(streamBytes) != "chunk-1|chunk-2" {
		t.Fatalf("streamBytes = %q, want chunk-1|chunk-2", streamBytes)
	}

	_, err = v.ReadFileStream(context.Background(), "/missing-stream")
	if err == nil {
		t.Fatal("expected 404 error from ReadFileStream")
	}
	fnf, ok := errors.AsType[*e2b.FileNotFoundError](err)
	if !ok {
		t.Fatalf("want *e2b.FileNotFoundError, got %v", err)
	}
	if fnf.Path != "/missing-stream" || fnf.ErrorCode() != "NOT_FOUND" || fnf.Message != "stream target missing" {
		t.Fatalf("fnf = %+v (code=%q)", fnf, fnf.ErrorCode())
	}
}

func TestVolumeErrorMappingClassifications(t *testing.T) {
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":       409,
				"error_code": "volume_exists",
				"message":    "volume already exists",
			})
		},
		{http.MethodGet, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    429,
				"message": "slow down",
			})
		},
		{http.MethodGet, "/volumecontent/vol-err/path"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":    "INVALID_JWT",
				"message": "expired volume token",
			})
		},
	})

	cli, err := volume.NewClient(cfg(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = cli.Create(context.Background(), "dup")
	ce, ok := errors.AsType[*e2b.ConflictError](err)
	_, okBase := errors.AsType[e2b.Error](err)
	if !ok || !okBase {
		t.Fatalf("want ConflictError + e2b.Error, got %T: %v", err, err)
	}
	if ce.ErrorCode() != "volume_exists" || ce.HTTPStatus() != http.StatusConflict {
		t.Fatalf("ce = %+v", ce)
	}

	_, err = cli.List(context.Background())
	if _, ok := errors.AsType[*e2b.RateLimitError](err); !ok {
		t.Fatalf("want RateLimitError, got %T: %v", err, err)
	}

	v, err := cli.ConnectWithToken("vol-err", "expired")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	_, err = v.Exists(context.Background(), "/secret")
	ae, ok := errors.AsType[*e2b.AuthenticationError](err)
	if !ok {
		t.Fatalf("want AuthenticationError from Exists, got %T: %v", err, err)
	}
	if ae.ErrorCode() != "INVALID_JWT" || ae.Message != "expired volume token" {
		t.Fatalf("ae = %+v (code=%q)", ae, ae.ErrorCode())
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = v.Stat(cancelledCtx, "/any")
	if _, ok := errors.AsType[*e2b.TimeoutError](err); !ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("want TimeoutError wrapping context.Canceled, got %T: %v", err, err)
	}
}

func TestVolumeContentHeaderIsolationCloudAndBYOC(t *testing.T) {
	var (
		cpHeaders      []http.Header
		contentHeaders []http.Header
		contentHosts   []string
	)

	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/volumes":
				cpHeaders = append(cpHeaders, r.Header.Clone())
				body, _ := json.Marshal(map[string]any{
					"volumeID": "vol-cloud",
					"name":     "cloud-vol",
					"token":    "jwt-cloud",
				})
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil

			case r.Method == http.MethodGet && r.URL.Path == "/volumes/vol-byoc":
				cpHeaders = append(cpHeaders, r.Header.Clone())
				body, _ := json.Marshal(map[string]any{
					"volumeID": "vol-byoc",
					"name":     "byoc-vol",
					"token":    "jwt-byoc",
					"domain":   "us-east.byoc.example.internal",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil

			case strings.HasPrefix(r.URL.Path, "/volumecontent/"):
				contentHeaders = append(contentHeaders, r.Header.Clone())
				contentHosts = append(contentHosts, r.URL.Host)
				body, _ := json.Marshal(map[string]any{
					"name":  "data",
					"type":  "directory",
					"path":  "/",
					"size":  0,
					"mode":  0o755,
					"uid":   0,
					"gid":   0,
					"atime": "2026-01-01T00:00:00Z",
					"mtime": "2026-01-01T00:00:00Z",
					"ctime": "2026-01-01T00:00:00Z",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			return nil, nil
		}),
	}

	cli, err := volume.NewClient(e2b.Config{
		AccessToken: "cp-access-token",
		Domain:      "cloud.e2b.test",
		HTTPClient:  hc,
		Retries:     new(0),
		Headers: map[string]string{
			"X-Control-Custom": "cp-secret-header",
			"X-API-Key":        "cp-custom-api-key",
			"X-Access-Token":   "envd-secret",
			"User-Agent":       "custom-volume-sdk/1.2.3",
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cloudVol, err := cli.Create(context.Background(), "cloud-vol")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := cloudVol.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("cloud Stat: %v", err)
	}

	byocVol, err := cli.Connect(context.Background(), "vol-byoc")
	if err != nil {
		t.Fatalf("Connect(BYOC): %v", err)
	}
	if _, err := byocVol.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("byoc Stat: %v", err)
	}

	if len(cpHeaders) != 2 {
		t.Fatalf("control-plane calls = %d, want 2", len(cpHeaders))
	}
	for i, h := range cpHeaders {
		if got := h.Get("X-Control-Custom"); got != "cp-secret-header" {
			t.Errorf("cp[%d] X-Control-Custom = %q, want cp-secret-header", i, got)
		}
		if got := h.Get("X-API-Key"); got != "cp-custom-api-key" {
			t.Errorf("cp[%d] X-API-Key = %q, want cp-custom-api-key", i, got)
		}
		if got := h.Get("User-Agent"); got != "custom-volume-sdk/1.2.3" {
			t.Errorf("cp[%d] User-Agent = %q, want custom-volume-sdk/1.2.3", i, got)
		}
	}

	if len(contentHeaders) != 2 {
		t.Fatalf("content calls = %d, want 2", len(contentHeaders))
	}
	wantHosts := []string{"api.cloud.e2b.test", "api.us-east.byoc.example.internal"}
	wantAuths := []string{"Bearer jwt-cloud", "Bearer jwt-byoc"}
	for i, h := range contentHeaders {
		if contentHosts[i] != wantHosts[i] {
			t.Errorf("content[%d] host = %q, want %q", i, contentHosts[i], wantHosts[i])
		}
		if got := h.Get("Authorization"); got != wantAuths[i] {
			t.Errorf("content[%d] Authorization = %q, want %q", i, got, wantAuths[i])
		}
		if got := h.Get("User-Agent"); got != "custom-volume-sdk/1.2.3" {
			t.Errorf("content[%d] User-Agent = %q, want custom-volume-sdk/1.2.3", i, got)
		}
		for _, forbidden := range []string{"X-Control-Custom", "X-API-Key", "X-Access-Token"} {
			if got := h.Get(forbidden); got != "" {
				t.Errorf("content[%d] leaked control-plane header %s = %q", i, forbidden, got)
			}
		}
	}

	// Verify default User-Agent is preserved and control-plane headers are still stripped
	// when User-Agent is not explicitly configured.
	contentHeaders = nil
	defaultCli, err := volume.NewClient(e2b.Config{
		APIKey:     "cp-key",
		Domain:     "cloud.e2b.test",
		HTTPClient: hc,
		Retries:    new(0),
		Headers: map[string]string{
			"X-Control-Custom": "must-not-leak",
		},
	})
	if err != nil {
		t.Fatalf("NewClient(default UA): %v", err)
	}
	vDef, err := defaultCli.ConnectWithToken("vol-def", "jwt-def")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	if _, err := vDef.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("default UA Stat: %v", err)
	}
	if len(contentHeaders) != 1 {
		t.Fatalf("default UA content calls = %d, want 1", len(contentHeaders))
	}
	if got := contentHeaders[0].Get("User-Agent"); !strings.HasPrefix(got, "e2b-go-sdk/") {
		t.Errorf("default content User-Agent = %q, want e2b-go-sdk/ prefix", got)
	}
	if got := contentHeaders[0].Get("X-Control-Custom"); got != "" {
		t.Errorf("default content leaked X-Control-Custom = %q", got)
	}

	// Verify case-insensitive user-agent key does not leak other headers.
	contentHeaders = nil
	lowerCli, err := volume.NewClient(e2b.Config{
		APIKey:     "cp-key",
		Domain:     "cloud.e2b.test",
		HTTPClient: hc,
		Retries:    new(0),
		Headers: map[string]string{
			"user-agent":       "lower-ua/1.0",
			"X-Control-Custom": "must-not-leak",
		},
	})
	if err != nil {
		t.Fatalf("NewClient(lower UA): %v", err)
	}
	vLower, err := lowerCli.ConnectWithToken("vol-lower", "jwt-lower")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	if _, err := vLower.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("lower UA Stat: %v", err)
	}
	if len(contentHeaders) != 1 {
		t.Fatalf("lower UA content calls = %d, want 1", len(contentHeaders))
	}
	if got := contentHeaders[0].Get("User-Agent"); got == "" {
		t.Errorf("lower UA content User-Agent is empty")
	}
	if got := contentHeaders[0].Get("X-Control-Custom"); got != "" {
		t.Errorf("lower UA content leaked X-Control-Custom = %q", got)
	}
}

func TestVolumeIDValidation(t *testing.T) {
	var calls atomic.Int32
	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			t.Errorf("unexpected HTTP request for invalid volumeID: %s %s", r.Method, r.URL.String())
			return nil, errors.New("unexpected network call")
		}),
	}
	testCfg := e2b.Config{
		APIKey:     "test-key",
		Domain:     "example.com",
		HTTPClient: hc,
		Retries:    new(0),
	}
	cli, err := volume.NewClient(testCfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	opts := volume.Options{Config: testCfg}
	ctx := context.Background()

	assertInvalid := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
		if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
			t.Fatalf("%s: want *e2b.InvalidArgumentError, got %T: %v", name, err, err)
		}
		if _, ok := errors.AsType[*e2b.VolumeError](err); !ok {
			t.Fatalf("%s: want *e2b.VolumeError, got %T: %v", name, err, err)
		}
	}

	for _, badID := range []string{"", ".", ".."} {
		t.Run("id="+badID, func(t *testing.T) {
			_, err := cli.GetInfo(ctx, badID)
			assertInvalid(t, "Client.GetInfo", err)

			_, err = cli.Connect(ctx, badID)
			assertInvalid(t, "Client.Connect", err)

			_, err = cli.ConnectWithToken(badID, "tok")
			assertInvalid(t, "Client.ConnectWithToken", err)

			ok, err := cli.Destroy(ctx, badID)
			if ok {
				t.Fatalf("Client.Destroy(%q) = true, want false", badID)
			}
			assertInvalid(t, "Client.Destroy", err)

			_, err = volume.GetInfo(ctx, badID, opts)
			assertInvalid(t, "volume.GetInfo", err)

			_, err = volume.Connect(ctx, badID, "tok", opts)
			assertInvalid(t, "volume.Connect", err)

			_, err = volume.ConnectByID(ctx, badID, testCfg)
			assertInvalid(t, "volume.ConnectByID", err)

			_, err = volume.ConnectWithToken(badID, "tok", opts)
			assertInvalid(t, "volume.ConnectWithToken", err)

			ok, err = volume.Destroy(ctx, badID, opts)
			if ok {
				t.Fatalf("volume.Destroy(%q) = true, want false", badID)
			}
			assertInvalid(t, "volume.Destroy", err)

			v, err := cli.ConnectWithToken("valid-vol", "tok")
			if err != nil {
				t.Fatalf("ConnectWithToken(valid): %v", err)
			}
			v.ID = badID
			_, err = v.GetInfo(ctx)
			assertInvalid(t, "Volume.GetInfo", err)
			_, err = v.Destroy(ctx)
			assertInvalid(t, "Volume.Destroy", err)
			err = v.Delete(ctx)
			assertInvalid(t, "Volume.Delete", err)
			_, err = v.Stat(ctx, "/")
			assertInvalid(t, "Volume.Stat(badID)", err)
		})
	}

	if calls.Load() != 0 {
		t.Fatalf("made %d HTTP calls for invalid volumeIDs, want 0", calls.Load())
	}
}

func TestVolumeContentPathAndDepthValidation(t *testing.T) {
	var calls atomic.Int32
	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			switch r.URL.Path {
			case "/volumecontent/vol-1/path":
				if r.URL.Query().Get("path") != "/" {
					t.Errorf("unexpected stat path: %q", r.URL.Query().Get("path"))
				}
				body, _ := json.Marshal(map[string]any{
					"name":  "/",
					"type":  "directory",
					"path":  "/",
					"size":  0,
					"mode":  0o755,
					"uid":   0,
					"gid":   0,
					"atime": "2026-01-01T00:00:00Z",
					"mtime": "2026-01-01T00:00:00Z",
					"ctime": "2026-01-01T00:00:00Z",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			case "/volumecontent/vol-1/dir":
				if r.URL.Query().Get("path") != "/" {
					t.Errorf("unexpected dir path: %q", r.URL.Query().Get("path"))
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader("[]")),
				}, nil
			}
			t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.String())
			return nil, errors.New("unexpected network call")
		}),
	}

	cli, err := volume.NewClient(e2b.Config{
		APIKey:     "test-key",
		Domain:     "example.com",
		HTTPClient: hc,
		Retries:    new(0),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	v, err := cli.ConnectWithToken("vol-1", "tok-1")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}
	ctx := context.Background()

	assertInvalid := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
		if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
			t.Fatalf("%s: want *e2b.InvalidArgumentError, got %T: %v", name, err, err)
		}
		if _, ok := errors.AsType[*e2b.VolumeError](err); !ok {
			t.Fatalf("%s: want *e2b.VolumeError, got %T: %v", name, err, err)
		}
	}

	_, err = v.Stat(ctx, "")
	assertInvalid(t, "Stat", err)
	_, err = v.Exists(ctx, "")
	assertInvalid(t, "Exists", err)
	_, err = v.UpdateMetadata(ctx, "", volume.MetadataOptions{})
	assertInvalid(t, "UpdateMetadata", err)
	_, err = v.List(ctx, "")
	assertInvalid(t, "List", err)
	err = v.MakeDir(ctx, "")
	assertInvalid(t, "MakeDir", err)
	_, err = v.MakeDirWithOptions(ctx, "", volume.MakeDirOptions{})
	assertInvalid(t, "MakeDirWithOptions", err)
	err = v.Remove(ctx, "")
	assertInvalid(t, "Remove", err)
	_, err = v.ReadFile(ctx, "")
	assertInvalid(t, "ReadFile", err)
	_, err = v.ReadFileString(ctx, "")
	assertInvalid(t, "ReadFileString", err)
	if rc, err := v.ReadFileStream(ctx, ""); err == nil {
		_ = rc.Close()
		t.Fatal("ReadFileStream: expected error for empty path")
	} else {
		assertInvalid(t, "ReadFileStream", err)
	}
	err = v.WriteFile(ctx, "", []byte("data"))
	assertInvalid(t, "WriteFile", err)
	_, err = v.WriteFileWithOptions(ctx, "", []byte("data"), volume.WriteOptions{})
	assertInvalid(t, "WriteFileWithOptions", err)
	_, err = v.WriteFileStream(ctx, "", strings.NewReader("data"))
	assertInvalid(t, "WriteFileStream", err)

	// Depth overflow guard (on 64-bit platforms where int can exceed math.MaxUint32).
	if int64(math.MaxInt) > math.MaxUint32 {
		maxU32 := int64(math.MaxUint32)
		_, err = v.List(ctx, "/", volume.ListOptions{Depth: int(maxU32 + 1)})
		assertInvalid(t, "List(Depth > MaxUint32)", err)
	}

	if calls.Load() != 0 {
		t.Fatalf("made %d HTTP calls for invalid path/depth, want 0", calls.Load())
	}

	// Root directory "/" remains valid.
	if _, err := v.Stat(ctx, "/"); err != nil {
		t.Fatalf("Stat(/): %v", err)
	}
	if _, err := v.List(ctx, "/"); err != nil {
		t.Fatalf("List(/): %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("root path calls = %d, want 2", calls.Load())
	}
}

type trackingReadCloser struct {
	r      *bytes.Reader
	closed bool
}

func (tr *trackingReadCloser) Read(p []byte) (int, error) { return tr.r.Read(p) }
func (tr *trackingReadCloser) Close() error {
	tr.closed = true
	return nil
}

func TestVolumeRemoveAndDestroyDrainResponseBody(t *testing.T) {
	bodies := make([]*trackingReadCloser, 0, 2)
	hc := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b := &trackingReadCloser{r: bytes.NewReader([]byte(`{"status":"ok"}`))}
			bodies = append(bodies, b)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       b,
			}, nil
		}),
	}
	cli, err := volume.NewClient(e2b.Config{
		APIKey:     "test-key",
		Domain:     "example.com",
		HTTPClient: hc,
		Retries:    new(0),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	v, err := cli.ConnectWithToken("vol-1", "tok-1")
	if err != nil {
		t.Fatalf("ConnectWithToken: %v", err)
	}

	if err := v.Remove(context.Background(), "/file.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, err := v.Destroy(context.Background()); err != nil || !ok {
		t.Fatalf("Destroy = (%v, %v), want (true, nil)", ok, err)
	}

	if len(bodies) != 2 {
		t.Fatalf("recorded %d response bodies, want 2", len(bodies))
	}
	for i, b := range bodies {
		if !b.closed {
			t.Errorf("body[%d] was not closed", i)
		}
		if b.r.Len() != 0 {
			t.Errorf("body[%d] had %d unread bytes remaining", i, b.r.Len())
		}
	}
}
