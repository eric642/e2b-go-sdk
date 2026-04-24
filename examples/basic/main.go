// Basic example: create a sandbox, run a command, read its output.
//
// Usage:
//
//	source ./.env && go run ./examples/basic
//	# or: bash examples/basic/run.sh
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/eric642/e2b-go-sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
		Template: envOr("E2B_TEMPLATE", "base"),
		Timeout:  5 * time.Minute,
	})
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer func() {
		if err := sbx.Kill(ctx); err != nil {
			log.Printf("kill: %v", err)
		}
	}()

	fmt.Printf("sandbox id: %s\n", sbx.ID)

	handle, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", "echo hello from e2b && date"},
		TimeoutMs: 10_000,
	})
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	res, err := handle.Wait(ctx)
	if err != nil {
		log.Fatalf("wait: %v", err)
	}
	fmt.Printf("exit=%d stdout=%s", res.ExitCode, res.Stdout)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
