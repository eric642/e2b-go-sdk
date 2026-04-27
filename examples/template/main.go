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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tpl := template.New().
		FromDebianImage("bookworm").
		RunCmd("apt-get update && apt-get install -y curl").
		SetStartCmd("sleep infinity", template.WaitForTimeoutMs(1000))

	cli, err := template.NewClient(e2b.Config{})
	if err != nil {
		log.Fatalf("template client: %v", err)
	}

	events, err := cli.BuildStream(ctx, tpl.Builder(), template.BuildOptions{
		Name: "go-sdk-demo:latest",
	})
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
	fmt.Printf("template built: %s\n", info.TemplateID)

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{Template: info.TemplateID})
	if err != nil {
		log.Fatalf("create sandbox: %v", err)
	}
	defer func() {
		if err := sbx.Kill(ctx); err != nil {
			log.Printf("sandbox kill: %v", err)
		}
	}()
	fmt.Printf("sandbox id: %s\n", sbx.ID)
}
