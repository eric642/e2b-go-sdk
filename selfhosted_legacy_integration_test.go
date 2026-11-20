//go:build integration

package e2b_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	e2b "github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/template"
)

var (
	selfhostedConfigFlag = flag.String("e2b.selfhosted-config", "", "Path to E2B CLI config JSON for legacy self-hosted integration tests (fallback when E2B_SELFHOSTED_CONFIG is unset)")
	selfhostedDomainFlag = flag.String("e2b.selfhosted-domain", "", "Domain for legacy self-hosted integration tests (fallback when E2B_SELFHOSTED_DOMAIN is unset)")
)

type selfhostedCLIConfig struct {
	TeamAPIKey  string `json:"teamApiKey"`
	AccessToken string `json:"accessToken"`
	TeamID      string `json:"teamId"`
}

type selfhostedEnv struct {
	domain      string
	teamAPIKey  string
	accessToken string
	teamID      string
}

func loadSelfHostedEnv(t *testing.T) selfhostedEnv {
	t.Helper()

	cfgPath := strings.TrimSpace(os.Getenv("E2B_SELFHOSTED_CONFIG"))
	if cfgPath == "" {
		cfgPath = strings.TrimSpace(*selfhostedConfigFlag)
	}
	if cfgPath == "" {
		t.Skip("E2B_SELFHOSTED_CONFIG not set; skipping legacy self-hosted integration test")
	}

	domain := strings.TrimSpace(os.Getenv("E2B_SELFHOSTED_DOMAIN"))
	if domain == "" {
		domain = strings.TrimSpace(*selfhostedDomainFlag)
	}
	if domain == "" {
		domain = strings.TrimSpace(os.Getenv("E2B_DOMAIN"))
	}
	if domain == "" {
		t.Fatal("E2B_SELFHOSTED_DOMAIN (or E2B_DOMAIN) must be set when E2B_SELFHOSTED_CONFIG is provided")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read E2B_SELFHOSTED_CONFIG (%s): %v", cfgPath, err)
	}
	var parsed selfhostedCLIConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse E2B_SELFHOSTED_CONFIG JSON (%s): %v", cfgPath, err)
	}
	if parsed.TeamAPIKey == "" && parsed.AccessToken == "" {
		t.Fatalf("E2B_SELFHOSTED_CONFIG (%s) contains neither teamApiKey nor accessToken", cfgPath)
	}

	return selfhostedEnv{
		domain:      domain,
		teamAPIKey:  parsed.TeamAPIKey,
		accessToken: parsed.AccessToken,
		teamID:      parsed.TeamID,
	}
}

func selfhostedCtx(t *testing.T, fallback time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	if d, ok := t.Deadline(); ok {
		if rem := time.Until(d) - 5*time.Second; rem > 0 && rem < fallback {
			return context.WithTimeout(context.Background(), rem)
		}
	}
	return context.WithTimeout(context.Background(), fallback)
}

func semverAtLeast(ver, want string) bool {
	parse := func(s string) [3]int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		if idx := strings.IndexAny(s, "-+"); idx >= 0 {
			s = s[:idx]
		}
		parts := strings.Split(s, ".")
		var out [3]int
		for i := 0; i < 3 && i < len(parts); i++ {
			n, _ := strconv.Atoi(parts[i])
			out[i] = n
		}
		return out
	}
	if strings.TrimSpace(ver) == "" {
		return false
	}
	a, b := parse(ver), parse(want)
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func newSelfHostedTemplateClient(t *testing.T, env selfhostedEnv) *template.Client {
	t.Helper()
	cli, err := template.NewClient(e2b.Config{
		Domain:      env.domain,
		APIKey:      env.teamAPIKey,
		AccessToken: env.accessToken,
	})
	if err != nil {
		t.Fatalf("template.NewClient: %v", err)
	}
	return cli
}

func resolveTemplateClientAndList(ctx context.Context, t *testing.T, env selfhostedEnv) (*template.Client, []template.Template) {
	t.Helper()
	cli := newSelfHostedTemplateClient(t, env)
	tpls, err := cli.ListV1(ctx, template.ListOptions{TeamID: env.teamID})
	if err != nil {
		t.Fatalf("template.ListV1 (unified dual-credential client): %T: %v", err, err)
	}
	t.Logf("template.ListV1 succeeded via unified template.Client: %d templates", len(tpls))
	return cli, tpls
}

