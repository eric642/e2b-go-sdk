package e2b

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestListSnapshotsPaginatesViaNextToken(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuthHeader(t, r, "test-key")
		if r.URL.Path != "/snapshots" {
			t.Errorf("path: %s", r.URL.Path)
		}
		switch r.URL.Query().Get("nextToken") {
		case "":
			w.Header().Set("x-next-token", "tok-2")
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "snap-1:default", "names": []string{"team/snap-1:default"}},
			})
		case "tok-2":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"snapshotID": "snap-2:default", "names": []string{}},
			})
		default:
			t.Errorf("unexpected nextToken: %q", r.URL.Query().Get("nextToken"))
		}
	}))

	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	all, err := c.ListAllSnapshots(context.Background(), SnapshotListOptions{})
	if err != nil {
		t.Fatalf("ListAllSnapshots: %v", err)
	}
	if len(all) != 2 || all[0].SnapshotID != "snap-1:default" || all[1].SnapshotID != "snap-2:default" {
		t.Fatalf("snapshots: %+v", all)
	}
	if len(all[0].Names) != 1 || all[0].Names[0] != "team/snap-1:default" {
		t.Fatalf("names: %+v", all[0].Names)
	}
}

func TestListSnapshotsSendsSandboxIDFilter(t *testing.T) {
	mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sandboxID"); got != "sbx-42" {
			t.Errorf("sandboxID=%q want sbx-42", got)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Errorf("limit=%q want 10", got)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	c, err := NewClient(mock.Config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.ListAllSnapshots(context.Background(), SnapshotListOptions{SandboxID: "sbx-42", Limit: 10}); err != nil {
		t.Fatalf("ListAllSnapshots: %v", err)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool
		errNil bool
	}{
		{"ok", http.StatusOK, true, true},
		{"gone", http.StatusNotFound, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newRESTMock(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("method=%s want DELETE", r.Method)
				}
				if !strings.HasPrefix(r.URL.Path, "/templates/") {
					t.Errorf("path=%s want /templates/{id}", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			c, err := NewClient(mock.Config)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ok, err := c.DeleteSnapshot(context.Background(), "snap-1:default")
			if (err == nil) != tc.errNil {
				t.Fatalf("err=%v", err)
			}
			if ok != tc.want {
				t.Fatalf("ok=%v want %v", ok, tc.want)
			}
		})
	}
}
