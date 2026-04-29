//go:build integration

package e2b

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegrationPtyCreateAndSendInput(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	var mu sync.Mutex
	var buf bytes.Buffer
	onData := func(b []byte) {
		mu.Lock()
		buf.Write(b)
		mu.Unlock()
	}

	h, err := sbx.Pty.Create(ctx, PtyOptions{
		Cmd:    "/bin/bash",
		Args:   []string{"--noprofile", "--norc"},
		Cols:   80,
		Rows:   24,
		OnData: onData,
	})
	if err != nil {
		t.Fatalf("Pty.Create: %v", err)
	}

	// Wait for a PID so we know the process is ready for input.
	deadline := time.Now().Add(10 * time.Second)
	for h.PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.PID() == 0 {
		t.Fatal("PTY never received PID")
	}

	if err := sbx.Pty.SendInput(ctx, h.PID(), []byte("echo hello-pty\n")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// Allow the echo to round-trip.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		contains := strings.Contains(buf.String(), "hello-pty")
		mu.Unlock()
		if contains {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if !strings.Contains(got, "hello-pty") {
		t.Fatalf("pty output missing echo: %q", got)
	}

	if err := sbx.Pty.Resize(ctx, h.PID(), 120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := sbx.Pty.Kill(ctx, h.PID()); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}
