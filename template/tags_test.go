package template

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestClient_Exists_True(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/templates/aliases/") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"templateID": "tpl_1"})
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	ok, err := cli.Exists(context.Background(), "my-alias")
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
}

func TestClient_Exists_False(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	ok, err := cli.Exists(context.Background(), "missing")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("expected false")
	}
}

func TestClient_Exists_ForbiddenMeansExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	ok, err := cli.Exists(context.Background(), "private")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("403 should mean the alias exists")
	}
}

func TestClient_AssignTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/templates/tags" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"buildID":"11111111-1111-1111-1111-111111111111","tags":["prod","stable"]}`))
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	info, err := cli.AssignTags(context.Background(), "tpl:v1", []string{"prod", "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || len(info.Tags) != 2 || info.BuildID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("info: %+v", info)
	}
}

func TestClient_RemoveTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/templates/tags" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err := cli.RemoveTags(context.Background(), "tpl", []string{"stable"}); err != nil {
		t.Fatal(err)
	}
}

func TestClient_GetTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/tags") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag":"prod","buildID":"11111111-1111-1111-1111-111111111111","createdAt":"2026-04-24T00:00:00Z"}]`))
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	tags, err := cli.GetTags(context.Background(), "tpl_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Tag != "prod" || tags[0].BuildID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("tags: %+v", tags)
	}
}

func TestClient_Delete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err := cli.Delete(context.Background(), "tpl_1"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_Delete_ErrorOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	err := cli.Delete(context.Background(), "tpl_1")
	if err == nil {
		t.Fatal("expected error")
	}
	var be *BuildError
	if !asBuildErr(err, &be) || be.Op != "delete" {
		t.Fatalf("expected *BuildError{Op: delete}, got %T: %v", err, err)
	}
}

func asBuildErr(err error, dst **BuildError) bool {
	be, ok := err.(*BuildError)
	if ok {
		*dst = be
	}
	return ok
}
