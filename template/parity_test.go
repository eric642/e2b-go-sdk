package template

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestBuildV3_OmitsZeroResourcesAndSendsExtendedOptions(t *testing.T) {
	var (
		gotCreateBody   map[string]any
		gotCreateIdem   string
		gotTriggerIdem  string
		gotStartPayload map[string]any
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, r *http.Request) {
		gotCreateIdem = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotCreateBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_v3","buildID":"bld_v3","aliases":[],"names":["team/demo"],"public":false,"tags":["v1"]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_v3/builds/bld_v3", func(w http.ResponseWriter, r *http.Request) {
		gotTriggerIdem = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotStartPayload)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/templates/tpl_v3/builds/bld_v3/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_v3","buildID":"bld_v3","status":"ready","logEntries":[{"timestamp":"2026-01-01T00:00:00Z","level":"info","message":"built ok"}],"logs":[]}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	maxCPU := int32(8)
	minDisk := int32(2048)
	var callbackLogs []string
	tpl := New().FromImage("alpine:3").RunCmd("echo ok")

	info, err := cli.Build(context.Background(), tpl, BuildOptions{
		Name:           "demo",
		Tags:           []string{"v1"},
		MaxCPUCount:    &maxCPU,
		MinFreeDiskMB:  &minDisk,
		IdempotencyKey: "idem-key-001",
		PollInterval:   5 * time.Millisecond,
		OnBuildLogs: func(le LogEntry) {
			callbackLogs = append(callbackLogs, le.Message)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.TemplateID != "tpl_v3" || len(info.Tags) != 1 || info.Tags[0] != "v1" {
		t.Fatalf("unexpected BuildInfo: %+v", info)
	}
	if _, hasCPU := gotCreateBody["cpuCount"]; hasCPU {
		t.Fatalf("cpuCount must be omitted when 0: %v", gotCreateBody)
	}
	if _, hasMem := gotCreateBody["memoryMB"]; hasMem {
		t.Fatalf("memoryMB must be omitted when 0: %v", gotCreateBody)
	}
	if gotCreateBody["maxCpuCount"] != float64(8) || gotCreateBody["minFreeDiskMb"] != float64(2048) {
		t.Fatalf("missing v3 resource fields: %v", gotCreateBody)
	}
	if gotCreateIdem != "idem-key-001" || gotTriggerIdem != "idem-key-001" {
		t.Fatalf("Idempotency-Key mismatch: create=%q trigger=%q", gotCreateIdem, gotTriggerIdem)
	}
	if gotStartPayload["fromImage"] != "alpine:3" {
		t.Fatalf("start payload: %v", gotStartPayload)
	}
	if len(callbackLogs) != 1 || callbackLogs[0] != "built ok" {
		t.Fatalf("OnBuildLogs got %v", callbackLogs)
	}
}

func TestBuildV2_410GoneNeverFallsBackToV3(t *testing.T) {
	var v2Calls, v3Calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, _ *http.Request) {
		v2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"code":410,"message":"POST /v2/templates removed; use POST /v3/templates","error_code":"endpoint_gone"}`))
	})
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		v3Calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL, Retries: new(0)})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").RunCmd("echo hi")
	_, err = cli.BuildV2(context.Background(), tpl, BuildOptions{Name: "legacy-demo"})
	if err == nil {
		t.Fatal("expected 410 error from BuildV2")
	}
	if v3Calls.Load() != 0 {
		t.Fatalf("BuildV2 must NEVER fall back to /v3/templates, got %d v3 calls", v3Calls.Load())
	}
	if v2Calls.Load() != 1 {
		t.Fatalf("expected exactly 1 v2 call, got %d", v2Calls.Load())
	}

	if _, ok := errors.AsType[e2b.Error](err); !ok {
		t.Fatalf("BuildV2 error must satisfy e2b.Error: %T", err)
	}
	be, ok := errors.AsType[*BuildError](err)
	if !ok {
		t.Fatalf("expected *BuildError, got %T", err)
	}
	if be.HTTPStatus() != http.StatusGone || be.ErrorCode() != "endpoint_gone" {
		t.Fatalf("status=%d code=%q", be.HTTPStatus(), be.ErrorCode())
	}
	if !strings.Contains(be.Error(), "POST /v2/templates removed") {
		t.Fatalf("expected server message preserved in BuildError: %s", be.Error())
	}
}

func TestBuildV2_HappyPathAndLegacyStringLogsAndReason(t *testing.T) {
	var statusPoll atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, hasCPU := body["cpuCount"]; hasCPU {
			t.Errorf("BuildV2 should omit unset cpuCount: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_v2","buildID":"bld_v2","aliases":["legacy"]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_v2/builds/bld_v2", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/templates/tpl_v2/builds/bld_v2/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if statusPoll.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"templateID":"tpl_v2","buildID":"bld_v2","status":"building","logEntries":[],"logs":["step 1/2","step 2/2"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"templateID":"tpl_v2","buildID":"bld_v2","status":"error","logEntries":[],"logs":[],"reason":"legacy string failure"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var streamedLogs []LogEntry
	tpl := New().FromImage("alpine:3").RunCmd("false")
	_, err = cli.BuildV2(context.Background(), tpl, BuildOptions{
		Name:         "legacy",
		PollInterval: 5 * time.Millisecond,
		OnBuildLogs: func(le LogEntry) {
			streamedLogs = append(streamedLogs, le)
		},
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if len(streamedLogs) != 2 || streamedLogs[0].Message != "step 1/2" || streamedLogs[0].Level != LogLevelInfo || streamedLogs[1].Message != "step 2/2" {
		t.Fatalf("expected legacy logs synthesized as LogEntry, got %+v", streamedLogs)
	}
	if be, ok := errors.AsType[*BuildError](err); !ok || be.Message != "legacy string failure" {
		t.Fatalf("expected legacy string reason mapped to BuildError.Message, got %+v", err)
	}
}

func TestUploadFile_ExactContentLengthAndAzureHeaders(t *testing.T) {
	var (
		gotContentLength int64
		gotTransferEnc   []string
		gotBlobType      string
		gotCustomHdr     string
		gotContentType   string
		gotPayloadLen    int
	)

	var uploadURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_up","buildID":"bld_up","aliases":[],"names":["up"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/templates/tpl_up/files/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"present": false,
			"url":     uploadURL,
			"headers": map[string]string{
				"x-ms-blob-type": "BlockBlob",
				"x-custom-meta":  "abc",
				"Content-Length": "999999",
			},
		})
	})
	mux.HandleFunc("/presigned-put", func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		gotTransferEnc = append([]string(nil), r.TransferEncoding...)
		gotBlobType = r.Header.Get("x-ms-blob-type")
		gotCustomHdr = r.Header.Get("x-custom-meta")
		gotContentType = r.Header.Get("Content-Type")
		if len(r.TransferEncoding) > 0 || r.ContentLength <= 0 {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte("S3 requires exact Content-Length without chunked Transfer-Encoding"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		gotPayloadLen = len(raw)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/templates/tpl_up/builds/bld_up", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	uploadURL = srv.URL + "/presigned-put"

	dir := t.TempDir()
	writeFile(t, dir, "hello.txt", "hello world from e2b go sdk")

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").WithContext(dir).Copy("hello.txt", "/app/")
	if _, err := cli.BuildInBackground(context.Background(), tpl, BuildOptions{Name: "up"}); err != nil {
		t.Fatal(err)
	}
	if len(gotTransferEnc) != 0 {
		t.Fatalf("presigned PUT must not use chunked Transfer-Encoding, got %v", gotTransferEnc)
	}
	if gotContentLength <= 0 || gotContentLength != int64(gotPayloadLen) {
		t.Fatalf("ContentLength=%d payloadLen=%d", gotContentLength, gotPayloadLen)
	}
	if gotBlobType != "BlockBlob" || gotCustomHdr != "abc" {
		t.Fatalf("Azure/custom link headers missing: x-ms-blob-type=%q x-custom-meta=%q", gotBlobType, gotCustomHdr)
	}
	if gotContentType != "application/gzip" {
		t.Fatalf("default Content-Type=%q, want application/gzip", gotContentType)
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: "http://unused"})
	if err != nil {
		t.Fatal(err)
	}
	tpl := New().FromImage("alpine:3").RunCmd("echo ok")

	for _, bad := range []string{
		"UPPERCASE",
		"has_underscore",
		"has space",
		strings.Repeat("a", 256),
	} {
		_, err := cli.BuildInBackground(context.Background(), tpl, BuildOptions{
			Name:           "demo",
			IdempotencyKey: bad,
		})
		if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
			t.Fatalf("key %q: want *InvalidArgumentError, got %T %v", bad, err, err)
		}
	}
}

func TestSkipCacheFromImageInvalidatesWholeTemplate(t *testing.T) {
	b := New().SkipCache().FromImage("alpine:3").RunCmd("echo 1").RunCmd("echo 2")
	body, err := b.serialize(false)
	if err != nil {
		t.Fatal(err)
	}
	if body.Force == nil || !*body.Force {
		t.Fatalf("FromImage after SkipCache should set Force=true on build body: %+v", body.Force)
	}
	for i, s := range *body.Steps {
		if s.Force == nil || !*s.Force {
			t.Fatalf("step %d should have Force=true: %+v", i, s)
		}
	}
}

func TestTemplates_ListV1_ListV2Pagination_ListDefault_And_GetNextToken(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/templates" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"templateID":"tpl_legacy","buildID":"b0","aliases":["legacy-alias"],"names":[],"public":false,"cpuCount":2,"memoryMB":512,"diskSizeMB":1024,"envdVersion":"0.1.0","buildStatus":"ready","buildCount":1,"spawnCount":3,"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-01T00:00:00Z","createdBy":null,"lastSpawnedAt":null}]`))
	})

	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("nextToken")
		w.Header().Set("Content-Type", "application/json")
		if tok == "" {
			w.Header().Set("x-next-token", "cursor_page_2")
			_, _ = w.Write([]byte(`[
				{"templateID":"tpl_1","buildID":"b1","aliases":["alpha"],"names":["team/alpha"],"description":"Alpha template","public":false,"isDefault":false,"cpuCount":2,"maxCpuCount":8,"memoryMB":1024,"diskSizeMB":4096,"envdVersion":"0.5.7","buildStatus":"ready","buildCount":2,"spawnCount":10,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","createdBy":null,"lastSpawnedAt":null},
				{"templateID":"tpl_2","buildID":"b2","aliases":["beta"],"names":["team/beta"],"public":true,"isDefault":false,"cpuCount":4,"memoryMB":2048,"diskSizeMB":8192,"envdVersion":"0.5.7","buildStatus":"ready","buildCount":1,"spawnCount":0,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","createdBy":null,"lastSpawnedAt":null}
			]`))
			return
		}
		if tok == "cursor_page_2" {
			_, _ = w.Write([]byte(`[
				{"templateID":"tpl_3","buildID":"b3","aliases":["gamma"],"names":["team/gamma"],"public":false,"isDefault":false,"cpuCount":2,"memoryMB":1024,"diskSizeMB":4096,"envdVersion":"0.6.0","buildStatus":"ready","buildCount":1,"spawnCount":5,"createdAt":"2026-01-03T00:00:00Z","updatedAt":"2026-01-03T00:00:00Z","createdBy":null,"lastSpawnedAt":null}
			]`))
			return
		}
		t.Errorf("unexpected nextToken %q", tok)
	})

	mux.HandleFunc("/default-templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"templateID":"base","buildID":"b_def","aliases":["base"],"names":["base"],"description":"Default base","public":true,"isDefault":true,"cpuCount":2,"memoryMB":512,"diskSizeMB":2048,"envdVersion":"0.6.0","buildStatus":"ready","buildCount":0,"spawnCount":0,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","createdBy":null,"lastSpawnedAt":null}]`))
	})

	mux.HandleFunc("/templates/tpl_1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-next-token", "builds_cursor_2")
		_, _ = w.Write([]byte(`{
			"templateID":"tpl_1",
			"buildID":"11111111-1111-1111-1111-111111111111",
			"aliases":["alpha"],
			"names":["team/alpha"],
			"description":"Detailed alpha",
			"public":true,
			"isDefault":true,
			"cpuCount":2,
			"maxCpuCount":16,
			"memoryMB":1024,
			"diskSizeMB":4096,
			"envdVersion":"0.6.0",
			"buildStatus":"ready",
			"buildCount":3,
			"spawnCount":42,
			"createdAt":"2026-01-01T00:00:00Z",
			"updatedAt":"2026-01-02T00:00:00Z",
			"lastSpawnedAt":null,
			"createdBy":null,
			"builds":[
				{
					"buildID":"11111111-1111-1111-1111-111111111111",
					"status":"ready",
					"cpuCount":2,
					"maxCpuCount":16,
					"memoryMB":1024,
					"diskSizeMB":4096,
					"envdVersion":"0.6.0",
					"createdAt":"2026-01-01T00:00:00Z",
					"updatedAt":"2026-01-02T00:00:00Z"
				}
			]
		}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v1List, err := cli.ListV1(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v1List) != 1 || len(v1List[0].Names) != 1 || v1List[0].Names[0] != "legacy-alias" {
		t.Fatalf("ListV1 legacy alias fallback failed: %+v", v1List)
	}

	all, err := cli.ListAllV2(ctx, ListV2Options{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAllV2 len=%d, want 3", len(all))
	}
	if all[0].Description != "Alpha template" || all[0].MaxCPUCount == nil || *all[0].MaxCPUCount != 8 {
		t.Fatalf("enriched Template fields missing: %+v", all[0])
	}

	filtered, err := cli.ListAllV2(ctx, ListV2Options{Names: []string{"gamma"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].TemplateID != "tpl_3" {
		t.Fatalf("ListAllV2 Names filter got %+v", filtered)
	}

	defaults, err := cli.ListDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 1 || !defaults[0].IsDefault || defaults[0].TemplateID != "base" {
		t.Fatalf("ListDefault got %+v", defaults)
	}

	detail, err := cli.Get(ctx, "tpl_1", GetOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if detail.NextToken != "builds_cursor_2" {
		t.Fatalf("detail.NextToken=%q, want builds_cursor_2", detail.NextToken)
	}
	if !detail.IsDefault || detail.Description != "Detailed alpha" || detail.MaxCPUCount == nil || *detail.MaxCPUCount != 16 || detail.DiskSizeMB != 4096 {
		t.Fatalf("enriched TemplateDetail fields missing: %+v", detail)
	}
	if len(detail.Builds) != 1 || detail.Builds[0].MaxCPUCount == nil || *detail.Builds[0].MaxCPUCount != 16 {
		t.Fatalf("enriched TemplateBuild fields missing: %+v", detail.Builds)
	}
}

func TestTagQueryEndpointsAndAssignIdempotency(t *testing.T) {
	var gotAssignIdem string
	mux := http.NewServeMux()

	mux.HandleFunc("/templates/tags", func(w http.ResponseWriter, r *http.Request) {
		gotAssignIdem = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"buildID":"11111111-1111-1111-1111-111111111111","tags":["v1"]}`))
	})
	mux.HandleFunc("/templates/tpl_1/tags/count", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":7}`))
	})
	mux.HandleFunc("/templates/tpl_1/tags/exists", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("tag"); got != "prod" {
			t.Errorf("tag query=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"exists":true,"normalizedTag":"prod"}`))
	})
	mux.HandleFunc("/templates/tpl_1/tags/groups", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sort"); got != string(TagGroupsSortLatestDesc) {
			t.Errorf("sort=%q", got)
		}
		if got := r.URL.Query().Get("search"); got != "pr" {
			t.Errorf("search=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"nextCursor":"groups_next",
			"tags":[
				{
					"tag":"prod",
					"hasMore":true,
					"assignments":[
						{
							"assignmentId":"22222222-2222-2222-2222-222222222222",
							"buildId":"11111111-1111-1111-1111-111111111111",
							"assignedAt":"2026-02-01T00:00:00Z",
							"buildCreatedAt":"2026-01-31T00:00:00Z",
							"buildFinishedAt":"2026-01-31T00:05:00Z"
						}
					]
				}
			]
		}`))
	})
	mux.HandleFunc("/templates/tpl_1/tags/prod/assignments", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("limit=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"nextCursor":"assign_next",
			"data":[
				{
					"assignmentId":"22222222-2222-2222-2222-222222222222",
					"buildId":"11111111-1111-1111-1111-111111111111",
					"assignedAt":"2026-02-01T00:00:00Z",
					"buildCreatedAt":"2026-01-31T00:00:00Z",
					"buildFinishedAt":null
				}
			]
		}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tagInfo, err := cli.AssignTagsWithOptions(ctx, "tpl_1:v1", []string{"v1"}, AssignTagsOptions{
		IdempotencyKey: "tag-idem-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAssignIdem != "tag-idem-1" || tagInfo.BuildID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("AssignTagsWithOptions: idem=%q info=%+v", gotAssignIdem, tagInfo)
	}

	total, err := cli.CountTags(ctx, "tpl_1")
	if err != nil || total != 7 {
		t.Fatalf("CountTags: total=%d err=%v", total, err)
	}

	existsRes, err := cli.TagExists(ctx, "tpl_1", "prod")
	if err != nil || !existsRes.Exists || existsRes.NormalizedTag != "prod" {
		t.Fatalf("TagExists: %+v err=%v", existsRes, err)
	}

	groups, err := cli.ListTagGroups(ctx, "tpl_1", TagGroupsOptions{
		AssignmentLimit: 3,
		TagsLimit:       10,
		Search:          "pr",
		Sort:            TagGroupsSortLatestDesc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if groups.NextCursor != "groups_next" || len(groups.Tags) != 1 || groups.Tags[0].Tag != "prod" || !groups.Tags[0].HasMore || len(groups.Tags[0].Assignments) != 1 {
		t.Fatalf("ListTagGroups: %+v", groups)
	}

	assignments, err := cli.GetTagAssignments(ctx, "tpl_1", "prod", TagAssignmentsOptions{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if assignments.NextCursor != "assign_next" || len(assignments.Data) != 1 || assignments.Data[0].AssignmentID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("GetTagAssignments: %+v", assignments)
	}
}

func TestBuildStream_CancelWithoutDrainingDoesNotLeakPollGoroutine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_leak","buildID":"bld_leak","aliases":[],"names":["leak"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_leak/builds/bld_leak", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/templates/tpl_leak/builds/bld_leak/status", func(w http.ResponseWriter, _ *http.Request) {
		entries := make([]map[string]any, 32)
		for i := range entries {
			entries[i] = map[string]any{
				"timestamp": "2026-01-01T00:00:00Z",
				"level":     "info",
				"message":   "log line",
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"templateID": "tpl_leak",
			"buildID":    "bld_leak",
			"status":     "building",
			"logEntries": entries,
			"logs":       []string{},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	ch, err := cli.BuildStream(streamCtx, New().FromImage("alpine:3").RunCmd("echo hi"), BuildOptions{
		Name:         "leak",
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		streamCancel()
		t.Fatal(err)
	}
	if _, ok := <-ch; !ok {
		streamCancel()
		t.Fatal("expected first log event before cancel")
	}
	streamCancel()

	pollCtx, pollCancel := context.WithCancel(context.Background())
	out := make(chan BuildEvent, buildEventBuffer)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		cli.pollUntilDone(pollCtx, &BuildInfo{TemplateID: "tpl_leak", BuildID: "bld_leak"}, 5*time.Millisecond, nil, out)
	}()
	if _, ok := <-out; !ok {
		pollCancel()
		t.Fatal("expected first event from pollUntilDone")
	}
	pollCancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("pollUntilDone leaked/blocked after ctx cancel with full event buffer")
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range ch {
		}
	}()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("BuildStream channel did not close after ctx cancel")
	}
}

func TestUploadFile_HonorsRequestTimeoutDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var sawDefaultDeadline, sawDisabledDeadline atomic.Bool

	defaultRT := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Context().Deadline(); ok {
			sawDefaultDeadline.Store(true)
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	cliDefault, err := NewClient(e2b.Config{
		APIKey:     "k",
		APIURL:     srv.URL,
		HTTPClient: &http.Client{Transport: defaultRT},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cliDefault.uploadFile(context.Background(), srv.URL, strings.NewReader("gz")); err != nil {
		t.Fatal(err)
	}
	if !sawDefaultDeadline.Load() {
		t.Fatal("expected 1h context deadline when RequestTimeoutDisabled is false")
	}

	disabledRT := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Context().Deadline(); ok {
			sawDisabledDeadline.Store(true)
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	cliDisabled, err := NewClient(e2b.Config{
		APIKey:                 "k",
		APIURL:                 srv.URL,
		RequestTimeoutDisabled: true,
		HTTPClient:             &http.Client{Transport: disabledRT},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cliDisabled.uploadFile(context.Background(), srv.URL, strings.NewReader("gz")); err != nil {
		t.Fatal(err)
	}
	if sawDisabledDeadline.Load() {
		t.Fatal("expected no context deadline when RequestTimeoutDisabled is true")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFinalBuilderConvenience_Destroy_NewReadyCmd_AndLogStatusFilters(t *testing.T) {
	var (
		gotStatusQuery string
		gotLogsQuery   string
		deleteCalls    atomic.Int32
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_fb","buildID":"bld_fb","aliases":[],"names":["fb"],"public":false,"tags":[]}`))
	})
	mux.HandleFunc("/v2/templates/tpl_fb/builds/bld_fb", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/templates/tpl_fb/builds/bld_fb/status", func(w http.ResponseWriter, r *http.Request) {
		gotStatusQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_fb","buildID":"bld_fb","status":"ready","logEntries":[],"logs":[]}`))
	})
	mux.HandleFunc("/templates/tpl_fb/builds/bld_fb/logs", func(w http.ResponseWriter, r *http.Request) {
		gotLogsQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"logs":[{"timestamp":"2026-01-01T00:00:00Z","level":"warn","message":"warn line"}]}`))
	})
	mux.HandleFunc("/templates/tpl_fb", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if deleteCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := NewClient(e2b.Config{APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	final := New().
		FromAlpineImage("").
		RunCmd("echo hi").
		SetStartCmd("/app/server", NewReadyCmd("test -S /tmp/app.sock")).
		SetTag("fb:v1")

	if final.BaseImage() != "alpine:3.24" || final.StartCmd() != "/app/server" ||
		final.ReadyCmdString() != "test -S /tmp/app.sock" || final.Tag() != "fb:v1" {
		t.Fatalf("FinalBuilder accessors mismatch: base=%q start=%q ready=%q tag=%q",
			final.BaseImage(), final.StartCmd(), final.ReadyCmdString(), final.Tag())
	}

	info, err := final.Build(ctx, cli, BuildOptions{Name: "fb", PollInterval: 5 * time.Millisecond})
	if err != nil || info.TemplateID != "tpl_fb" {
		t.Fatalf("FinalBuilder.Build: info=%+v err=%v", info, err)
	}

	if _, err := cli.GetBuildStatusWithOptions(ctx, *info, BuildStatusOptions{
		LogsOffset: 5,
		Limit:      25,
		Level:      LogLevelWarn,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotStatusQuery, "logsOffset=5") || !strings.Contains(gotStatusQuery, "limit=25") || !strings.Contains(gotStatusQuery, "level=warn") {
		t.Fatalf("GetBuildStatusWithOptions query=%q", gotStatusQuery)
	}

	logs, err := cli.GetBuildLogs(ctx, "tpl_fb", "bld_fb", BuildLogsOptions{
		Limit:  10,
		Level:  LogLevelWarn,
		Source: "persistent",
	})
	if err != nil || len(logs) != 1 {
		t.Fatalf("GetBuildLogs: %+v err=%v", logs, err)
	}
	if !strings.Contains(gotLogsQuery, "source=persistent") {
		t.Fatalf("GetBuildLogs missing source query: %q", gotLogsQuery)
	}

	deleted, err := cli.Destroy(ctx, "tpl_fb")
	if err != nil || !deleted {
		t.Fatalf("first Destroy: deleted=%v err=%v", deleted, err)
	}
	deletedAgain, err := cli.Destroy(ctx, "tpl_fb")
	if err != nil || deletedAgain {
		t.Fatalf("second Destroy (404): deleted=%v err=%v", deletedAgain, err)
	}
}

func TestDualCredentials_LegacyAuthSplitNeverSendsBothHeaders(t *testing.T) {
	var uploadURL string
	assertSingleAuthHeader := func(r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		bearer := r.Header.Get("Authorization")
		if apiKey != "" && bearer != "" {
			t.Errorf("%s %s sent BOTH X-API-Key and Authorization headers", r.Method, r.URL.Path)
		}
	}
	requireAPIKey := func(w http.ResponseWriter, r *http.Request) bool {
		assertSingleAuthHeader(r)
		if r.Header.Get("X-API-Key") != "team-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"X-API-Key header is missing"}`))
			return false
		}
		return true
	}
	requireAccessToken := func(w http.ResponseWriter, r *http.Request) bool {
		assertSingleAuthHeader(r)
		if r.Header.Get("Authorization") != "Bearer user-token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"authorization header is missing"}`))
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/templates" {
			http.NotFound(w, r)
			return
		}
		if !requireAccessToken(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"templateID":"tpl_sh","buildID":"bld_sh","aliases":["sh-demo"],"public":false,"cpuCount":2,"memoryMB":512,"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-01T00:00:00Z","createdBy":null,"lastSpawnedAt":null,"spawnCount":0,"buildCount":1}]`))
	})
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		if !requireAPIKey(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"templateID":"tpl_sh","buildID":"bld_sh","aliases":["sh-demo"]}`))
	})
	mux.HandleFunc("/templates/tpl_sh/files/", func(w http.ResponseWriter, r *http.Request) {
		if !requireAccessToken(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"present": false, "url": uploadURL})
	})
	mux.HandleFunc("/upload-sh", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/templates/tpl_sh/builds/bld_sh", func(w http.ResponseWriter, r *http.Request) {
		if !requireAPIKey(w, r) {
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/templates/tpl_sh/builds/bld_sh/status", func(w http.ResponseWriter, r *http.Request) {
		if !requireAccessToken(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"templateID":"tpl_sh","buildID":"bld_sh","status":"ready","logEntries":[],"logs":["built ok"]}`))
	})
	mux.HandleFunc("/templates/tpl_sh", func(w http.ResponseWriter, r *http.Request) {
		if !requireAccessToken(w, r) {
			return
		}
		switch r.Method {
		case http.MethodPatch, http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	uploadURL = srv.URL + "/upload-sh"

	cli, err := NewClient(e2b.Config{
		APIKey:      "team-key",
		AccessToken: "user-token",
		APIURL:      srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	list, err := cli.ListV1(ctx, ListOptions{})
	if err != nil || len(list) != 1 || list[0].TemplateID != "tpl_sh" {
		t.Fatalf("ListV1: list=%+v err=%v", list, err)
	}

	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "data")
	tpl := New().FromBaseImage().WithContext(dir).Copy("f.txt", "/tmp/f.txt")
	info, err := cli.BuildV2(ctx, tpl, BuildOptions{Name: "sh-demo", PollInterval: 5 * time.Millisecond})
	if err != nil || info.TemplateID != "tpl_sh" {
		t.Fatalf("BuildV2: info=%+v err=%v", info, err)
	}

	if err := cli.SetPublicV1(ctx, "tpl_sh", true); err != nil {
		t.Fatalf("SetPublicV1: %v", err)
	}
	deleted, err := cli.Destroy(ctx, "tpl_sh")
	if err != nil || !deleted {
		t.Fatalf("Destroy: deleted=%v err=%v", deleted, err)
	}
}
