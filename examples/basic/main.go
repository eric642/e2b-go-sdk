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
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := e2b.NewClient(e2b.Config{})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}

	sbx, err := client.Create(ctx, e2b.CreateOptions{
		Template: envOr("E2B_TEMPLATE", "base"),
		Timeout:  5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			log.Printf("kill: %v", err)
		}
	}()

	fmt.Printf("sandbox id: %s\n", sbx.ID)

	handle, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", "echo hello from e2b && date"},
		TimeoutMs: 10_000,
	})
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	res, err := handle.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	fmt.Printf("exit=%d stdout=%s", res.ExitCode, res.Stdout)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
