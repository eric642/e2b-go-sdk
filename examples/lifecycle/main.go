// Lifecycle example: list templates, build a new one, inspect it, spawn a
// sandbox from it, read sandbox info, then tear everything down.
//
// Usage:
//
//	source ./.env && go run ./examples/lifecycle
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/template"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cli, err := template.NewClient(e2b.Config{})
	if err != nil {
		log.Fatalf("template client: %v", err)
	}

	// 1) List existing templates.
	existing, err := cli.List(ctx, template.ListOptions{})
	if err != nil {
		log.Fatalf("list templates: %v", err)
	}
	fmt.Printf("existing templates: %d\n", len(existing))
	for i, t := range existing {
		if i >= 5 {
			fmt.Printf("... and %d more\n", len(existing)-5)
			break
		}
		fmt.Printf("  - %s  id=%s  status=%s  created=%s\n",
			firstOr(t.Names, "(unnamed)"),
			t.TemplateID,
			t.BuildStatus,
			t.CreatedAt.Format(time.RFC3339))
	}

	// 2) Build a new template.
	name := fmt.Sprintf("go-sdk-lifecycle-demo-%d", time.Now().Unix())
	tpl := template.New().
		FromDebianImage("bookworm").
		RunCmd("echo built-by-lifecycle-example").
		SetStartCmd("sleep infinity", template.WaitForTimeoutMs(1000))

	events, err := cli.BuildStream(ctx, tpl.Builder(), template.BuildOptions{Name: name})
	if err != nil {
		log.Fatalf("build start: %v", err)
	}
	var info *template.BuildInfo
	for ev := range events {
		switch {
		case ev.Log != nil:
			fmt.Printf("[%s] %s\n", ev.Log.Level, ev.Log.Message)
		case ev.Err != nil:
			log.Fatalf("build: %v", ev.Err)
		case ev.Done != nil:
			info = ev.Done
		}
	}
	if info == nil {
		log.Fatal("build finished without a BuildInfo")
	}
	fmt.Printf("\ntemplate built: id=%s build=%s\n", info.TemplateID, info.BuildID)

	// Make sure the template is removed even if later steps fail.
	defer func() {
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer deleteCancel()
		if err := cli.Delete(deleteCtx, info.TemplateID); err != nil {
			log.Printf("delete template: %v", err)
			return
		}
		fmt.Printf("template deleted: %s\n", info.TemplateID)
	}()

	// 3) Inspect the freshly built template.
	detail, err := cli.Get(ctx, info.TemplateID, template.GetOptions{Limit: 5})
	if err != nil {
		log.Fatalf("get template: %v", err)
	}
	fmt.Printf("template detail: names=%v public=%v builds=%d\n",
		detail.Names, detail.Public, len(detail.Builds))
	for _, b := range detail.Builds {
		fmt.Printf("  build %s status=%s cpu=%d memMB=%d created=%s\n",
			b.BuildID, b.Status, b.CPUCount, b.MemoryMB, b.CreatedAt.Format(time.RFC3339))
	}

	// 4) Start a sandbox from the template.
	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
		Template: info.TemplateID,
		Timeout:  5 * time.Minute,
	})
	if err != nil {
		log.Fatalf("create sandbox: %v", err)
	}
	// Kill runs before the template-delete defer thanks to LIFO ordering.
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			log.Printf("sandbox kill: %v", err)
			return
		}
		fmt.Printf("sandbox killed: %s\n", sbx.ID)
	}()
	fmt.Printf("sandbox started: id=%s\n", sbx.ID)

	// 5) Read sandbox info.
	si, err := sbx.GetInfo(ctx)
	if err != nil {
		log.Fatalf("sandbox info: %v", err)
	}
	fmt.Printf("sandbox info: state=%s cpu=%d memMB=%d template=%s started=%s endAt=%s\n",
		si.State, si.CPUCount, si.MemoryMB, si.TemplateID,
		si.StartedAt.Format(time.RFC3339), si.EndAt.Format(time.RFC3339))
}

func firstOr(names []string, fallback string) string {
	if len(names) == 0 {
		return fallback
	}
	return names[0]
}
