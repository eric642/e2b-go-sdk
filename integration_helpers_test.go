//go:build integration

package e2b

import (
	"context"
	"os"
	"testing"
	"time"
)

// skipIfNoAPIKey skips the current test when E2B_API_KEY is not set in the
// environment. Used to gate integration tests that need a live sandbox.
func skipIfNoAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("E2B_API_KEY") == "" {
		t.Skip("E2B_API_KEY not set; skipping integration test")
	}
}

// integrationContext returns a context that honours t.Deadline() and defaults
// to 2 minutes otherwise.
func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if d, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), d.Add(-2*time.Second))
	}
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

// newIntegrationSandbox creates a live sandbox using E2B_API_KEY and registers
// a cleanup that kills it. It panics via t.Fatalf on failure, so callers can
// treat the returned *Sandbox as non-nil.
func newIntegrationSandbox(t *testing.T, opts CreateOptions) *Sandbox {
	t.Helper()
	skipIfNoAPIKey(t)

	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.Metadata == nil {
		opts.Metadata = map[string]string{}
	}
	if _, ok := opts.Metadata["test_id"]; !ok {
		opts.Metadata["test_id"] = t.Name()
	}

	ctx, cancel := integrationContext(t)
	defer cancel()

	sbx, err := Create(ctx, opts)
	if err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}
	t.Cleanup(func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			t.Logf("sandbox cleanup (id=%s): %v", sbx.ID, err)
		}
	})
	return sbx
}
