// Interactive terminal example: open an E2B sandbox, attach the local TTY to a
// remote PTY (bash) in raw mode, and close the sandbox when the user exits.
//
// Usage:
//
//	E2B_API_KEY=...             # team API key
//	E2B_DOMAIN=e2b.example.com  # optional self-hosted domain
//	E2B_TEMPLATE=base           # optional template id/alias
//	go run ./examples/terminal
//
// Type `exit` or press Ctrl+D inside the remote shell to quit. The sandbox
// is killed automatically on return. Terminal resize forwarding uses SIGWINCH
// on Unix and is a no-op on other platforms.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/eric642/e2b-go-sdk"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg := e2b.Config{
		APIKey: os.Getenv("E2B_API_KEY"),
		Domain: os.Getenv("E2B_DOMAIN"),
		APIURL: os.Getenv("E2B_API_URL"),
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("E2B_API_KEY is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := e2b.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}

	sbx, err := client.Create(ctx, e2b.CreateOptions{
		Template: envOr("E2B_TEMPLATE", "base"),
		Timeout:  30 * time.Minute,
		Metadata: map[string]string{"example": "terminal"},
	})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			log.Printf("kill sandbox: %v", err)
		}
	}()

	fmt.Printf("sandbox %s ready, attaching bash...\r\n", sbx.ID)

	fd := int(os.Stdin.Fd())
	cols, rows := 80, 24
	if term.IsTerminal(fd) {
		if c, r, err := term.GetSize(fd); err == nil {
			cols, rows = c, r
		}
	}

	handle, err := sbx.Pty.Create(ctx, e2b.PtyOptions{
		Cmd:  "/bin/bash",
		Cols: uint32(cols),
		Rows: uint32(rows),
		Envs: map[string]string{"TERM": envOr("TERM", "xterm-256color")},
	})
	if err != nil {
		return fmt.Errorf("pty create: %w", err)
	}

	pid, err := waitForPID(handle, 5*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for pid: %w", err)
	}

	var oldState *term.State
	if term.IsTerminal(fd) {
		oldState, err = term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("make raw: %w", err)
		}
		defer func() {
			if oldState != nil {
				_ = term.Restore(fd, oldState)
			}
		}()
	}

	go pumpStdinToPty(ctx, sbx, pid)
	go pumpPtyToStdout(handle)
	go watchResize(ctx, sbx, pid, fd)

	result, err := handle.Wait(ctx)
	if oldState != nil {
		_ = term.Restore(fd, oldState)
		oldState = nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\r\nsession ended: %v\r\n", err)
	}
	if result != nil {
		fmt.Fprintf(os.Stderr, "\r\nexit code: %d (sandbox %s)\r\n", result.ExitCode, sbx.ID)
	}
	return nil
}

func waitForPID(h *e2b.CommandHandle, timeout time.Duration) (uint32, error) {
	deadline := time.Now().Add(timeout)
	for {
		if pid := h.PID(); pid != 0 {
			return pid, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("pid not reported within %s", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func pumpStdinToPty(ctx context.Context, sbx *e2b.Sandbox, pid uint32) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if sendErr := sbx.Pty.SendInput(ctx, pid, buf[:n]); sendErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func pumpPtyToStdout(h *e2b.CommandHandle) {
	for data := range h.PtyOutput() {
		if _, err := os.Stdout.Write(data); err != nil {
			return
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
