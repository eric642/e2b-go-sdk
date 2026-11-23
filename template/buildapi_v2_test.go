package template

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestBuildInBackgroundV2_PayloadAndHeaders(t *testing.T) {
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "k" {
			t.Errorf("X-API-Key=%q", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type=%q", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_1","buildID":"bld_1","aliases":["my-tpl"],"public":false}`))
	})
	mux.HandleFunc("/v2/templates/tpl_1/builds/bld_1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").RunCmd("echo ok")
	out, err := c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{
		Name:     "my-tpl",
		CPUCount: 2,
		MemoryMB: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.TemplateID != "tpl_1" || out.BuildID != "bld_1" {
		t.Fatalf("got %+v", out)
	}
	if gotBody["alias"] != "my-tpl" || gotBody["cpuCount"] != float64(2) || gotBody["memoryMB"] != float64(1024) {
		t.Fatalf("body=%v", gotBody)
	}
	if _, ok := gotBody["tags"]; ok {
		t.Fatal("v2 body must not carry tags")
	}
}

func TestBuildInBackgroundV2_OmitsZeroResources(t *testing.T) {
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl","buildID":"bld"}`))
	})
	mux.HandleFunc("/v2/templates/tpl/builds/bld", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").RunCmd("echo ok")
	if _, err := c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["cpuCount"]; ok {
		t.Fatalf("cpuCount should be omitted: %v", gotBody)
	}
	if _, ok := gotBody["memoryMB"]; ok {
		t.Fatalf("memoryMB should be omitted: %v", gotBody)
	}
}

func TestBuildInBackgroundV2_EmptyResponseAndTagsRejection(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL, Retries: new(0)})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").RunCmd("echo ok")

	_, err = c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{
		Name: "a",
		Tags: []string{"v1"},
	})
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("tags: want *e2b.InvalidArgumentError, got %T %v", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("tags rejection must fail before HTTP request, got %d calls", calls.Load())
	}

	maxCPU := int32(4)
	_, err = c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{
		Name:        "a",
		MaxCPUCount: &maxCPU,
	})
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("MaxCPUCount: want *e2b.InvalidArgumentError, got %T %v", err, err)
	}

	minDisk := int32(512)
	_, err = c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{
		Name:          "a",
		MinFreeDiskMB: &minDisk,
	})
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("MinFreeDiskMB: want *e2b.InvalidArgumentError, got %T %v", err, err)
	}

	_, err = c.BuildInBackgroundV2(context.Background(), tpl, BuildOptions{Name: "a"})
	if be, ok := errors.AsType[*BuildError](err); !ok || be.Op != "request" {
		t.Fatalf("empty 202 body: want *BuildError{Op:request}, got %T %v", err, err)
	}
}
