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
// is killed automatically on return. Unix only (uses SIGWINCH).
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/eric642/e2b-go-sdk"
)

func main() {
	cfg := e2b.Config{
		APIKey: os.Getenv("E2B_API_KEY"),
		Domain: os.Getenv("E2B_DOMAIN"),
		APIURL: os.Getenv("E2B_API_URL"),
		// PTY event stream is long-lived; the default 60s HTTP client timeout
		// would cut it off mid-session.
		RequestTimeoutDisabled: true,
	}
	if cfg.APIKey == "" {
		log.Fatal("E2B_API_KEY is required")
	}

	ctx := context.Background()

	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
		Config:   cfg,
		Template: envOr("E2B_TEMPLATE", "base"),
		Timeout:  30 * time.Minute,
		Metadata: map[string]string{"example": "terminal"},
		Secure:   true,
	})
	if err != nil {
		log.Fatalf("create sandbox: %v", err)
	}
	defer func() {
		if err := sbx.Kill(context.Background()); err != nil {
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
		log.Fatalf("pty create: %v", err)
	}

	pid, err := waitForPID(handle, 5*time.Second)
	if err != nil {
		log.Fatalf("waiting for pid: %v", err)
	}

	var oldState *term.State
	if term.IsTerminal(fd) {
		oldState, err = term.MakeRaw(fd)
		if err != nil {
			log.Fatalf("make raw: %v", err)
		}
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	go pumpStdinToPty(ctx, sbx, pid)
	go pumpPtyToStdout(handle)
	go watchResize(ctx, sbx, pid, fd)

	result, err := handle.Wait(ctx)
	if oldState != nil {
		_ = term.Restore(fd, oldState)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\r\nsession ended: %v\r\n", err)
	}
	if result != nil {
		fmt.Fprintf(os.Stderr, "\r\nexit code: %d (sandbox %s)\r\n", result.ExitCode, sbx.ID)
	}
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
			if err != io.EOF {
				return
			}
			return
		}
	}
}

func watchResize(ctx context.Context, sbx *e2b.Sandbox, pid uint32, fd int) {
	if !term.IsTerminal(fd) {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	for range ch {
		cols, rows, err := term.GetSize(fd)
		if err != nil {
			continue
		}
		_ = sbx.Pty.Resize(ctx, pid, uint32(cols), uint32(rows))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
