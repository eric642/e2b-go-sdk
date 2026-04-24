package e2b

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIntegrationCommandRunEcho(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "echo hello world"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: %d (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello world") {
		t.Fatalf("stdout: %q", res.Stdout)
	}
}

func TestIntegrationCommandSpecialChars(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	// Special characters and multi-line output.
	h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "printf 'line1\\nline2\\n'; echo 'quo\"te'"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "line1") || !strings.Contains(res.Stdout, "line2") {
		t.Fatalf("multiline stdout: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, `quo"te`) {
		t.Fatalf("special chars: %q", res.Stdout)
	}
}

func TestIntegrationCommandEnvVars(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	h, err := sbx.Commands.Run(ctx, "sh", RunOptions{
		Args: []string{"-c", "echo $MY_VAR"},
		Envs: map[string]string{"MY_VAR": "hello-env"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "hello-env") {
		t.Fatalf("env not propagated: stdout=%q", res.Stdout)
	}
}

func TestIntegrationCommandNonZeroExit(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	_, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "echo boom; exit 7"}})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	var cee *CommandExitError
	if !errors.As(err, &cee) {
		t.Fatalf("want *CommandExitError, got %T: %v", err, err)
	}
	if cee.Result.ExitCode != 7 {
		t.Fatalf("ExitCode: %d", cee.Result.ExitCode)
	}
}

func TestIntegrationCommandBackgroundKill(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "sleep 60"}, Background: true})
	if err != nil {
		t.Fatalf("Run background: %v", err)
	}
	// Allow the stream to receive the start event.
	deadline := time.Now().Add(10 * time.Second)
	for h.PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.PID() == 0 {
		t.Fatal("never received PID")
	}
	if _, err := h.Kill(ctx); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

func TestIntegrationCommandStdin(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	h, err := sbx.Commands.Run(ctx, "cat", RunOptions{Stdin: bytes.NewReader([]byte("piped-input\n"))})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "piped-input") {
		t.Fatalf("stdout: %q", res.Stdout)
	}
}

func TestIntegrationCommandListAndKillByPID(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "sleep 60"}, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.PID() == 0 {
		t.Fatal("never received PID")
	}

	procs, err := sbx.Commands.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range procs {
		if p.PID == h.PID() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("PID %d not present in List result (%d procs)", h.PID(), len(procs))
	}

	if _, err := sbx.Commands.Kill(ctx, h.PID()); err != nil {
		t.Fatalf("Commands.Kill: %v", err)
	}
}

func TestIntegrationCommandTimeout(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	_, err := sbx.Commands.Run(ctx, "sh", RunOptions{
		Args:      []string{"-c", "sleep 30"},
		TimeoutMs: 500,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want *TimeoutError, got %T: %v", err, err)
	}
}
