package template

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
)

func skipIfNoAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("E2B_API_KEY") == "" {
		t.Skip("E2B_API_KEY not set; skipping integration test")
	}
}

func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if d, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), d.Add(-2*time.Second))
	}
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

// uniqueName returns a test-name-scoped, time-stamped template name so
// parallel or retried runs do not collide.
func uniqueName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("go-sdk-test-%s-%d",
		strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
		time.Now().UnixNano())
}

// newIntegrationClient returns a configured Client and fails the test on
// construction error.
func newIntegrationClient(t *testing.T) *Client {
	t.Helper()
	skipIfNoAPIKey(t)
	cli, err := NewClient(e2b.Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return cli
}

// registerCleanupDelete schedules a Delete for the given template ID when
// the test ends.
func registerCleanupDelete(t *testing.T, cli *Client, templateID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cli.Delete(ctx, templateID); err != nil {
			t.Logf("cleanup: delete template %s failed: %v", templateID, err)
		}
	})
}

func TestIntegrationBuildSimpleDebian(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	tpl := New().FromDebianImage("bookworm").
		RunCmd("echo ok").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: uniqueName(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{Template: info.TemplateID, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}
	t.Cleanup(func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			t.Logf("sandbox kill: %v", err)
		}
	})
	if sbx.ID == "" {
		t.Fatal("sandbox id empty")
	}
}

func TestIntegrationBuildWithCopyCaches(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/data.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tpl := New().FromDebianImage("bookworm").
		WithContext(dir).
		Copy("data.txt", "/work/").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	name := uniqueName(t)
	info1, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: name})
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	registerCleanupDelete(t, cli, info1.TemplateID)

	// Second build reuses the same source tree -> cache hit path exercised.
	info2, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: name})
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if info2.TemplateID == "" {
		t.Fatal("second build returned empty template id")
	}
}

func TestIntegrationTags(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	tpl := New().FromDebianImage("bookworm").RunCmd("echo tag-test").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))

	base := uniqueName(t)
	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: base + ":v1"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	tagInfo, err := cli.AssignTags(ctx, base+":v1", []string{"stable"})
	if err != nil {
		t.Fatalf("AssignTags: %v", err)
	}
	if tagInfo == nil {
		t.Fatal("AssignTags returned nil TagInfo")
	}
	if len(tagInfo.Tags) == 0 {
		t.Fatal("expected at least one tag returned")
	}

	tags, err := cli.GetTags(ctx, info.TemplateID)
	if err != nil {
		t.Fatalf("GetTags: %v", err)
	}
	seen := map[string]bool{}
	for _, t := range tags {
		seen[t.Tag] = true
	}
	if !seen["stable"] {
		t.Fatalf("expected 'stable' in tags, got %+v", tags)
	}

	if err := cli.RemoveTags(ctx, base, []string{"stable"}); err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
}

func TestIntegrationExistsFalseThenTrue(t *testing.T) {
	cli := newIntegrationClient(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	name := uniqueName(t)
	ok, err := cli.Exists(ctx, name)
	if err != nil {
		t.Fatalf("Exists pre: %v", err)
	}
	if ok {
		t.Fatalf("expected %q not to exist before build", name)
	}

	tpl := New().FromDebianImage("bookworm").RunCmd("echo exists-test").
		SetStartCmd("sleep infinity", WaitForTimeoutMs(1000))
	info, err := cli.Build(ctx, tpl.Builder(), BuildOptions{Name: name})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	registerCleanupDelete(t, cli, info.TemplateID)

	ok, err = cli.Exists(ctx, name)
	if err != nil {
		t.Fatalf("Exists post: %v", err)
	}
	if !ok {
		t.Fatalf("expected %q to exist after build", name)
	}
}
