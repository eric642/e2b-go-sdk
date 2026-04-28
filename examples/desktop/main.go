// Desktop example: build the local e2b.Dockerfile as a template, launch a
// sandbox, print the noVNC URL, then prove that SDK PTY sessions are mirrored
// into the shared tmux that the VNC desktop shows.
//
// Requirements verified here:
//  1. Browser GUI over VNC: prints an HTTPS noVNC URL the caller can open.
//  2. Bash-PTY visibility: writes a marker to a bash PTY started via
//     Pty.Create, then snapshots `tmux -L main` with Commands.Run and asserts
//     the marker appears there.
//  3. Dev toolkit smoke test: python3 + node imports run cleanly.
//
// Note: `Commands.Run` does NOT go through a PTY and therefore is NOT mirrored
// to the VNC desktop. Use `Pty.Create` for a visible session, or explicitly
// send keystrokes with `tmux -L main send-keys -t main "<cmd>" Enter`.
//
// Usage:
//
//	source ./.env && go run ./examples/desktop
//
// Set E2B_TEMPLATE_ID to reuse an existing template (skips build + teardown).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/template"
)

const (
	dockerfilePath     = "e2b.Dockerfile"
	terminalVNCPort    = 6080 // DISPLAY=:0 — xterm + shared tmux
	browserVNCPort     = 6081 // DISPLAY=:1 — chromium is launched on demand
	browserDisplay     = ":1"
	tmuxSocket         = "main"
	tmuxSession        = "main"
	ptyMarker          = "HELLO_FROM_SDK_PTY"
	templateIDEnv      = "E2B_TEMPLATE_ID"
	defaultBrowserURL  = "https://google.com"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// 1) Resolve the template. If E2B_TEMPLATE_ID is set we reuse it as-is
	//    (no build, no teardown). Otherwise build from the local Dockerfile;
	//    Xvfb readiness guarantees the X stack is up, the example polls the rest.
	templateID, ok := os.LookupEnv(templateIDEnv)
	if ok && templateID != "" {
		fmt.Printf("reusing template from %s: id=%s\n", templateIDEnv, templateID)
	} else {
		cli, err := template.NewClient(e2b.Config{})
		if err != nil {
			log.Fatalf("template client: %v", err)
		}

		tpl := template.New().
			FromDockerfileFile(dockerfilePath).
			SetStartCmd("/usr/local/bin/start-desktop.sh", template.WaitForProcess("Xvfb"))

		name := fmt.Sprintf("go-sdk-desktop-demo-%d", time.Now().Unix())
		// 2 vCPU / 4 GiB RAM. Disk size is not exposed on the public build
		// API — it is provisioned from the team's plan (request 50 GiB there
		// if the default is too small).
		events, err := cli.BuildStream(ctx, tpl.Builder(), template.BuildOptions{
			Name:     name,
			CPUCount: 2,
			MemoryMB: 4096,
		})
		if err != nil {
			log.Fatalf("build start: %v", err)
		}
		var info *template.BuildInfo
		for ev := range events {
			switch {
			case ev.Log != nil:
				fmt.Printf("[%s] %s\n", ev.Log.Level, ev.Log.Message)
			case ev.Err != nil:
				log.Fatalf("build: %v", ev.Err)
			case ev.Done != nil:
				info = ev.Done
			}
		}
		if info == nil {
			log.Fatal("build finished without a BuildInfo")
		}
		templateID = info.TemplateID
		fmt.Printf("\ntemplate built: id=%s build=%s\n", info.TemplateID, info.BuildID)
		defer func() {
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer deleteCancel()
			if err := cli.Delete(deleteCtx, info.TemplateID); err != nil {
				log.Printf("delete template: %v", err)
				return
			}
			fmt.Printf("template deleted: %s\n", info.TemplateID)
		}()
	}

	// 2) Launch a sandbox and tear it down on exit. RequestTimeoutDisabled on
	//    Config prevents the default 60s HTTP client from cutting the PTY
	//    stream short.
	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
		Config:   e2b.Config{RequestTimeoutDisabled: true},
		Template: templateID,
		Timeout:  15 * time.Minute,
		Secure:   true,
	})
	if err != nil {
		log.Fatalf("create sandbox: %v", err)
	}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if err := sbx.Kill(killCtx); err != nil {
			log.Printf("sandbox kill: %v", err)
			return
		}
		fmt.Printf("sandbox killed: %s\n", sbx.ID)
	}()
	fmt.Printf("sandbox started: id=%s\n", sbx.ID)

	// 3) Wait for the rest of the desktop stack (websockify + tmux session)
	//    so the PTY-mirror assertion is deterministic.
	if err := waitForDesktop(ctx, sbx, 3*time.Minute); err != nil {
		log.Printf("desktop diagnostics:\n%s", collectDesktopDiagnostics(ctx, sbx))
		log.Fatalf("desktop didn't come up: %v", err)
	}
	fmt.Printf("terminal noVNC URL: https://%s/vnc.html\n", sbx.GetHost(terminalVNCPort))

	// 4) Requirement 2: open an interactive PTY via the SDK, echo a marker,
	//    then assert the marker shows up in the shared tmux session.
	if err := proveSharedTmux(ctx, sbx); err != nil {
		log.Fatalf("shared tmux assertion failed: %v", err)
	}
	fmt.Println("PTY-mirror OK: SDK bash output is visible in the VNC desktop.")

	// 5) Requirement 3: dev toolkit smoke test.
	if err := smokeDevToolkit(ctx, sbx); err != nil {
		log.Fatalf("dev toolkit smoke test: %v", err)
	}
	fmt.Println("Python & Node toolkits OK.")

	// 6) Launch chromium on demand and print a noVNC URL that lands on the
	//    opened page. Browser starts here (not in start-desktop.sh) to keep
	//    the baseline sandbox lightweight.
	browserURL := defaultBrowserURL
	if v := os.Getenv("DESKTOP_BROWSER_URL"); v != "" {
		browserURL = v
	}
	if err := launchBrowser(ctx, sbx, browserURL); err != nil {
		log.Fatalf("launch browser: %v", err)
	}
	fmt.Printf("browser noVNC URL:  https://%s/vnc.html (opened %s)\n", sbx.GetHost(browserVNCPort), browserURL)

	fmt.Println("All checks passed. Open either noVNC URL above to interact with the desktop.")
	fmt.Println("Press Ctrl+C to tear down the sandbox.")
	_ = os.Stdout.Sync()

	// Block until the user sends SIGINT/SIGTERM. The deferred sandbox.Kill
	// and template.Delete still run after this returns.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case s := <-sigCh:
		fmt.Printf("\nreceived %s, shutting down...\n", s)
	case <-ctx.Done():
		fmt.Printf("\ncontext done (%v), shutting down...\n", ctx.Err())
	}
}