func TestSelfHostedLegacyTemplates(t *testing.T) {
	env := loadSelfHostedEnv(t)
	ctx, cancel := selfhostedCtx(t, 3*time.Minute)
	defer cancel()

	tplCli, tpls := resolveTemplateClientAndList(ctx, t, env)

	tplsViaList, err := tplCli.List(ctx, template.ListOptions{TeamID: env.teamID})
	if err != nil {
		t.Fatalf("template.List: %T: %v", err, err)
	}
	if len(tplsViaList) != len(tpls) {
		t.Logf("template.List count=%d vs ListV1 count=%d", len(tplsViaList), len(tpls))
	}
	for i, tm := range tpls {
		if i < 10 {
			t.Logf("template[%d]: id=%s buildID=%s aliases=%v names=%v public=%v cpu=%d memMB=%d envd=%q",
				i, tm.TemplateID, tm.BuildID, tm.Aliases, tm.Names, tm.Public, tm.CPUCount, tm.MemoryMB, tm.EnvdVersion)
		}
		if tm.TemplateID == "" {
			t.Fatalf("template[%d] has empty TemplateID", i)
		}
	}

	t.Run("BuildV3FailsFastWithoutFallback", func(t *testing.T) {
		probeName := fmt.Sprintf("sdk-v3-probe-%d", time.Now().UnixNano())
		b := template.New().FromBaseImage().RunCmd("echo probe")
		info, err := tplCli.BuildInBackground(ctx, b, template.BuildOptions{Name: probeName})
		if err == nil {
			t.Cleanup(func() {
				delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer delCancel()
				_ = tplCli.Delete(delCtx, info.TemplateID)
			})
			t.Logf("POST /v3/templates is supported on this server (created templateID=%s); scheduled cleanup", info.TemplateID)
			return
		}
		var sdkErr e2b.Error
		if !errors.As(err, &sdkErr) {
			t.Fatalf("BuildInBackground (v3) error must implement e2b.Error, got %T: %v", err, err)
		}
		t.Logf("BuildInBackground (POST /v3/templates) failed fast as expected on legacy server: %T: %v", err, err)
	})

	t.Run("ListV2NoHiddenFallback", func(t *testing.T) {
		v2Tpls, err := tplCli.ListAllV2(ctx, template.ListV2Options{TeamID: env.teamID, Limit: 5})
		if err != nil {
			var sdkErr e2b.Error
			if !errors.As(err, &sdkErr) {
				t.Fatalf("ListAllV2 error must implement e2b.Error, got %T: %v", err, err)
			}
			t.Logf("ListAllV2 (GET /v2/templates) failed fast as expected on legacy server: %T: %v", err, err)
			return
		}
		t.Logf("ListAllV2 (GET /v2/templates) supported: returned %d templates", len(v2Tpls))
	})

	t.Run("BuildV2LifecycleIfSupported", func(t *testing.T) {
		buildCtx, buildCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer buildCancel()

		baseTplID := "base"
		for _, tm := range tpls {
			if len(tm.Aliases) > 0 {
				baseTplID = tm.TemplateID
				break
			}
		}
		if baseTplID == "base" && len(tpls) > 0 {
			baseTplID = tpls[0].TemplateID
		}

		name := fmt.Sprintf("go-sdk-legacy-test-%d", time.Now().UnixNano())
		b := template.New().FromTemplate(baseTplID).RunCmd("echo legacy-build-ok")

		info, err := tplCli.BuildInBackgroundV2(buildCtx, b, template.BuildOptions{Name: name})
		if err != nil {
			var sdkErr e2b.Error
			if !errors.As(err, &sdkErr) {
				t.Fatalf("BuildInBackgroundV2 error must implement e2b.Error, got %T: %v", err, err)
			}
			t.Logf("BuildInBackgroundV2 (POST /v2/templates) not enabled/supported on this server (%T: %v)", err, err)
			return
		}

		t.Cleanup(func() {
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			deleted, delErr := tplCli.Destroy(delCtx, info.TemplateID)
			if delErr != nil {
				t.Errorf("cleanup Destroy(%s) failed: %T: %v", info.TemplateID, delErr, delErr)
			} else {
				t.Logf("cleanup Destroy(%s) succeeded (deleted=%v)", info.TemplateID, deleted)
			}
		})
		t.Logf("BuildInBackgroundV2 started: templateID=%s buildID=%s", info.TemplateID, info.BuildID)

		if pubErr := tplCli.SetPublicV1(buildCtx, info.TemplateID, false); pubErr != nil {
			t.Fatalf("SetPublicV1 via unified template.Client failed: %T: %v", pubErr, pubErr)
		}
		t.Logf("SetPublicV1(false) succeeded via unified template.Client for templateID=%s", info.TemplateID)

		offset := 0
		for {
			st, stErr := tplCli.GetBuildStatus(buildCtx, *info, offset)
			if stErr != nil {
				var sdkErr e2b.Error
				if !errors.As(stErr, &sdkErr) {
					t.Fatalf("GetBuildStatus error must implement e2b.Error, got %T: %v", stErr, stErr)
				}
				t.Logf("GetBuildStatus stopped with %T: %v", stErr, stErr)
				break
			}
			offset += len(st.Logs)
			if st.Status == template.BuildStatusReady {
				t.Logf("BuildV2 completed READY (templateID=%s, logs=%d)", info.TemplateID, offset)
				if pubErr := tplCli.SetPublicV1(buildCtx, info.TemplateID, false); pubErr != nil {
					t.Fatalf("SetPublicV1 via unified template.Client failed: %T: %v", pubErr, pubErr)
				}
				t.Logf("SetPublicV1(false) succeeded via unified template.Client for templateID=%s", info.TemplateID)
				break
			}
			if st.Status == template.BuildStatusError {
				t.Logf("BuildV2 ended in cluster build error state (reason=%+v)", st.Reason)
				break
			}
			select {
			case <-buildCtx.Done():
				t.Logf("BuildV2 still building after 2m window (status=%s); deferring to cleanup Destroy", st.Status)
				return
			case <-time.After(2 * time.Second):
			}
		}
	})
}

