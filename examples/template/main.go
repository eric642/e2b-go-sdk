// Template example: build a simple Debian-based template and launch a
// sandbox from it.
//
// Usage:
//
//	source ./.env && go run ./examples/template
//	# or: bash examples/template/run.sh
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
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tpl := template.New().
		FromDebianImage("bookworm").
		RunCmd("apt-get update && apt-get install -y curl").
		SetStartCmd("sleep infinity", template.WaitForTimeoutMs(1000))

	cli, err := template.NewClient(e2b.Config{})
	if err != nil {
		return fmt.Errorf("template client: %w", err)
	}

	events, err := cli.BuildStream(ctx, tpl.Builder(), template.BuildOptions{
		Name: "go-sdk-demo:latest",
	})
	if err != nil {
		return fmt.Errorf("build start: %w", err)
	}

	var info *template.BuildInfo
	for ev := range events {
		switch {
		case ev.Log != nil:
			fmt.Printf("[%s] %s\n", ev.Log.Level, ev.Log.Message)
		case ev.Err != nil:
			return fmt.Errorf("build: %w", ev.Err)
		case ev.Done != nil:
			info = ev.Done
		}
	}
	if info == nil {
		return fmt.Errorf("build finished without a BuildInfo")
	}
	fmt.Printf("template built: %s\n", info.TemplateID)

	client, err := e2b.NewClient(e2b.Config{})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	sbx, err := client.Create(ctx, e2b.CreateOptions{Template: info.TemplateID})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			log.Printf("sandbox kill: %v", err)
		}
	}()
	fmt.Printf("sandbox id: %s\n", sbx.ID)
	return nil
}