// waitForDesktop polls inside the sandbox until websockify and the shared
// tmux session are both live. `ready_cmd` only waits for Xvfb so we don't
// stall the build on GUI startup ordering; this fills in the remaining gap.
// On timeout it dumps the relevant log tails so the caller can see which
// service failed to come up.
func waitForDesktop(ctx context.Context, sbx *e2b.Sandbox, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	checks := []struct {
		name string
		cmd  string
	}{
		{"websockify-terminal", fmt.Sprintf("pgrep -f 'websockify.*%d' >/dev/null", terminalVNCPort)},
		{"websockify-browser", fmt.Sprintf("pgrep -f 'websockify.*%d' >/dev/null", browserVNCPort)},
		{"tmux-session", fmt.Sprintf("tmux -L %s has-session -t %s", tmuxSocket, tmuxSession)},
	}
	var lastMissing string
	for {
		missing := ""
		for _, c := range checks {
			if !sandboxCheck(ctx, sbx, c.cmd) {
				missing = c.name
				break
			}
		}
		if missing == "" {
			return nil
		}
		lastMissing = missing
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s after %s", lastMissing, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// sandboxCheck runs a shell predicate in the sandbox and reports whether it
// exited 0. Any transport or non-zero exit is treated as "not ready yet".
func sandboxCheck(ctx context.Context, sbx *e2b.Sandbox, cmd string) bool {
	h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", cmd},
		TimeoutMs: 5_000,
	})
	if err != nil {
		return false
	}
	res, err := h.Wait(ctx)
	return err == nil && res != nil && res.ExitCode == 0
}

