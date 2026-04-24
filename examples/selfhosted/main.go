// Self-hosted example: point the SDK at a self-hosted E2B control plane and
// show the current sandbox session (ID, template, state, timing, envd info,
// live metrics).
//
// Usage:
//
//	E2B_API_KEY=...            # team API key issued by your self-hosted cluster
//	E2B_DOMAIN=e2b.example.com # your self-hosted domain; APIs resolve to
//	                           # https://api.<domain> and sandboxes to
//	                           # https://49983-<id>.<domain> unless overridden.
//	E2B_API_URL=https://api.e2b.example.com  # optional full override
//	go run ./examples/selfhosted
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

	cfg := e2b.Config{
		APIKey: os.Getenv("E2B_API_KEY"),
		Domain: envOr("E2B_DOMAIN", "e2b.example.com"),
		APIURL: os.Getenv("E2B_API_URL"), // optional; derived from Domain if empty
		Headers: map[string]string{
			"X-Client": "selfhosted-example",
		},
	}
	if cfg.APIKey == "" {
		log.Fatal("E2B_API_KEY is required")
	}

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
		Config:   cfg,
		Template: envOr("E2B_TEMPLATE", "base"),
		Timeout:  5 * time.Minute,
		Metadata: map[string]string{"example": "selfhosted"},
		Secure:   true,
	})
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer func() {
		if err := sbx.Kill(ctx); err != nil {
			log.Printf("kill: %v", err)
		}
	}()

	fmt.Println("=== sandbox session ===")
	fmt.Printf("ID:           %s\n", sbx.ID)
	fmt.Printf("Domain:       %s\n", sbx.Domain)
	fmt.Printf("EnvdVersion:  %s\n", sbx.EnvdVersion)
	fmt.Printf("Host (:8080): %s\n", sbx.GetHost(8080))

	info, err := sbx.GetInfo(ctx)
	if err != nil {
		log.Fatalf("get info: %v", err)
	}
	fmt.Printf("Template:     %s\n", info.TemplateID)
	fmt.Printf("State:        %s\n", info.State)
	fmt.Printf("CPU/Mem/Disk: %d vCPU / %d MB / %d MB\n", info.CPUCount, info.MemoryMB, info.DiskSizeMB)
	fmt.Printf("StartedAt:    %s\n", info.StartedAt.Format(time.RFC3339))
	fmt.Printf("EndAt:        %s\n", info.EndAt.Format(time.RFC3339))
	if len(info.Metadata) > 0 {
		fmt.Printf("Metadata:     %v\n", info.Metadata)
	}

	handle, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", "uname -a && whoami && date"},
		TimeoutMs: 10_000,
	})
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	res, err := handle.Wait(ctx)
	if err != nil {
		log.Fatalf("wait: %v", err)
	}
	fmt.Println("=== exec result ===")
	fmt.Printf("exit=%d\n%s", res.ExitCode, res.Stdout)

	metrics, err := sbx.GetMetrics(ctx)
	if err != nil {
		log.Printf("metrics: %v", err)
	} else if len(metrics) > 0 {
		m := metrics[len(metrics)-1]
		fmt.Println("=== latest metrics ===")
		fmt.Printf("CPU:  %.1f%% of %d vCPU\n", m.CPUUsedPct, m.CPUCount)
		fmt.Printf("Mem:  %d / %d bytes\n", m.MemUsed, m.MemTotal)
		fmt.Printf("Disk: %d / %d bytes\n", m.DiskUsed, m.DiskTotal)
		fmt.Printf("At:   %s\n", m.Timestamp.Format(time.RFC3339))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