func TestSelfHostedLegacySandboxSuite(t *testing.T) {
	env := loadSelfHostedEnv(t)
	ctx, cancel := selfhostedCtx(t, 5*time.Minute)
	defer cancel()

	if env.teamAPIKey == "" {
		t.Fatal("teamApiKey is required in E2B_SELFHOSTED_CONFIG for sandbox operations")
	}

	client, err := e2b.NewClient(e2b.Config{
		APIKey: env.teamAPIKey,
		Domain: env.domain,
	})
	if err != nil {
		t.Fatalf("e2b.NewClient: %v", err)
	}

	selectedTemplate := "base"
	tplProbeCli, teamTpls := resolveTemplateClientAndList(ctx, t, env)
	allTpls, allErr := tplProbeCli.ListV1(ctx, template.ListOptions{})
	if allErr == nil && len(allTpls) > 0 {
		t.Logf("unfiltered template.ListV1 returned %d templates", len(allTpls))
		for i, tm := range allTpls {
			if i < 10 {
				t.Logf("all_template[%d]: id=%s aliases=%v public=%v envd=%q", i, tm.TemplateID, tm.Aliases, tm.Public, tm.EnvdVersion)
			}
		}
	} else {
		allTpls = teamTpls
	}
	if len(allTpls) > 0 {
		hasBase := false
		var preferredID string
		for _, tm := range allTpls {
			for _, a := range tm.Aliases {
				if a == "base" {
					hasBase = true
					break
				}
				if preferredID == "" && strings.Contains(a, "office") {
					preferredID = tm.TemplateID
				}
			}
			for _, n := range tm.Names {
				if n == "base" || strings.HasSuffix(n, "/base") {
					hasBase = true
					break
				}
			}
			if tm.TemplateID == "base" {
				hasBase = true
			}
		}
		if !hasBase {
			if preferredID != "" {
				selectedTemplate = preferredID
			} else {
				selectedTemplate = allTpls[0].TemplateID
			}
		}
	}
	t.Logf("selected template for sandbox tests: %q", selectedTemplate)

	t.Run("ControlPlane_CreateV2FailsFast", func(t *testing.T) {
		sbxV2, createErr := client.Create(ctx, e2b.CreateOptions{
			Template: selectedTemplate,
			Timeout:  60 * time.Second,
		})
		if createErr == nil {
			t.Cleanup(func() {
				killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer killCancel()
				_, _ = client.Kill(killCtx, sbxV2.ID)
			})
			t.Fatalf("expected client.Create (POST /v2/sandboxes) to fail on legacy self-hosted server, but created %s", sbxV2.ID)
		}
		var sdkErr e2b.Error
		if !errors.As(createErr, &sdkErr) {
			t.Fatalf("client.Create error must implement e2b.Error, got %T: %v", createErr, createErr)
		}
		t.Logf("client.Create (POST /v2/sandboxes) failed fast without fallback: %T: %v", createErr, createErr)
	})

	testTag := fmt.Sprintf("selfhosted-legacy-%d", time.Now().UnixNano())
	createOpts := e2b.CreateOptions{
		Template: selectedTemplate,
		Timeout:  3 * time.Minute,
		Metadata: map[string]string{
			"suite":   "selfhosted-legacy",
			"test_id": testTag,
		},
		Envs: map[string]string{
			"E2B_LEGACY_SUITE": "1",
		},
	}

	sbx, err := client.CreateV1(ctx, createOpts)
	if err != nil {
		t.Logf("CreateV1 (default secure=true) returned %T: %v; probing with Insecure=true", err, err)
		createOpts.Insecure = true
		sbx, err = client.CreateV1(ctx, createOpts)
		if err != nil {
			t.Fatalf("CreateV1 (Insecure=true) also failed: %T: %v", err, err)
		}
	}
	t.Cleanup(func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		if _, killErr := client.Kill(killCtx, sbx.ID); killErr != nil {
			t.Logf("cleanup Kill(%s): %v", sbx.ID, killErr)
		}
	})

	t.Logf("created live sandbox via CreateV1: id=%s domain=%s envdVersion=%q hasEnvdToken=%v insecure=%v host8080=%s",
		sbx.ID, sbx.Domain, sbx.EnvdVersion, sbx.EnvdAccessToken != "", createOpts.Insecure, sbx.GetHost(8080))

	if sbx.ID == "" {
		t.Fatal("sbx.ID must not be empty")
	}

	t.Run("ControlPlane_UnsupportedV2EndpointsFailFast", func(t *testing.T) {
		if _, connErr := client.Connect(ctx, sbx.ID, e2b.ConnectOptions{}); connErr == nil {
			t.Fatal("expected client.Connect (POST /v2/sandboxes/{id}/connect) to fail on legacy self-hosted server")
		} else {
			var sdkErr e2b.Error
			if !errors.As(connErr, &sdkErr) {
				t.Fatalf("client.Connect error must implement e2b.Error, got %T: %v", connErr, connErr)
			}
			t.Logf("client.Connect (v2) failed fast as expected: %T: %v", connErr, connErr)
		}

		if _, logsV2Err := sbx.GetLogs(ctx); logsV2Err != nil {
			var sdkErr e2b.Error
			if !errors.As(logsV2Err, &sdkErr) {
				t.Fatalf("sbx.GetLogs (v2) error must implement e2b.Error, got %T: %v", logsV2Err, logsV2Err)
			}
			t.Logf("sbx.GetLogs (GET /v2/sandboxes/{id}/logs) failed fast as expected: %T: %v", logsV2Err, logsV2Err)
		} else {
			t.Logf("sbx.GetLogs (GET /v2/sandboxes/{id}/logs) succeeded on this server")
		}
	})

	t.Run("ControlPlane_IsRunningAndGetInfo", func(t *testing.T) {
		running, runErr := sbx.IsRunning(ctx)
		if runErr != nil {
			t.Fatalf("sbx.IsRunning: %T: %v", runErr, runErr)
		}
		if !running {
			t.Fatal("expected sbx.IsRunning == true for newly created sandbox")
		}

		cpRunning, cpErr := client.IsRunning(ctx, sbx.ID)
		if cpErr != nil {
			t.Fatalf("client.IsRunning: %T: %v", cpErr, cpErr)
		}
		if !cpRunning {
			t.Fatal("expected client.IsRunning == true for newly created sandbox")
		}

		info, infoErr := sbx.GetInfo(ctx)
		if infoErr != nil {
			t.Fatalf("sbx.GetInfo: %T: %v", infoErr, infoErr)
		}
		t.Logf("GetInfo: sandboxID=%s templateID=%s state=%q cpu=%d memMB=%d diskMB=%d envd=%q metadata=%v",
			info.SandboxID, info.TemplateID, info.State, info.CPUCount, info.MemoryMB, info.DiskSizeMB, info.EnvdVersion, info.Metadata)

		if info.State != e2b.SandboxStateRunning {
			t.Fatalf("expected normalized State == %q, got %q", e2b.SandboxStateRunning, info.State)
		}
		if info.Metadata["test_id"] != testTag {
			t.Fatalf("expected metadata test_id=%q, got %v", testTag, info.Metadata)
		}

		infoViaClient, clientInfoErr := client.GetInfo(ctx, sbx.ID)
		if clientInfoErr != nil {
			t.Fatalf("client.GetInfo: %T: %v", clientInfoErr, clientInfoErr)
		}
		if infoViaClient.State != e2b.SandboxStateRunning {
			t.Fatalf("expected client.GetInfo State == %q, got %q", e2b.SandboxStateRunning, infoViaClient.State)
		}
	})

	t.Run("ControlPlane_ListV1", func(t *testing.T) {
		items, listErr := client.ListV1(ctx, e2b.SandboxListV1Options{
			Metadata: map[string]string{"test_id": testTag},
		})
		if listErr != nil {
			t.Fatalf("client.ListV1: %T: %v", listErr, listErr)
		}
		found := false
		for _, item := range items {
			if item.SandboxID == sbx.ID || strings.HasPrefix(sbx.ID, item.SandboxID+"-") || item.Metadata["test_id"] == testTag {
				found = true
				if item.State != e2b.SandboxStateRunning {
					t.Fatalf("ListV1 item State = %q, want %q", item.State, e2b.SandboxStateRunning)
				}
				t.Logf("ListV1 matched sandbox: id=%s state=%q metadata=%v", item.SandboxID, item.State, item.Metadata)
			}
		}
		if !found {
			t.Fatalf("ListV1 with metadata test_id=%q did not find sandbox %s (got %d items)", testTag, sbx.ID, len(items))
		}

		v2Items, v2Err := client.ListAll(ctx, e2b.SandboxListOptions{
			Metadata: map[string]string{"test_id": testTag},
		})
		if v2Err != nil {
			var sdkErr e2b.Error
			if !errors.As(v2Err, &sdkErr) {
				t.Fatalf("client.ListAll (v2) error must implement e2b.Error, got %T: %v", v2Err, v2Err)
			}
			t.Logf("client.ListAll (GET /v2/sandboxes) failed fast on pre-v2 server: %T: %v", v2Err, v2Err)
		} else {
			t.Logf("client.ListAll (GET /v2/sandboxes) succeeded: matched %d items", len(v2Items))
		}
	})

	t.Run("ControlPlane_Timeouts", func(t *testing.T) {
		if err := client.Health(ctx); err != nil {
			t.Fatalf("client.Health: %T: %v", err, err)
		}
		if err := sbx.SetTimeout(ctx, 240*time.Second); err != nil {
			t.Fatalf("sbx.SetTimeout: %T: %v", err, err)
		}
		if err := sbx.RefreshTimeout(ctx, 270*time.Second); err != nil {
			t.Fatalf("sbx.RefreshTimeout: %T: %v", err, err)
		}
		if err := sbx.RefreshTimeoutV1(ctx, 300*time.Second); err != nil {
			t.Fatalf("sbx.RefreshTimeoutV1: %T: %v", err, err)
		}
	})

	t.Run("ControlPlane_GetLogsV1AndMetrics", func(t *testing.T) {
		logs, logsErr := sbx.GetLogsV1(ctx)
		if logsErr != nil {
			var sdkErr e2b.Error
			if !errors.As(logsErr, &sdkErr) {
				t.Fatalf("GetLogsV1 error must implement e2b.Error, got %T: %v", logsErr, logsErr)
			}
			t.Fatalf("sbx.GetLogsV1 failed: %T: %v", logsErr, logsErr)
		}
		t.Logf("GetLogsV1: %d plain-text lines, %d structured entries", len(logs.Logs), len(logs.LogEntries))

		if semverAtLeast(sbx.EnvdVersion, "0.1.5") {
			metrics, mErr := sbx.GetMetrics(ctx)
			if mErr != nil {
				var sdkErr e2b.Error
				if !errors.As(mErr, &sdkErr) {
					t.Fatalf("GetMetrics error must implement e2b.Error, got %T: %v", mErr, mErr)
				}
				t.Logf("GetMetrics returned %T: %v", mErr, mErr)
			} else {
				t.Logf("GetMetrics returned %d samples", len(metrics))
			}
		} else {
			_, mErr := sbx.GetMetrics(ctx)
			var tplErr *e2b.TemplateError
			if !errors.As(mErr, &tplErr) {
				t.Fatalf("expected *e2b.TemplateError for GetMetrics on envd %q (< 0.1.5), got %T: %v", sbx.EnvdVersion, mErr, mErr)
			}
			t.Logf("GetMetrics gated cleanly with *TemplateError on envd %q", sbx.EnvdVersion)
		}
	})

	t.Run("DataPlane_Commands", func(t *testing.T) {
		h, err := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
			Args: []string{"-c", `printf "hello-%s-%s\n" "$E2B_LEGACY_SUITE" "$CALL_VAR"`},
			Envs: map[string]string{"CALL_VAR": "ok"},
		})
		if err != nil {
			t.Fatalf("Commands.Run echo: %T: %v", err, err)
		}
		res, err := h.Wait(ctx)
		if err != nil {
			t.Fatalf("CommandHandle.Wait: %T: %v", err, err)
		}
		if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello-1-ok") {
			t.Fatalf("unexpected echo output: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
		}

		hRoot, err := sbx.Commands.Run(ctx, "whoami", e2b.RunOptions{User: "root"})
		if err != nil {
			t.Fatalf("Commands.Run(whoami, root): %T: %v", err, err)
		}
		resRoot, err := hRoot.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait(whoami): %T: %v", err, err)
		}
		if strings.TrimSpace(resRoot.Stdout) != "root" {
			t.Fatalf("expected whoami=root, got %q", resRoot.Stdout)
		}

		_, exitErr := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
			Args: []string{"-c", "echo err-msg >&2; exit 7"},
		})
		var cee *e2b.CommandExitError
		if !errors.As(exitErr, &cee) || cee.Result.ExitCode != 7 {
			t.Fatalf("expected *CommandExitError(exit=7), got %T: %v", exitErr, exitErr)
		}

		// Stdin piping via Start + Wait (note: on envd < 0.5.2 CloseStdin is not supported,
		// so test line-terminated read rather than EOF-terminated cat!).
		hStdin, err := sbx.Commands.Start(ctx, "sh", e2b.RunOptions{
			Args:  []string{"-c", "read line && echo got:$line"},
			Stdin: strings.NewReader("stdin-line\n"),
		})
		if err != nil {
			t.Fatalf("Commands.Start stdin: %T: %v", err, err)
		}
		resStdin, err := hStdin.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait stdin: %T: %v", err, err)
		}
		if !strings.Contains(resStdin.Stdout, "got:stdin-line") {
			t.Fatalf("expected got:stdin-line, got %q (stderr=%q)", resStdin.Stdout, resStdin.Stderr)
		}

		bg, err := sbx.Commands.Start(ctx, "sh", e2b.RunOptions{
			Args: []string{"-c", "sleep 60"},
		})
		if err != nil {
			t.Fatalf("Commands.Start sleep: %T: %v", err, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for bg.PID() == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if bg.PID() == 0 {
			t.Fatal("background command never received PID")
		}

		procs, err := sbx.Commands.List(ctx)
		if err != nil {
			t.Fatalf("Commands.List: %T: %v", err, err)
		}
		foundPID := false
		for _, p := range procs {
			if p.PID == bg.PID() {
				foundPID = true
				break
			}
		}
		if !foundPID {
			t.Fatalf("PID %d not found in Commands.List (%d processes)", bg.PID(), len(procs))
		}

		killed, err := sbx.Commands.Kill(ctx, bg.PID())
		if err != nil {
			t.Fatalf("Commands.Kill(%d): %T: %v", bg.PID(), err, err)
		}
		if !killed {
			t.Fatalf("expected Commands.Kill(%d) == true", bg.PID())
		}
		_, _ = bg.Wait(ctx)

		killedMissing, err := sbx.Commands.Kill(ctx, 999999)
		if err != nil {
			t.Fatalf("Commands.Kill(non-existent PID): expected nil err, got %T: %v", err, err)
		}
		if killedMissing {
			t.Fatal("Commands.Kill(non-existent PID): expected false, got true")
		}
	})

	t.Run("DataPlane_Pty", func(t *testing.T) {
		var mu sync.Mutex
		var out bytes.Buffer
		ptyHandle, err := sbx.Pty.Create(ctx, e2b.PtyOptions{
			Cmd:  "/bin/sh",
			Args: []string{"-i"},
			Cols: 80,
			Rows: 24,
			OnData: func(b []byte) {
				mu.Lock()
				out.Write(b)
				mu.Unlock()
			},
		})
		if err != nil {
			t.Fatalf("Pty.Create: %T: %v", err, err)
		}

		deadline := time.Now().Add(10 * time.Second)
		for ptyHandle.PID() == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if ptyHandle.PID() == 0 {
			t.Fatal("Pty never received PID")
		}

		if err := sbx.Pty.Resize(ctx, ptyHandle.PID(), 120, 40); err != nil {
			t.Fatalf("Pty.Resize: %T: %v", err, err)
		}

		if err := sbx.Pty.SendInput(ctx, ptyHandle.PID(), []byte("echo pty-selfhosted-ok\n")); err != nil {
			t.Fatalf("Pty.SendInput: %T: %v", err, err)
		}

		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := out.String()
			mu.Unlock()
			if strings.Contains(got, "pty-selfhosted-ok") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		mu.Lock()
		finalOut := out.String()
		mu.Unlock()
		if !strings.Contains(finalOut, "pty-selfhosted-ok") {
			t.Fatalf("PTY output missing expected marker: %q", finalOut)
		}

		killed, err := sbx.Pty.Kill(ctx, ptyHandle.PID())
		if err != nil {
			t.Fatalf("Pty.Kill: %T: %v", err, err)
		}
		if !killed {
			t.Logf("Pty.Kill reported false (process may have exited already)")
		}
		_, _ = ptyHandle.Wait(ctx)

		killedMissing, err := sbx.Pty.Kill(ctx, 999999)
		if err != nil || killedMissing {
			t.Fatalf("Pty.Kill(non-existent) = (%v, %v), want (false, nil)", killedMissing, err)
		}
	})

	t.Run("DataPlane_Filesystem", func(t *testing.T) {
		baseDir := "/tmp/e2b-legacy-fs"
		if err := sbx.Files.MakeDir(ctx, baseDir+"/sub", e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.MakeDir: %T: %v", err, err)
		}

		txtPath := baseDir + "/hello.txt"
		if _, err := sbx.Files.WriteString(ctx, txtPath, "hello-legacy-fs", e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.WriteString: %T: %v", err, err)
		}
		gotStr, err := sbx.Files.ReadString(ctx, txtPath, e2b.FsOptions{})
		if err != nil {
			t.Fatalf("Files.ReadString: %T: %v", err, err)
		}
		if gotStr != "hello-legacy-fs" {
			t.Fatalf("ReadString = %q, want %q", gotStr, "hello-legacy-fs")
		}

		binPayload := []byte{0x00, 0x01, 0x02, 0x7f, 0x80, 0xfe, 0xff}
		binPath := baseDir + "/binary.dat"
		if _, err := sbx.Files.WriteBytes(ctx, binPath, binPayload, e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.WriteBytes: %T: %v", err, err)
		}
		gotBytes, err := sbx.Files.ReadBytes(ctx, binPath, e2b.FsOptions{})
		if err != nil {
			t.Fatalf("Files.ReadBytes: %T: %v", err, err)
		}
		if !bytes.Equal(gotBytes, binPayload) {
			t.Fatalf("ReadBytes mismatch: got %x, want %x", gotBytes, binPayload)
		}

		streamPath := baseDir + "/stream.txt"
		if _, err := sbx.Files.Write(ctx, streamPath, strings.NewReader("stream-payload"), e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.Write: %T: %v", err, err)
		}
		rc, err := sbx.Files.ReadStream(ctx, streamPath, e2b.FsOptions{})
		if err != nil {
			t.Fatalf("Files.ReadStream: %T: %v", err, err)
		}
		streamBytes, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			t.Fatalf("ReadAll(ReadStream): %v", readErr)
		}
		if string(streamBytes) != "stream-payload" {
			t.Fatalf("ReadStream = %q, want %q", string(streamBytes), "stream-payload")
		}

		exists, err := sbx.Files.Exists(ctx, txtPath, e2b.FsOptions{})
		if err != nil || !exists {
			t.Fatalf("Exists(%s) = (%v, %v), want (true, nil)", txtPath, exists, err)
		}
		st, err := sbx.Files.Stat(ctx, txtPath, e2b.FsOptions{})
		if err != nil {
			t.Fatalf("Stat(%s): %T: %v", txtPath, err, err)
		}
		if st.Type != e2b.EntryTypeFile {
			t.Fatalf("Stat(%s).Type = %v, want EntryTypeFile", txtPath, st.Type)
		}

		missingPath := baseDir + "/does-not-exist-deadbeef"
		missingExists, err := sbx.Files.Exists(ctx, missingPath, e2b.FsOptions{})
		if err != nil || missingExists {
			t.Fatalf("Exists(missing) = (%v, %v), want (false, nil)", missingExists, err)
		}
		_, statMissingErr := sbx.Files.Stat(ctx, missingPath, e2b.FsOptions{})
		var fnf *e2b.FileNotFoundError
		if !errors.As(statMissingErr, &fnf) {
			t.Fatalf("Stat(missing) expected *FileNotFoundError, got %T: %v", statMissingErr, statMissingErr)
		}

		entries, err := sbx.Files.List(ctx, baseDir, e2b.FsOptions{})
		if err != nil {
			t.Fatalf("Files.List: %T: %v", err, err)
		}
		if len(entries) < 3 {
			t.Fatalf("expected >= 3 entries in %s, got %d", baseDir, len(entries))
		}

		movedPath := baseDir + "/sub/moved.txt"
		if err := sbx.Files.Move(ctx, txtPath, movedPath, e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.Move: %T: %v", err, err)
		}
		if ok, _ := sbx.Files.Exists(ctx, movedPath, e2b.FsOptions{}); !ok {
			t.Fatal("moved destination file does not exist")
		}
		if err := sbx.Files.Remove(ctx, movedPath, e2b.FsOptions{}); err != nil {
			t.Fatalf("Files.Remove: %T: %v", err, err)
		}
		if ok, _ := sbx.Files.Exists(ctx, movedPath, e2b.FsOptions{}); ok {
			t.Fatal("removed file still exists")
		}

		watchDir := baseDir + "/watch-nonrec"
		if err := sbx.Files.MakeDir(ctx, watchDir, e2b.FsOptions{}); err != nil {
			t.Fatalf("MakeDir(watchDir): %v", err)
		}
		wh, err := sbx.Files.Watch(ctx, watchDir, false)
		if err != nil {
			t.Fatalf("Files.Watch(non-recursive): %T: %v", err, err)
		}
		time.Sleep(400 * time.Millisecond)
		if _, err := sbx.Files.WriteString(ctx, watchDir+"/ev.txt", "trigger", e2b.FsOptions{}); err != nil {
			_ = wh.Stop(ctx)
			t.Fatalf("WriteString in watched dir: %v", err)
		}
		select {
		case ev, ok := <-wh.Events():
			if !ok {
				t.Fatalf("non-recursive watch closed without event (err=%v)", wh.Err())
			}
			t.Logf("non-recursive watch received event: name=%q type=%v", ev.Name, ev.Type)
		case <-time.After(10 * time.Second):
			_ = wh.Stop(ctx)
			t.Fatal("timed out waiting for non-recursive watch event")
		}
		_ = wh.Stop(ctx)

		recDir := baseDir + "/watch-rec"
		if err := sbx.Files.MakeDir(ctx, recDir+"/nested", e2b.FsOptions{}); err != nil {
			t.Fatalf("MakeDir(recDir): %v", err)
		}
		if semverAtLeast(sbx.EnvdVersion, "0.1.4") {
			rwh, rErr := sbx.Files.Watch(ctx, recDir, true)
			if rErr != nil {
				t.Fatalf("Files.Watch(recursive) on envd %q: %T: %v", sbx.EnvdVersion, rErr, rErr)
			}
			time.Sleep(400 * time.Millisecond)
			_, _ = sbx.Files.WriteString(ctx, recDir+"/nested/deep.txt", "deep", e2b.FsOptions{})
			select {
			case ev, ok := <-rwh.Events():
				if !ok {
					t.Fatalf("recursive watch closed without event (err=%v)", rwh.Err())
				}
				t.Logf("recursive watch received event: name=%q type=%v", ev.Name, ev.Type)
			case <-time.After(10 * time.Second):
				_ = rwh.Stop(ctx)
				t.Fatal("timed out waiting for recursive watch event")
			}
			_ = rwh.Stop(ctx)
		} else {
			_, rErr := sbx.Files.Watch(ctx, recDir, true)
			var tplErr *e2b.TemplateError
			if !errors.As(rErr, &tplErr) {
				t.Fatalf("expected *TemplateError for recursive Watch on envd %q (< 0.1.4), got %T: %v", sbx.EnvdVersion, rErr, rErr)
			}
			t.Logf("recursive Watch gated cleanly with *TemplateError on envd %q", sbx.EnvdVersion)
		}
	})

	t.Run("DataPlane_Git", func(t *testing.T) {
		if _, checkErr := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{Args: []string{"-c", "command -v git"}}); checkErr != nil {
			t.Logf("git binary not preinstalled in template %q; attempting lightweight package install as root", selectedTemplate)
			installCmd := "if command -v apt-get >/dev/null 2>&1; then apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git; elif command -v apk >/dev/null 2>&1; then apk add --no-cache git; elif command -v yum >/dev/null 2>&1; then yum install -y -q git; else exit 1; fi"
			if _, instErr := sbx.Commands.Run(ctx, "sh", e2b.RunOptions{
				Args:      []string{"-c", installCmd},
				User:      "root",
				TimeoutMs: 90_000,
			}); instErr != nil {
				t.Skipf("git is not installed in template %q and package install unavailable (%v); skipping Git helper subtest", selectedTemplate, instErr)
			}
		}

		repoDir := "/tmp/test-repo"
		_ = sbx.Files.Remove(ctx, repoDir, e2b.FsOptions{})

		if _, err := sbx.Git.Init(ctx, repoDir, e2b.GitInitOptions{InitialBranch: "main"}); err != nil {
			// Older git binaries (< 2.28) may not support --initial-branch; retry plain git init.
			t.Logf("Git.Init(InitialBranch=main) returned %v; retrying plain Git.Init", err)
			if _, err2 := sbx.Git.Init(ctx, repoDir, e2b.GitInitOptions{}); err2 != nil {
				t.Fatalf("Git.Init: %T: %v", err2, err2)
			}
		}

		if _, err := sbx.Git.ConfigureUser(ctx, "SelfHostedBot", "bot@example.com", e2b.GitConfigOptions{
			Scope:      e2b.GitConfigLocal,
			GitOptions: e2b.GitOptions{RepoPath: repoDir},
		}); err != nil {
			t.Fatalf("Git.ConfigureUser: %T: %v", err, err)
		}

		if _, err := sbx.Files.WriteString(ctx, repoDir+"/README.md", "# Legacy Self-Hosted Test\n", e2b.FsOptions{}); err != nil {
			t.Fatalf("WriteString README.md: %v", err)
		}

		stBefore, err := sbx.Git.Status(ctx, e2b.GitOptions{RepoPath: repoDir})
		if err != nil {
			t.Fatalf("Git.Status(before): %T: %v", err, err)
		}
		if len(stBefore.Untracked) == 0 {
			t.Fatalf("expected README.md in Untracked, got %+v", stBefore)
		}

		if _, err := sbx.Git.Add(ctx, ".", e2b.GitOptions{RepoPath: repoDir}); err != nil {
			t.Fatalf("Git.Add: %T: %v", err, err)
		}
		if _, err := sbx.Git.Commit(ctx, "initial commit", e2b.GitOptions{RepoPath: repoDir}); err != nil {
			t.Fatalf("Git.Commit: %T: %v", err, err)
		}

		stAfter, err := sbx.Git.Status(ctx, e2b.GitOptions{RepoPath: repoDir})
		if err != nil {
			t.Fatalf("Git.Status(after): %T: %v", err, err)
		}
		if len(stAfter.Modified)+len(stAfter.Staged)+len(stAfter.Untracked) != 0 {
			t.Fatalf("expected clean git status after commit, got %+v", stAfter)
		}

		if _, err := sbx.Git.CreateBranch(ctx, "feature-legacy", e2b.GitOptions{RepoPath: repoDir}); err != nil {
			t.Fatalf("Git.CreateBranch: %T: %v", err, err)
		}
		if _, err := sbx.Git.CheckoutBranch(ctx, "feature-legacy", e2b.GitOptions{RepoPath: repoDir}); err != nil {
			t.Fatalf("Git.CheckoutBranch: %T: %v", err, err)
		}
		branches, err := sbx.Git.Branches(ctx, e2b.GitOptions{RepoPath: repoDir})
		if err != nil {
			t.Fatalf("Git.Branches: %T: %v", err, err)
		}
		if branches.Current != "feature-legacy" {
			t.Fatalf("expected current branch feature-legacy, got %q (local=%v)", branches.Current, branches.Local)
		}
	})

	t.Run("ControlPlane_PauseAndResume", func(t *testing.T) {
		if _, runningResumeErr := client.Resume(ctx, sbx.ID); runningResumeErr != nil {
			var sdkErr e2b.Error
			if !errors.As(runningResumeErr, &sdkErr) {
				t.Fatalf("client.Resume on running sandbox must return e2b.Error, got %T: %v", runningResumeErr, runningResumeErr)
			}
			t.Logf("client.Resume on running sandbox returned expected %T: %v", runningResumeErr, runningResumeErr)
		}

		paused, pauseErr := sbx.Pause(ctx)
		if pauseErr != nil {
			var sdkErr e2b.Error
			if !errors.As(pauseErr, &sdkErr) {
				t.Fatalf("Pause error must implement e2b.Error, got %T: %v", pauseErr, pauseErr)
			}
			t.Logf("Pause not supported/enabled on this legacy cluster (%T: %v); skipping Resume", pauseErr, pauseErr)
			return
		}
		t.Logf("Pause succeeded (paused=%v); testing sbx.Resume", paused)

		if resumeErr := sbx.Resume(ctx, e2b.ResumeOptions{Timeout: 2 * time.Minute}); resumeErr != nil {
			var sdkErr e2b.Error
			if !errors.As(resumeErr, &sdkErr) {
				t.Fatalf("sbx.Resume error must implement e2b.Error, got %T: %v", resumeErr, resumeErr)
			}
			t.Fatalf("Pause succeeded on cluster, so sbx.Resume should succeed too, got %T: %v", resumeErr, resumeErr)
		}
		h, err := sbx.Commands.Run(ctx, "echo", e2b.RunOptions{Args: []string{"resumed-ok"}})
		if err != nil {
			t.Fatalf("sbx.Commands.Run after sbx.Resume: %T: %v", err, err)
		}
		res, err := h.Wait(ctx)
		if err != nil || !strings.Contains(res.Stdout, "resumed-ok") {
			t.Fatalf("resumed command failed: err=%v stdout=%q", err, res.Stdout)
		}
		t.Logf("sbx.Resume succeeded and executed command in resumed sandbox (id=%s)", sbx.ID)
	})

	killed, killErr := client.Kill(ctx, sbx.ID)
	if killErr != nil {
		t.Fatalf("client.Kill(%s): %T: %v", sbx.ID, killErr, killErr)
	}
	t.Logf("explicit client.Kill(%s) -> killed=%v", sbx.ID, killed)

	killedAgain, killAgainErr := client.Kill(ctx, sbx.ID)
	if killAgainErr != nil || killedAgain {
		t.Fatalf("second client.Kill(%s) = (%v, %v), want (false, nil)", sbx.ID, killedAgain, killAgainErr)
	}
}