// collectDesktopDiagnostics grabs a short snapshot of process state + the log
// tails produced by start-desktop.sh so a waitForDesktop timeout is easier
// to diagnose without re-opening a VNC session.
func collectDesktopDiagnostics(ctx context.Context, sbx *e2b.Sandbox) string {
	diagCmd := strings.Join([]string{
		`echo "--- whoami / id ---"`,
		`whoami; id`,
		`echo "--- ps (Xvfb/x11vnc/websockify/tmux/fluxbox/xterm) ---"`,
		`ps -eo pid,user,cmd | grep -E 'Xvfb|x11vnc|websockify|tmux|fluxbox|xterm|start-desktop' | grep -v grep || true`,
		`echo "--- tmux sockets on disk ---"`,
		`ls -la /tmp/tmux-* 2>&1 || true`,
		`echo "--- tmux list-sessions (as current user) ---"`,
		fmt.Sprintf(`tmux -L %s list-sessions 2>&1 || true`, tmuxSocket),
		`echo "--- tmux list-sessions (root view) ---"`,
		fmt.Sprintf(`sudo -n tmux -L %s list-sessions 2>&1 || true`, tmuxSocket),
		`for f in /tmp/websockify.log /tmp/websockify.1.log /tmp/x11vnc.log /tmp/x11vnc.1.log /tmp/xterm.log /tmp/fluxbox.log /tmp/fluxbox.1.log /tmp/chromium.log; do`,
		`  echo "--- $f ---"; tail -n 40 "$f" 2>&1 || true`,
		`done`,
	}, "; ")
	h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", diagCmd},
		TimeoutMs: 10_000,
	})
	if err != nil {
		return fmt.Sprintf("diagnostics run: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		return fmt.Sprintf("diagnostics wait: %v", err)
	}
	return res.Stdout + res.Stderr
}

// proveSharedTmux opens a bash PTY through the SDK. /etc/profile.d/e2b-tmux.sh
// should exec the shell into the shared tmux session, so the echoed marker
// lands in the same buffer the xterm inside the VNC desktop is displaying.
// We then capture that pane via Commands.Run and assert the marker is there.
func proveSharedTmux(ctx context.Context, sbx *e2b.Sandbox) error {
	// -l makes it a login shell so /etc/profile.d/e2b-tmux.sh fires and exec's
	// us into the shared tmux session. Without it, bash skips profile.d and
	// the echo below goes to a plain PTY instead of the tmux pane.
	pty, err := sbx.Pty.Create(ctx, e2b.PtyOptions{
		Cmd:  "/bin/bash",
		Args: []string{"-l"},
		Cols: 120,
		Rows: 30,
		Envs: map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		return fmt.Errorf("pty create: %w", err)
	}
	defer func() { _, _ = pty.Kill(context.Background()) }()

	pid, err := waitForPID(pty, 10*time.Second)
	if err != nil {
		return err
	}

	// Give the profile hook time to swap the shell into tmux before we type.
	time.Sleep(1500 * time.Millisecond)

	payload := fmt.Sprintf("echo %s\n", ptyMarker)
	if err := sbx.Pty.SendInput(ctx, pid, []byte(payload)); err != nil {
		return fmt.Errorf("pty send input: %w", err)
	}
	// Small delay so tmux has time to render the line before we capture it.
	time.Sleep(1500 * time.Millisecond)

	capture := fmt.Sprintf("tmux -L %s capture-pane -t %s -p", tmuxSocket, tmuxSession)
	h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", capture},
		TimeoutMs: 5_000,
	})
	if err != nil {
		return fmt.Errorf("tmux capture-pane: %w", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		return fmt.Errorf("tmux capture-pane wait: %w", err)
	}
	if !strings.Contains(res.Stdout, ptyMarker) {
		return fmt.Errorf("marker %q not found in tmux pane; pane=%q", ptyMarker, res.Stdout)
	}
	return nil
}

// smokeDevToolkit imports the most commonly used Python + Node libraries
// pre-installed in the Dockerfile. A non-zero exit from either interpreter
// means a dependency is missing.
func smokeDevToolkit(ctx context.Context, sbx *e2b.Sandbox) error {
	checks := []struct {
		name string
		cmd  string
	}{
		{"python3", `python3 -c "import httpx, bs4, lxml, playwright, pydantic; print('py ok')"`},
		// Global npm packages land in /usr/lib/node_modules; node won't search
		// there unless NODE_PATH says so. The Dockerfile exports NODE_PATH,
		// but setting it inline makes the smoke test independent of whether
		// envd inherits the image's ENV.
		{"node", `NODE_PATH=/usr/lib/node_modules node -e "require('puppeteer-core'); require('playwright'); console.log('node ok')"`},
	}
	for _, c := range checks {
		// Commands.Run returns *CommandExitError on non-zero exit; that error
		// wraps stdout/stderr/exit_code. Surface all three so a failing check
		// shows which import broke instead of a bare "exit status 1".
		h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
			Args:      []string{"-c", c.cmd},
			TimeoutMs: 30_000,
		})
		var exitErr *e2b.CommandExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s failed (exit=%d)\ncmd: %s\nstdout: %s\nstderr: %s",
				c.name, exitErr.Result.ExitCode, c.cmd, exitErr.Result.Stdout, exitErr.Result.Stderr)
		}
		if err != nil {
			return fmt.Errorf("%s run: %w", c.name, err)
		}
		res, err := h.Wait(ctx)
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s failed (exit=%d)\ncmd: %s\nstdout: %s\nstderr: %s",
				c.name, exitErr.Result.ExitCode, c.cmd, exitErr.Result.Stdout, exitErr.Result.Stderr)
		}
		if err != nil {
			return fmt.Errorf("%s wait: %w", c.name, err)
		}
		fmt.Printf("  %s -> %s", c.name, res.Stdout)
	}
	return nil
}

