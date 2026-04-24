package e2b

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationSandboxCreateAndKill(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	if sbx.ID == "" {
		t.Fatal("sandbox ID must be populated")
	}
	t.Logf("created sandbox: %s (domain=%s)", sbx.ID, sbx.Domain)

	ctx, cancel := integrationContext(t)
	defer cancel()

	ok, err := sbx.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !ok {
		t.Fatal("fresh sandbox should be running")
	}
}

func TestIntegrationSandboxConnect(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	other, err := Connect(ctx, sbx.ID, ConnectOptions{Config: Config{APIKey: sbx.cfg.APIKey}})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if other.ID != sbx.ID {
		t.Fatalf("Connect returned a different sandbox: %s vs %s", other.ID, sbx.ID)
	}
}

func TestIntegrationSandboxSetTimeout(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	if err := sbx.SetTimeout(ctx, 120*time.Second); err != nil {
		t.Fatalf("SetTimeout: %v", err)
	}
}

func TestIntegrationSandboxGetInfo(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{
		Metadata: map[string]string{"purpose": "integration-info"},
	})
	ctx, cancel := integrationContext(t)
	defer cancel()

	info, err := sbx.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.SandboxID != sbx.ID {
		t.Fatalf("SandboxID mismatch: %s vs %s", info.SandboxID, sbx.ID)
	}
	if info.State != SandboxStateRunning {
		t.Fatalf("expected running, got %s", info.State)
	}
	if info.Metadata["purpose"] != "integration-info" {
		t.Fatalf("metadata not round-tripped: %+v", info.Metadata)
	}
}

func TestIntegrationSandboxGetMetrics(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	// Metrics may be empty for a fresh sandbox; just verify the call works.
	if _, err := sbx.GetMetrics(ctx); err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
}

func TestIntegrationSandboxPauseAndResume(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{
		Lifecycle: &LifecycleOptions{AutoResume: true},
	})
	ctx, cancel := integrationContext(t)
	defer cancel()

	paused, err := sbx.Pause(ctx)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !paused {
		t.Log("sandbox reported already paused; continuing")
	}

	// Connect resumes a paused sandbox.
	resumed, err := Connect(ctx, sbx.ID, ConnectOptions{Config: Config{APIKey: sbx.cfg.APIKey}})
	if err != nil {
		t.Fatalf("Connect (resume): %v", err)
	}
	if resumed.ID != sbx.ID {
		t.Fatalf("resumed sandbox mismatch: %s vs %s", resumed.ID, sbx.ID)
	}
}

func TestIntegrationKillNonExistent(t *testing.T) {
	skipIfNoAPIKey(t)
	ctx, cancel := integrationContext(t)
	defer cancel()
	ok, err := Kill(ctx, "sbx-does-not-exist-deadbeef", ConnectOptions{})
	// Unknown sandboxes should resolve to (false, nil) — matching Python/JS.
	if err != nil {
		// Accept the 401/403 if the key path fails entirely, but a
		// well-authenticated client should hit 404 → (false, nil).
		var ae *AuthenticationError
		if !errors.As(err, &ae) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok {
		t.Fatal("Kill of unknown sandbox should not return true")
	}
}
