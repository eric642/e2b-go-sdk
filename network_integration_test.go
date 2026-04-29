//go:build integration

package e2b

import (
	"errors"
	"testing"
)

// TestIntegrationNetworkDenyAllOut mirrors python-sdk tests/sync/sandbox_sync/test_network.py:
// a sandbox created with DenyOut=[AllTraffic] should be unable to reach the
// internet (curl exits non-zero).
func TestIntegrationNetworkDenyAllOut(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{
		Network: &NetworkOptions{DenyOut: []string{AllTraffic}},
	})
	ctx, cancel := integrationContext(t)
	defer cancel()

	_, err := sbx.Commands.Run(ctx, "sh", RunOptions{
		Args:      []string{"-c", "curl -sS -m 5 https://example.com"},
		TimeoutMs: 10_000,
	})
	// Either a CommandExitError (curl non-zero) or a TimeoutError are fine —
	// both prove egress was blocked.
	if err == nil {
		t.Fatal("expected egress-denied error, curl succeeded")
	}
	var cee *CommandExitError
	var te *TimeoutError
	if !errors.As(err, &cee) && !errors.As(err, &te) {
		t.Fatalf("unexpected error type %T: %v", err, err)
	}
}
