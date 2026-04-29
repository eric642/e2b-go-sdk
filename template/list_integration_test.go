//go:build integration

package template

import (
	"testing"
)

func TestIntegrationListAfterBuild(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	tpl := New().FromDebianImage("bookworm").RunCmd("echo list-test").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: uniqueName(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	templates, err := cli.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, tm := range templates {
		if tm.TemplateID == info.TemplateID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("built template %s not present in List result (got %d templates)",
			info.TemplateID, len(templates))
	}
}

func TestIntegrationGetReturnsBuilds(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	tpl := New().FromDebianImage("bookworm").RunCmd("echo get-test").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: uniqueName(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	detail, err := cli.Get(ctx, info.TemplateID, GetOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.TemplateID != info.TemplateID {
		t.Fatalf("TemplateID mismatch: got %q want %q", detail.TemplateID, info.TemplateID)
	}
	if len(detail.Builds) == 0 {
		t.Fatalf("expected at least one build, got 0")
	}
	if detail.Builds[0].BuildID == "" {
		t.Fatalf("first build has empty BuildID: %+v", detail.Builds[0])
	}
}

func TestIntegrationGetBuildLogs(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	tpl := New().FromDebianImage("bookworm").RunCmd("echo logs-test").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: uniqueName(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	logs, err := cli.GetBuildLogs(ctx, info.TemplateID, info.BuildID, BuildLogsOptions{Limit: 50})
	if err != nil {
		t.Fatalf("GetBuildLogs: %v", err)
	}
	var gotMsg bool
	for _, le := range logs {
		if le.Message != "" {
			gotMsg = true
			break
		}
	}
	if !gotMsg {
		t.Fatalf("expected at least one non-empty log message, got %d entries", len(logs))
	}
}
