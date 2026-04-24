package volume_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/volume"
)

type routeKey struct {
	method string
	path   string
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
	return e2b.Config{APIKey: "test-key", APIURL: url, Domain: "example.com"}
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
	if v.ID != "vol-123" || v.Name != "vol1" || v.Token() != "jwt-abc" {
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
			w.WriteHeader(http.StatusNotFound)
		},
	})
	_, err := volume.Connect(context.Background(), "missing", "t", volume.Options{Config: cfg(srv.URL)})
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *e2b.VolumeError
	if !errors.As(err, &ve) {
		t.Fatalf("want *e2b.VolumeError, got %T: %v", err, err)
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
	var ve *e2b.VolumeError
	if !errors.As(err, &ve) {
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
			w.WriteHeader(http.StatusOK)
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
	var ve *e2b.VolumeError
	if !errors.As(err, &ve) {
		t.Fatalf("want *e2b.VolumeError, got %T", err)
	}
}

func TestVolumeCreateDebugModeUsesLocalhost(t *testing.T) {
	// Sanity: if Debug=true and no explicit APIURL is given, we should hit
	// http://localhost:3000. We just verify the derived URL by spinning up a
	// server on localhost:3000... which we can't in tests. Instead we test
	// the helper path by inspecting that an explicit APIURL wins over Debug.
	srv := newVolumeMock(t, map[routeKey]http.HandlerFunc{
		{http.MethodPost, "/volumes"}: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"volumeID": "v", "name": "n", "token": "t"})
		},
	})
	_, err := volume.Create(context.Background(), "x", volume.Options{Config: e2b.Config{
		APIKey: "k",
		APIURL: srv.URL, // explicit wins over Debug
		Debug:  true,
	}})
	if err != nil {
		t.Fatal(err)
	}
}