// launchBrowser starts chromium on the browser X display (browserDisplay)
// pointing at `url`, and waits until the window is mapped so the returned
// noVNC URL actually shows the page. Running on its own :1 keeps chromium
// from overlapping the xterm shown on :0. nohup + background shell keep it
// alive after the Commands.Run RPC returns.
func launchBrowser(ctx context.Context, sbx *e2b.Sandbox, url string) error {
	// Best-effort cleanup of a prior chromium (same profile dir).
	if killH, killErr := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", "pkill -f 'chromium.*--user-data-dir=/tmp/chromium-profile' 2>/dev/null; mkdir -p /tmp/chromium-profile"},
		TimeoutMs: 5_000,
	}); killErr == nil {
		_, _ = killH.Wait(ctx)
	}

	// Launch chromium as a first-class background process so envd tracks it
	// directly — wrapping it in `sh -c "... &"` got the shell torn down
	// before chromium could even open its log. Flags required inside most
	// containers: --no-sandbox (no user-ns), --disable-dev-shm-usage
	// (avoids /dev/shm size issues).
	_, err := sbx.Commands.Run(ctx, "chromium", e2b.RunOptions{
		Args: []string{
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--no-first-run",
			"--no-default-browser-check",
			"--user-data-dir=/tmp/chromium-profile",
			url,
		},
		Envs: map[string]string{
			"DISPLAY":    browserDisplay,
			"XAUTHORITY": "/home/user/.Xauthority",
		},
		Background: true,
	})
	if err != nil {
		return fmt.Errorf("chromium start: %w", err)
	}

	// Poll xwininfo (from x11-utils, already in the image) until a chromium
	// window shows up on the browser display so the caller opening the URL
	// doesn't hit an empty desktop. Cap at 30s — cold chromium on a fresh
	// sandbox usually takes 3–8s.
	deadline := time.Now().Add(30 * time.Second)
	check := fmt.Sprintf(`DISPLAY=%s xwininfo -root -tree 2>/dev/null | grep -iq 'chromium'`, browserDisplay)
	for {
		if sandboxCheck(ctx, sbx, check) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("chromium window didn't appear within 30s\n%s",
				collectBrowserDiagnostics(ctx, sbx))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// collectBrowserDiagnostics tails chromium/X logs and reports process + window
// state so a launchBrowser timeout explains itself.
func collectBrowserDiagnostics(ctx context.Context, sbx *e2b.Sandbox) string {
	diag := strings.Join([]string{
		`echo "--- chromium processes ---"`,
		`ps -eo pid,user,cmd | grep -i chromium | grep -v grep || echo "(none)"`,
		fmt.Sprintf(`echo "--- xwininfo -root -tree on %s ---"`, browserDisplay),
		fmt.Sprintf(`DISPLAY=%s xwininfo -root -tree 2>&1 || true`, browserDisplay),
		`echo "--- /tmp/chromium.log ---"`,
		`tail -n 80 /tmp/chromium.log 2>&1 || true`,
		fmt.Sprintf(`echo "--- /tmp/x11vnc%s.log ---"`, displayLogSuffix(browserDisplay)),
		fmt.Sprintf(`tail -n 40 /tmp/x11vnc%s.log 2>&1 || true`, displayLogSuffix(browserDisplay)),
		fmt.Sprintf(`echo "--- /tmp/fluxbox%s.log ---"`, displayLogSuffix(browserDisplay)),
		fmt.Sprintf(`tail -n 40 /tmp/fluxbox%s.log 2>&1 || true`, displayLogSuffix(browserDisplay)),
	}, "; ")
	h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
		Args:      []string{"-c", diag},
		TimeoutMs: 10_000,
	})
	if err != nil {
		return fmt.Sprintf("diagnostics run: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		return fmt.Sprintf("diagnostics wait: %v", err)
	}
	return res.Stdout + res.Stderr
}

// displayLogSuffix turns ":1" into ".1" to match the log filenames that
// start-desktop.sh writes (:0 uses no suffix, :1 uses ".1").
func displayLogSuffix(display string) string {
	if display == ":0" {
		return ""
	}
	return strings.ReplaceAll(display, ":", ".")
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
