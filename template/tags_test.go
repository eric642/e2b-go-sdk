package template

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	be, ok := errors.AsType[*BuildError](err)
	if !ok || be.Op != "delete" {
		t.Fatalf("expected *BuildError{Op: delete}, got %T: %v", err, err)
	}
}

func TestClient_SetPublic(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/templates/tpl_1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"names":["team/demo","demo"]}`))
	}))
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	names, err := cli.SetPublic(context.Background(), "tpl_1", true)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["public"] != true {
		t.Fatalf("body=%v", gotBody)
	}
	if len(names) != 2 || names[0] != "team/demo" {
		t.Fatalf("names=%v", names)
	}
}

func TestClient_FallbackDecodePreservesJSONErrorAndPaginatorCursor(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates/tpl_bad", func(w http.ResponseWriter, _ *http.Request) {
		// Non-200 2xx triggers fallback decode path in SetPublic.
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{not-json`))
	})
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-next-token", "cursor_2")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{broken-json`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL, Retries: new(0)})
	_, err := cli.SetPublic(context.Background(), "tpl_bad", true)
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
	if strings.Contains(err.Error(), "empty response body") {
		t.Fatalf("fallback JSON error must not be masked as empty response body: %v", err)
	}

	p := cli.ListV2(context.Background(), ListV2Options{NextToken: "initial_cursor"})
	if _, err := p.NextItems(context.Background()); err == nil {
		t.Fatal("expected JSON decode error from paginator")
	}
	if p.NextToken() != "initial_cursor" || !p.HasNext() {
		t.Fatalf("paginator cursor must not advance on decode failure: next=%q hasNext=%v", p.NextToken(), p.HasNext())
	}
}

func TestClient_RejectsInvalidPathParamsAndLogDirection(t *testing.T) {
	cli, _ := NewClient(e2b.Config{APIKey: "k", APIURL: "http://unused"})
	ctx := context.Background()

	for _, bad := range []string{"", ".", ".."} {
		if _, err := cli.Exists(ctx, bad); !isInvalidArg(err) {
			t.Errorf("Exists(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.GetTags(ctx, bad); !isInvalidArg(err) {
			t.Errorf("GetTags(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.CountTags(ctx, bad); !isInvalidArg(err) {
			t.Errorf("CountTags(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.TagExists(ctx, bad, "v1"); !isInvalidArg(err) {
			t.Errorf("TagExists(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.ListTagGroups(ctx, bad, TagGroupsOptions{}); !isInvalidArg(err) {
			t.Errorf("ListTagGroups(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.GetTagAssignments(ctx, bad, "v1", TagAssignmentsOptions{}); !isInvalidArg(err) {
			t.Errorf("GetTagAssignments(tpl=%q): got %T %v", bad, err, err)
		}
		if _, err := cli.GetTagAssignments(ctx, "tpl_1", bad, TagAssignmentsOptions{}); !isInvalidArg(err) {
			t.Errorf("GetTagAssignments(tag=%q): got %T %v", bad, err, err)
		}
		if err := cli.Delete(ctx, bad); !isInvalidArg(err) {
			t.Errorf("Delete(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.Destroy(ctx, bad); !isInvalidArg(err) {
			t.Errorf("Destroy(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.Get(ctx, bad, GetOptions{}); !isInvalidArg(err) {
			t.Errorf("Get(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.SetPublic(ctx, bad, true); !isInvalidArg(err) {
			t.Errorf("SetPublic(%q): got %T %v", bad, err, err)
		}
		if err := cli.SetPublicV1(ctx, bad, true); !isInvalidArg(err) {
			t.Errorf("SetPublicV1(%q): got %T %v", bad, err, err)
		}
		if _, err := cli.GetBuildLogs(ctx, bad, "bld_1", BuildLogsOptions{}); !isInvalidArg(err) {
			t.Errorf("GetBuildLogs(tpl=%q): got %T %v", bad, err, err)
		}
		if _, err := cli.GetBuildLogs(ctx, "tpl_1", bad, BuildLogsOptions{}); !isInvalidArg(err) {
			t.Errorf("GetBuildLogs(bld=%q): got %T %v", bad, err, err)
		}
	}

	if _, err := cli.GetBuildLogs(ctx, "tpl_1", "bld_1", BuildLogsOptions{Direction: "asc"}); !isInvalidArg(err) {
		t.Fatalf("GetBuildLogs(Direction=asc): want *InvalidArgumentError, got %T %v", err, err)
	}
}

func isInvalidArg(err error) bool {
	_, ok := errors.AsType[*e2b.InvalidArgumentError](err)
	return ok
}
