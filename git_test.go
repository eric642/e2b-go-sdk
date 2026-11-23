package e2b

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

func TestParseGitStatus(t *testing.T) {
	out := " M file1\nA  file2\n?? file3\nMM file4\n"
	s := parseGitStatus(out)
	if len(s.Modified) == 0 || s.Modified[0] != "file1" {
		t.Fatalf("unexpected modified: %+v", s.Modified)
	}
	if !slices.Contains(s.Staged, "file2") {
		t.Fatalf("file2 should be staged: %+v", s.Staged)
	}
	if len(s.Untracked) == 0 || s.Untracked[0] != "file3" {
		t.Fatalf("unexpected untracked: %+v", s.Untracked)
	}
	if !slices.Contains(s.Staged, "file4") || !slices.Contains(s.Modified, "file4") {
		t.Fatalf("file4 should be both staged and modified; staged=%v modified=%v", s.Staged, s.Modified)
	}
}

func TestParseGitStatusEmpty(t *testing.T) {
	s := parseGitStatus("")
	if len(s.Modified)+len(s.Staged)+len(s.Untracked) != 0 {
		t.Fatalf("empty input should produce empty status, got %+v", s)
	}
}

func TestParseGitStatusShortLinesIgnored(t *testing.T) {
	s := parseGitStatus("\n \nMM\n")
	if len(s.Modified)+len(s.Staged) != 0 {
		t.Fatalf("short lines should be skipped, got %+v", s)
	}
}

func TestParseGitStatusConflict(t *testing.T) {
	s := parseGitStatus("UU merge.go\n")
	if !slices.Contains(s.Staged, "merge.go") || !slices.Contains(s.Modified, "merge.go") {
		t.Fatalf("conflict entry missing from both lists: %+v", s)
	}
}

func TestParseGitBranches(t *testing.T) {
	out := "* main\n  dev\n  remotes/origin/main\n"
	b := parseGitBranches(out)
	if b.Current != "main" {
		t.Fatalf("want current main, got %s", b.Current)
	}
	if len(b.Local) != 2 {
		t.Fatalf("want 2 local, got %v", b.Local)
	}
	if len(b.Remote) != 1 {
		t.Fatalf("want 1 remote, got %v", b.Remote)
	}
}

func TestParseGitBranchesDetachedHEAD(t *testing.T) {
	out := "* (HEAD detached at abc123)\n  main\n"
	b := parseGitBranches(out)
	if b.Current != "(HEAD detached at abc123)" {
		t.Fatalf("detached HEAD not captured: %q", b.Current)
	}
}

func TestParseGitBranchesEmpty(t *testing.T) {
	b := parseGitBranches("\n  \n")
	if b.Current != "" || len(b.Local)+len(b.Remote) != 0 {
		t.Fatalf("expected empty, got %+v", b)
	}
}

func TestStripCredsAndDeriveRepoDir(t *testing.T) {
	if got := stripCreds("https://alice:secret@github.com/org/my-repo.git"); got != "https://github.com/org/my-repo.git" {
		t.Fatalf("stripCreds = %q", got)
	}
	if got := deriveRepoDirFromURL("https://github.com/org/my-repo.git/"); got != "my-repo" {
		t.Fatalf("deriveRepoDirFromURL = %q, want my-repo", got)
	}
}

func TestClassifyGitErrAuth(t *testing.T) {
	exit := &CommandExitError{Result: CommandResult{Stderr: "fatal: Authentication failed for 'https://...'", ExitCode: 128}}
	err := classifyGitErr(exit)
	if _, ok := errors.AsType[*GitAuthError](err); !ok {
		t.Fatalf("want *GitAuthError, got %T", err)
	}
}

func TestClassifyGitErrUpstream(t *testing.T) {
	exit := &CommandExitError{Result: CommandResult{Stderr: "fatal: no upstream configured", ExitCode: 128}}
	err := classifyGitErr(exit)
	if _, ok := errors.AsType[*GitUpstreamError](err); !ok {
		t.Fatalf("want *GitUpstreamError, got %T", err)
	}
}

func TestClassifyGitErrUnknownReturnsOriginal(t *testing.T) {
	exit := &CommandExitError{Result: CommandResult{Stderr: "unrelated", ExitCode: 1}}
	err := classifyGitErr(exit)
	if !errors.Is(err, exit) {
		t.Fatalf("unknown error should pass through unchanged, got %T (%v)", err, err)
	}
}

type recordedGitCall struct {
	cmd       string
	args      []string
	cwd       string
	envs      map[string]string
	auth      string
	timeoutMs string
	stdin     string
}

type mockGitProcessHandler struct {
	processconnect.UnimplementedProcessHandler
	mu        sync.Mutex
	nextPID   uint32
	calls     []recordedGitCall
	stdin     map[uint32][]byte
	closed    map[uint32]bool
	step      func(idx int, call recordedGitCall) (stdout, stderr string, exitCode int32)
	streamErr func(idx int, call recordedGitCall) error
}

func newGitMockSandbox(t *testing.T, step func(idx int, call recordedGitCall) (stdout, stderr string, exitCode int32)) (*Sandbox, *mockGitProcessHandler) {
	t.Helper()
	svc := &mockGitProcessHandler{
		stdin:  make(map[uint32][]byte),
		closed: make(map[uint32]bool),
		step:   step,
	}
	_, procHandler := processconnect.NewProcessHandler(svc)
	srv := httptest.NewServer(procHandler)
	t.Cleanup(srv.Close)

	cfg := Config{Domain: "example.com", SandboxURL: srv.URL}.Resolve()
	envdCli, err := transport.NewEnvdClients(srv.URL, srv.Client(), transport.EnvdAuth{
		Token:     "envd-tok",
		SandboxID: "sbx-git",
		Headers:   cfg.envdHeaders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sbx := &Sandbox{
		ID:              "sbx-git",
		Domain:          "example.com",
		EnvdVersion:     "0.6.13",
		EnvdAccessToken: "envd-tok",
		cfg:             cfg,
		envd:            envdCli,
		httpCli:         srv.Client(),
	}
	sbx.Commands = &Commands{sbx: sbx}
	sbx.Git = &Git{cmds: sbx.Commands}
	return sbx, svc
}

func (m *mockGitProcessHandler) Start(ctx context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
	m.mu.Lock()
	m.nextPID++
	pid := m.nextPID
	idx := len(m.calls)
	p := req.Msg.GetProcess()
	call := recordedGitCall{
		cmd:       p.GetCmd(),
		args:      append([]string(nil), p.GetArgs()...),
		cwd:       p.GetCwd(),
		envs:      p.GetEnvs(),
		auth:      req.Header().Get("Authorization"),
		timeoutMs: req.Header().Get("Connect-Timeout-Ms"),
	}
	m.calls = append(m.calls, call)
	wantStdin := req.Msg.GetStdin()
	streamErrFn := m.streamErr
	m.mu.Unlock()

	if streamErrFn != nil {
		if err := streamErrFn(idx, call); err != nil {
			return err
		}
	}

	if err := stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: pid}},
		},
	}); err != nil {
		return err
	}

	if wantStdin {
		for {
			m.mu.Lock()
			done := m.closed[pid]
			got := string(m.stdin[pid])
			if done {
				m.calls[idx].stdin = got
			}
			m.mu.Unlock()
			if done || ctx.Err() != nil {
				break
			}
		}
	}

	var stdout, stderr string
	var exitCode int32
	if m.step != nil {
		m.mu.Lock()
		c := m.calls[idx]
		m.mu.Unlock()
		stdout, stderr, exitCode = m.step(idx, c)
	}
	if stdout != "" {
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{
					Data: &procpb.ProcessEvent_DataEvent{
						Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte(stdout)},
					},
				},
			},
		})
	}
	if stderr != "" {
		_ = stream.Send(&procpb.StartResponse{
			Event: &procpb.ProcessEvent{
				Event: &procpb.ProcessEvent_Data{
					Data: &procpb.ProcessEvent_DataEvent{
						Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte(stderr)},
					},
				},
			},
		})
	}
	return stream.Send(&procpb.StartResponse{
		Event: &procpb.ProcessEvent{
			Event: &procpb.ProcessEvent_End{
				End: &procpb.ProcessEvent_EndEvent{ExitCode: exitCode, Exited: true},
			},
		},
	})
}

func (m *mockGitProcessHandler) SendInput(_ context.Context, req *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
	pid := req.Msg.GetProcess().GetPid()
	m.mu.Lock()
	m.stdin[pid] = append(m.stdin[pid], req.Msg.GetInput().GetStdin()...)
	m.mu.Unlock()
	return connect.NewResponse(&procpb.SendInputResponse{}), nil
}

func (m *mockGitProcessHandler) CloseStdin(_ context.Context, req *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error) {
	pid := req.Msg.GetProcess().GetPid()
	m.mu.Lock()
	m.closed[pid] = true
	m.mu.Unlock()
	return connect.NewResponse(&procpb.CloseStdinResponse{}), nil
}

func TestGitCloneScrubsCredentialsByDefault(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	ctx := context.Background()

	_, err := sbx.Git.Clone(ctx, "https://github.com/org/private-repo.git", GitCloneOptions{
		GitOptions: GitOptions{
			RepoPath:  "/workspace/private-repo",
			User:      "alice",
			Cwd:       "/workspace",
			Envs:      map[string]string{"GIT_SSL_NO_VERIFY": "1"},
			TimeoutMs: 15000,
		},
		Branch:   "main",
		Depth:    1,
		Username: "x-access-token",
		Password: "ghs_secret123",
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("want 2 git invocations (clone + remote set-url), got %d: %+v", len(calls), calls)
	}
	wantCloneArgs := []string{
		"clone", "--depth", "1", "--branch", "main", "--",
		"https://x-access-token:ghs_secret123@github.com/org/private-repo.git",
		"/workspace/private-repo",
	}
	if !slices.Equal(calls[0].args, wantCloneArgs) {
		t.Fatalf("clone args = %v, want %v", calls[0].args, wantCloneArgs)
	}
	wantScrubArgs := []string{
		"-C", "/workspace/private-repo",
		"remote", "set-url", "--", "origin", "https://github.com/org/private-repo.git",
	}
	if !slices.Equal(calls[1].args, wantScrubArgs) {
		t.Fatalf("scrub args = %v, want %v", calls[1].args, wantScrubArgs)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"))
	for i, c := range calls {
		if c.auth != wantAuth {
			t.Errorf("call[%d] auth = %q, want %q", i, c.auth, wantAuth)
		}
		if c.cwd != "/workspace" {
			t.Errorf("call[%d] cwd = %q, want /workspace", i, c.cwd)
		}
		if c.envs["GIT_TERMINAL_PROMPT"] != "0" || c.envs["GIT_SSL_NO_VERIFY"] != "1" {
			t.Errorf("call[%d] envs = %v", i, c.envs)
		}
	}
	if calls[0].timeoutMs != "15000" {
		t.Errorf("call[0] timeoutMs = %q, want 15000", calls[0].timeoutMs)
	}
}

func TestGitCloneDerivesRepoDirAndDangerouslyStoreOption(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	ctx := context.Background()

	if _, err := sbx.Git.Clone(ctx, "https://github.com/org/my-repo.git", GitCloneOptions{
		Username: "u",
		Password: "p",
	}); err != nil {
		t.Fatalf("Clone derived dir: %v", err)
	}
	if _, err := sbx.Git.Clone(ctx, "https://github.com/org/keep-creds.git", GitCloneOptions{
		Username:                    "u",
		Password:                    "p",
		DangerouslyStoreCredentials: true,
	}); err != nil {
		t.Fatalf("Clone store creds: %v", err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	if len(calls) != 3 {
		t.Fatalf("want 3 total calls, got %d: %+v", len(calls), calls)
	}
	wantDerivedScrub := []string{"-C", "my-repo", "remote", "set-url", "--", "origin", "https://github.com/org/my-repo.git"}
	if !slices.Equal(calls[1].args, wantDerivedScrub) {
		t.Fatalf("derived scrub args = %v, want %v", calls[1].args, wantDerivedScrub)
	}
}

func TestGitPushAndPullScrubTemporaryCredsEvenOnError(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
		if slices.Contains(call.args, "remote") && len(call.args) == 3 {
			return "origin\n", "", 0
		}
		if slices.Contains(call.args, "get-url") {
			return "https://github.com/org/repo.git\n", "", 0
		}
		if slices.Contains(call.args, "push") {
			return "", "fatal: Authentication failed for 'https://github.com/org/repo.git/'", 128
		}
		return "", "", 0
	})

	_, err := sbx.Git.Push(context.Background(), GitOptions{
		RepoPath:    "/repo",
		Branch:      "main",
		SetUpstream: true,
		Username:    "u",
		Password:    "tok",
	})
	if _, ok := errors.AsType[*GitAuthError](err); !ok {
		t.Fatalf("want *GitAuthError on failed push, got %T: %v", err, err)
	}

	mock.mu.Lock()
	pushCalls := append([]recordedGitCall(nil), mock.calls...)
	mock.calls = nil
	mock.mu.Unlock()

	if len(pushCalls) != 5 {
		t.Fatalf("want 5 git calls for push with temp creds, got %d: %+v", len(pushCalls), pushCalls)
	}
	if !slices.Equal(pushCalls[2].args, []string{"-C", "/repo", "remote", "set-url", "--", "origin", "https://u:tok@github.com/org/repo.git"}) {
		t.Fatalf("inject creds args = %v", pushCalls[2].args)
	}
	if !slices.Equal(pushCalls[3].args, []string{"-C", "/repo", "push", "--set-upstream", "--", "origin", "main"}) {
		t.Fatalf("push args = %v", pushCalls[3].args)
	}
	if !slices.Equal(pushCalls[4].args, []string{"-C", "/repo", "remote", "set-url", "--", "origin", "https://github.com/org/repo.git"}) {
		t.Fatalf("restore clean URL args = %v", pushCalls[4].args)
	}

	if _, err := sbx.Git.Pull(context.Background(), GitOptions{
		RepoPath: "/repo",
		Remote:   "upstream",
		Branch:   "main",
		Username: "u",
		Password: "tok",
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	mock.mu.Lock()
	pullCalls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()
	if len(pullCalls) != 4 {
		t.Fatalf("want 4 git calls for pull with explicit remote, got %d: %+v", len(pullCalls), pullCalls)
	}
	if !slices.Equal(pullCalls[3].args, []string{"-C", "/repo", "remote", "set-url", "--", "upstream", "https://github.com/org/repo.git"}) {
		t.Fatalf("pull restore args = %v", pullCalls[3].args)
	}
}

func TestGitParityMethodsAndValidation(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
		if slices.Equal(call.args, []string{"-C", "/repo", "remote", "add", "--", "origin", "https://github.com/org/new.git"}) {
			return "", "error: remote origin already exists.", 3
		}
		if slices.Contains(call.args, "--get") && slices.Contains(call.args, "missing.key") {
			return "", "", 1
		}
		if slices.Contains(call.args, "--get") && slices.Contains(call.args, "pull.rebase") {
			return "true\n", "", 0
		}
		return "", "", 0
	})
	ctx := context.Background()

	if _, err := sbx.Git.Init(ctx, "/repo", GitInitOptions{Bare: true, InitialBranch: "main"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := sbx.Git.RemoteAdd(ctx, "origin", "https://github.com/org/new.git", GitRemoteAddOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Overwrite:  true,
		Fetch:      true,
	}); err != nil {
		t.Fatalf("RemoteAdd overwrite: %v", err)
	}
	if _, err := sbx.Git.DeleteBranchWithOptions(ctx, "topic", GitDeleteBranchOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Force:      false,
	}); err != nil {
		t.Fatalf("DeleteBranchWithOptions: %v", err)
	}
	if _, err := sbx.Git.Reset(ctx, GitResetOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Mode:       GitResetHard,
		Target:     "HEAD~1",
	}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := sbx.Git.Reset(ctx, GitResetOptions{Mode: "bogus"}); err == nil {
		t.Fatal("expected InvalidArgumentError for invalid reset mode")
	}
	stagedTrue := true
	if _, err := sbx.Git.Restore(ctx, GitRestoreOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Paths:      []string{"a.go"},
		Staged:     &stagedTrue,
		Source:     "HEAD",
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := sbx.Git.Restore(ctx, GitRestoreOptions{}); err == nil {
		t.Fatal("expected error for empty Restore paths")
	}
	if _, err := sbx.Git.SetConfig(ctx, "pull.rebase", "true", GitConfigOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Scope:      GitConfigLocal,
	}); err != nil {
		t.Fatalf("SetConfig local: %v", err)
	}
	if _, err := sbx.Git.SetConfig(ctx, "pull.rebase", "true", GitConfigOptions{Scope: GitConfigLocal}); err == nil {
		t.Fatal("expected error when Scope=local without RepoPath")
	}
	val, err := sbx.Git.GetConfig(ctx, "pull.rebase", GitConfigOptions{})
	if err != nil || val != "true" {
		t.Fatalf("GetConfig = %q, err = %v", val, err)
	}
	missing, err := sbx.Git.GetConfig(ctx, "missing.key", GitConfigOptions{})
	if err != nil || missing != "" {
		t.Fatalf("GetConfig missing key = %q, err = %v", missing, err)
	}
	if _, err := sbx.Git.ConfigureUser(ctx, "Dev", "dev@example.com", GitConfigOptions{}); err != nil {
		t.Fatalf("ConfigureUser: %v", err)
	}
	if _, err := sbx.Git.DangerouslyAuthenticate(ctx, GitDangerouslyAuthenticateOptions{
		Username: "bot",
		Password: "super-secret-password",
	}); err != nil {
		t.Fatalf("DangerouslyAuthenticate: %v", err)
	}
	if _, err := sbx.Git.DangerouslyAuthenticate(ctx, GitDangerouslyAuthenticateOptions{
		Username: "bot\ninjected",
		Password: "pw",
	}); err == nil {
		t.Fatal("expected newline injection in DangerouslyAuthenticate to be rejected")
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	if !slices.Equal(calls[0].args, []string{"init", "--initial-branch", "main", "--bare", "--", "/repo"}) {
		t.Fatalf("Init args = %v", calls[0].args)
	}
	last := calls[len(calls)-1]
	if !slices.Equal(last.args, []string{"credential", "approve"}) {
		t.Fatalf("DangerouslyAuthenticate last args = %v", last.args)
	}
	for _, c := range calls {
		for _, a := range c.args {
			if strings.Contains(a, "super-secret-password") {
				t.Fatalf("DangerouslyAuthenticate leaked password in argv: %v", c.args)
			}
		}
	}
	if !strings.Contains(last.stdin, "username=bot\npassword=super-secret-password\n\n") {
		t.Fatalf("DangerouslyAuthenticate stdin = %q", last.stdin)
	}
}

func TestGitAddWithOptions(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	ctx := context.Background()

	if _, err := sbx.Git.AddWithOptions(ctx, GitAddOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
	}); err != nil {
		t.Fatalf("AddWithOptions default: %v", err)
	}
	allFalse := false
	if _, err := sbx.Git.AddWithOptions(ctx, GitAddOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		All:        &allFalse,
	}); err != nil {
		t.Fatalf("AddWithOptions All=false: %v", err)
	}
	if _, err := sbx.Git.AddWithOptions(ctx, GitAddOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Files:      []string{"a.go", "b.go"},
	}); err != nil {
		t.Fatalf("AddWithOptions Files: %v", err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %d", len(calls))
	}
	if !slices.Equal(calls[0].args, []string{"-C", "/repo", "add", "-A"}) {
		t.Fatalf("call[0] args = %v", calls[0].args)
	}
	if !slices.Equal(calls[1].args, []string{"-C", "/repo", "add", "."}) {
		t.Fatalf("call[1] args = %v", calls[1].args)
	}
	if !slices.Equal(calls[2].args, []string{"-C", "/repo", "add", "--", "a.go", "b.go"}) {
		t.Fatalf("call[2] args = %v", calls[2].args)
	}
}

func TestParseGitStatusCRLFAndShortLines(t *testing.T) {
	out := " M file1\r\nA  file2\r\n?? file3\r\nMM file4\r\nMM \r\n   \r\n"
	s := parseGitStatus(out)
	if !slices.Equal(s.Modified, []string{"file1", "file4"}) {
		t.Fatalf("Modified = %q, want [file1 file4]", s.Modified)
	}
	if !slices.Equal(s.Staged, []string{"file2", "file4"}) {
		t.Fatalf("Staged = %q, want [file2 file4]", s.Staged)
	}
	if !slices.Equal(s.Untracked, []string{"file3"}) {
		t.Fatalf("Untracked = %q, want [file3]", s.Untracked)
	}
}

func TestClassifyGitErrParity(t *testing.T) {
	t.Run("permission denied in stderr", func(t *testing.T) {
		exit := &CommandExitError{Result: CommandResult{Stderr: "Permission denied (publickey).\n", ExitCode: 128}}
		err := classifyGitErr(exit)
		authErr, ok := errors.AsType[*GitAuthError](err)
		if !ok {
			t.Fatalf("want *GitAuthError, got %T (%v)", err, err)
		}
		if authErr.Message != "Permission denied (publickey)." {
			t.Fatalf("Message = %q", authErr.Message)
		}
	})

	t.Run("auth failure in stdout when stderr empty", func(t *testing.T) {
		exit := &CommandExitError{Result: CommandResult{Stdout: "fatal: Authentication failed for 'https://example.com/r.git'\n", ExitCode: 128}}
		err := classifyGitErr(exit)
		authErr, ok := errors.AsType[*GitAuthError](err)
		if !ok {
			t.Fatalf("want *GitAuthError, got %T (%v)", err, err)
		}
		if authErr.Message != "fatal: Authentication failed for 'https://example.com/r.git'" {
			t.Fatalf("Message = %q", authErr.Message)
		}
	})

	t.Run("upstream failure in stdout when stderr empty", func(t *testing.T) {
		exit := &CommandExitError{Result: CommandResult{Stdout: "fatal: The current branch main has no upstream branch.\n", ExitCode: 128}}
		err := classifyGitErr(exit)
		upErr, ok := errors.AsType[*GitUpstreamError](err)
		if !ok {
			t.Fatalf("want *GitUpstreamError, got %T (%v)", err, err)
		}
		if upErr.Message != "fatal: The current branch main has no upstream branch." {
			t.Fatalf("Message = %q", upErr.Message)
		}
	})

	t.Run("bare 403 does not trigger GitAuthError", func(t *testing.T) {
		for _, stderr := range []string{
			"error: commit 403abcd not found",
			"fatal: bad line 403 in config",
		} {
			exit := &CommandExitError{Result: CommandResult{Stderr: stderr, ExitCode: 1}}
			if got := classifyGitErr(exit); !errors.Is(got, exit) {
				t.Fatalf("stderr %q should not be misclassified, got %T (%v)", stderr, got, got)
			}
		}
	})
}

func TestGitClassifyRestrictedToRemoteOps(t *testing.T) {
	sbx, _ := newGitMockSandbox(t, func(_ int, _ recordedGitCall) (string, string, int32) {
		return "", "fatal: Permission denied", 128
	})
	ctx := context.Background()

	// Local operations must NEVER classify into *GitAuthError.
	localOps := []struct {
		name string
		run  func() error
	}{
		{"Add", func() error { _, err := sbx.Git.Add(ctx, ".", GitOptions{RepoPath: "/repo"}); return err }},
		{"Commit", func() error { _, err := sbx.Git.Commit(ctx, "msg", GitOptions{RepoPath: "/repo"}); return err }},
		{"Status", func() error { _, err := sbx.Git.Status(ctx, GitOptions{RepoPath: "/repo"}); return err }},
		{"CreateBranch", func() error { _, err := sbx.Git.CreateBranch(ctx, "feat", GitOptions{RepoPath: "/repo"}); return err }},
		{"Reset", func() error {
			_, err := sbx.Git.Reset(ctx, GitResetOptions{GitOptions: GitOptions{RepoPath: "/repo"}})
			return err
		}},
		{"GetConfig", func() error { _, err := sbx.Git.GetConfig(ctx, "user.name", GitConfigOptions{}); return err }},
	}
	for _, tc := range localOps {
		err := tc.run()
		if _, ok := errors.AsType[*GitAuthError](err); ok {
			t.Fatalf("%s must not classify local error as *GitAuthError: %v", tc.name, err)
		}
		if _, ok := errors.AsType[*CommandExitError](err); !ok {
			t.Fatalf("%s want *CommandExitError, got %T (%v)", tc.name, err, err)
		}
	}

	// Remote operations MUST classify into *GitAuthError.
	remoteOps := []struct {
		name string
		run  func() error
	}{
		{"Clone", func() error {
			_, err := sbx.Git.Clone(ctx, "https://github.com/org/r.git", GitCloneOptions{})
			return err
		}},
		{"Push", func() error { _, err := sbx.Git.Push(ctx, GitOptions{RepoPath: "/repo"}); return err }},
		{"Pull", func() error { _, err := sbx.Git.Pull(ctx, GitOptions{RepoPath: "/repo"}); return err }},
	}
	for _, tc := range remoteOps {
		err := tc.run()
		if _, ok := errors.AsType[*GitAuthError](err); !ok {
			t.Fatalf("%s want *GitAuthError, got %T (%v)", tc.name, err, err)
		}
	}
}

func TestGitShadowedEmbeddedOptions(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	ctx := context.Background()

	// Embedded GitOptions fields used when outer GitCloneOptions fields are empty.
	if _, err := sbx.Git.Clone(ctx, "https://github.com/org/repo.git", GitCloneOptions{
		GitOptions: GitOptions{
			RepoPath: "/workspace/repo",
			Branch:   "develop",
			Username: "emb-user",
			Password: "emb-pass",
		},
	}); err != nil {
		t.Fatalf("Clone with embedded fields: %v", err)
	}

	// Outer GitCloneOptions fields take precedence over embedded GitOptions fields.
	if _, err := sbx.Git.Clone(ctx, "https://github.com/org/repo.git", GitCloneOptions{
		GitOptions: GitOptions{
			RepoPath: "/workspace/repo2",
			Branch:   "emb-branch",
			Username: "emb-user",
			Password: "emb-pass",
		},
		Branch:   "outer-branch",
		Username: "outer-user",
		Password: "outer-pass",
	}); err != nil {
		t.Fatalf("Clone with outer precedence: %v", err)
	}

	// Embedded GitOptions credentials work in DangerouslyAuthenticate too.
	if _, err := sbx.Git.DangerouslyAuthenticate(ctx, GitDangerouslyAuthenticateOptions{
		GitOptions: GitOptions{
			Username: "emb-auth-user",
			Password: "emb-auth-pass",
		},
	}); err != nil {
		t.Fatalf("DangerouslyAuthenticate with embedded creds: %v", err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	if len(calls) != 6 {
		t.Fatalf("want 6 calls, got %d: %+v", len(calls), calls)
	}
	wantFirstClone := []string{
		"clone", "--branch", "develop", "--",
		"https://emb-user:emb-pass@github.com/org/repo.git", "/workspace/repo",
	}
	if !slices.Equal(calls[0].args, wantFirstClone) {
		t.Fatalf("embedded clone args = %v, want %v", calls[0].args, wantFirstClone)
	}
	wantSecondClone := []string{
		"clone", "--branch", "outer-branch", "--",
		"https://outer-user:outer-pass@github.com/org/repo.git", "/workspace/repo2",
	}
	if !slices.Equal(calls[2].args, wantSecondClone) {
		t.Fatalf("outer precedence clone args = %v, want %v", calls[2].args, wantSecondClone)
	}
	if !strings.Contains(calls[5].stdin, "username=emb-auth-user\npassword=emb-auth-pass\n\n") {
		t.Fatalf("DangerouslyAuthenticate embedded stdin = %q", calls[5].stdin)
	}
}

func TestGitWithTemporaryRemoteCredsPreservesRestoreError(t *testing.T) {
	t.Run("joins operation error and restore error", func(t *testing.T) {
		setURLCount := 0
		sbx, _ := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
			if slices.Contains(call.args, "get-url") {
				return "https://github.com/org/repo.git\n", "", 0
			}
			if slices.Contains(call.args, "set-url") {
				setURLCount++
				if setURLCount == 2 {
					return "", "error: could not lock config file .git/config", 255
				}
				return "", "", 0
			}
			if slices.Contains(call.args, "push") {
				return "", "fatal: Authentication failed for 'https://github.com/org/repo.git/'", 128
			}
			return "", "", 0
		})

		_, err := sbx.Git.Push(context.Background(), GitOptions{
			RepoPath: "/repo",
			Remote:   "origin",
			Username: "u",
			Password: "tok",
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if _, ok := errors.AsType[*GitAuthError](err); !ok {
			t.Fatalf("expected *GitAuthError in joined error, got %v", err)
		}
		exitErr, ok := errors.AsType[*CommandExitError](err)
		if !ok {
			t.Fatalf("expected restore *CommandExitError in joined error, got %v", err)
		}
		if !strings.Contains(exitErr.Result.Stderr, "could not lock config file") {
			t.Fatalf("unexpected restore error: %+v", exitErr)
		}
	})

	t.Run("returns restore error when push succeeds", func(t *testing.T) {
		setURLCount := 0
		sbx, _ := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
			if slices.Contains(call.args, "get-url") {
				return "https://github.com/org/repo.git\n", "", 0
			}
			if slices.Contains(call.args, "set-url") {
				setURLCount++
				if setURLCount == 2 {
					return "", "error: disk full", 1
				}
				return "", "", 0
			}
			return "", "", 0
		})

		_, err := sbx.Git.Push(context.Background(), GitOptions{
			RepoPath: "/repo",
			Remote:   "origin",
			Username: "u",
			Password: "tok",
		})
		if _, ok := errors.AsType[*CommandExitError](err); !ok {
			t.Fatalf("want restore *CommandExitError, got %T (%v)", err, err)
		}
	})
}

func TestGitSchemeCaseInsensitiveAndDeriveRepoDir(t *testing.T) {
	got, err := withCredsChecked("HTTPS://github.com/org/repo.git", "alice", "secret")
	if err != nil {
		t.Fatalf("withCredsChecked uppercase HTTPS: %v", err)
	}
	if got != "https://alice:secret@github.com/org/repo.git" {
		t.Fatalf("withCredsChecked = %q", got)
	}
	gotHTTP, err := withCredsChecked("HtTp://example.com/org/repo.git", "alice", "secret")
	if err != nil {
		t.Fatalf("withCredsChecked mixed-case HTTP: %v", err)
	}
	if gotHTTP != "http://alice:secret@example.com/org/repo.git" {
		t.Fatalf("withCredsChecked HTTP = %q", gotHTTP)
	}
	if _, err := withCredsChecked("ssh://git@github.com/org/repo.git", "alice", "secret"); err == nil {
		t.Fatal("expected non-http(s) scheme to fail")
	}

	if stripped := stripCreds("HTTPS://alice:secret@github.com/org/repo.git"); stripped != "https://github.com/org/repo.git" {
		t.Fatalf("stripCreds uppercase HTTPS = %q", stripped)
	}
	if stripped := stripCreds("HTTP://alice:secret@example.com/repo.git"); stripped != "http://example.com/repo.git" {
		t.Fatalf("stripCreds uppercase HTTP = %q", stripped)
	}

	for _, bad := range []string{
		"https://github.com/",
		"https://github.com/org/.",
		"https://github.com/org/..",
		"https://github.com/org/.git",
		"https://github.com/org/..git",
		"https://github.com/org/...git",
	} {
		if dir := deriveRepoDirFromURL(bad); dir != "" {
			t.Fatalf("deriveRepoDirFromURL(%q) = %q, want empty", bad, dir)
		}
	}

	// Verify Clone scrubs inline credentials even with uppercase HTTPS scheme.
	sbx, mock := newGitMockSandbox(t, nil)
	if _, err := sbx.Git.Clone(context.Background(), "HTTPS://alice:secret@github.com/org/my-repo.git", GitCloneOptions{}); err != nil {
		t.Fatalf("Clone uppercase HTTPS: %v", err)
	}
	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls (clone + scrub), got %d", len(calls))
	}
	if !slices.Equal(calls[1].args, []string{"-C", "my-repo", "remote", "set-url", "--", "origin", "https://github.com/org/my-repo.git"}) {
		t.Fatalf("scrub args = %v", calls[1].args)
	}
}

func TestGitOptionInjectionAndDoubleDash(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	ctx := context.Background()

	invalidCases := []struct {
		name string
		fn   func() error
	}{
		{"Clone outer branch", func() error {
			_, err := sbx.Git.Clone(ctx, "https://x/r.git", GitCloneOptions{Branch: "--evil"})
			return err
		}},
		{"Clone embedded branch", func() error {
			_, err := sbx.Git.Clone(ctx, "https://x/r.git", GitCloneOptions{GitOptions: GitOptions{Branch: "-b"}})
			return err
		}},
		{"Init initial branch", func() error { _, err := sbx.Git.Init(ctx, "/repo", GitInitOptions{InitialBranch: "-main"}); return err }},
		{"RemoteAdd name", func() error {
			_, err := sbx.Git.RemoteAdd(ctx, "-origin", "https://x/r.git", GitRemoteAddOptions{})
			return err
		}},
		{"GetRemoteURL name", func() error { _, err := sbx.Git.GetRemoteURL(ctx, "-origin", GitOptions{}); return err }},
		{"SetRemoteURL name", func() error {
			_, err := sbx.Git.SetRemoteURL(ctx, "-origin", "https://x/r.git", GitOptions{})
			return err
		}},
		{"Push remote", func() error { _, err := sbx.Git.Push(ctx, GitOptions{Remote: "-origin"}); return err }},
		{"Push branch", func() error {
			_, err := sbx.Git.Push(ctx, GitOptions{Remote: "origin", Branch: "--delete"})
			return err
		}},
		{"Pull remote", func() error { _, err := sbx.Git.Pull(ctx, GitOptions{Remote: "-origin"}); return err }},
		{"Pull branch", func() error { _, err := sbx.Git.Pull(ctx, GitOptions{Remote: "origin", Branch: "-main"}); return err }},
		{"CreateBranch", func() error { _, err := sbx.Git.CreateBranch(ctx, "-topic", GitOptions{}); return err }},
		{"DeleteBranch", func() error { _, err := sbx.Git.DeleteBranch(ctx, "-topic", GitOptions{}); return err }},
		{"DeleteBranchWithOptions", func() error {
			_, err := sbx.Git.DeleteBranchWithOptions(ctx, "-topic", GitDeleteBranchOptions{})
			return err
		}},
		{"CheckoutBranch", func() error { _, err := sbx.Git.CheckoutBranch(ctx, "--track", GitOptions{}); return err }},
		{"Reset target", func() error { _, err := sbx.Git.Reset(ctx, GitResetOptions{Target: "--hard"}); return err }},
		{"Restore source", func() error {
			_, err := sbx.Git.Restore(ctx, GitRestoreOptions{Paths: []string{"a.go"}, Source: "--staged"})
			return err
		}},
	}
	for _, tc := range invalidCases {
		err := tc.fn()
		if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Fatalf("%s: want *InvalidArgumentError, got %T (%v)", tc.name, err, err)
		}
	}

	// Verify no git command was executed for any rejected input.
	mock.mu.Lock()
	if len(mock.calls) != 0 {
		t.Fatalf("rejected inputs should not invoke git, got %d calls", len(mock.calls))
	}
	mock.mu.Unlock()

	// Verify valid calls emit exact `--` placement (and checkout/reset do NOT break ref semantics).
	if _, err := sbx.Git.Add(ctx, "-weird.txt", GitOptions{RepoPath: "/repo"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := sbx.Git.CheckoutBranch(ctx, "feature", GitOptions{RepoPath: "/repo"}); err != nil {
		t.Fatalf("CheckoutBranch: %v", err)
	}
	if _, err := sbx.Git.Reset(ctx, GitResetOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Mode:       GitResetMixed,
		Target:     "HEAD~1",
		Paths:      []string{"a.go"},
	}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := sbx.Git.SetConfig(ctx, "foo.bar", "--unset", GitConfigOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Scope:      GitConfigLocal,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := sbx.Git.GetConfig(ctx, "foo.bar", GitConfigOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Scope:      GitConfigLocal,
	}); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	want := [][]string{
		{"-C", "/repo", "add", "--", "-weird.txt"},
		{"-C", "/repo", "checkout", "feature"},
		{"-C", "/repo", "reset", "--mixed", "HEAD~1", "--", "a.go"},
		{"-C", "/repo", "config", "--local", "--", "foo.bar", "--unset"},
		{"-C", "/repo", "config", "--local", "--get", "--", "foo.bar"},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(calls), len(want))
	}
	for i, w := range want {
		if !slices.Equal(calls[i].args, w) {
			t.Errorf("call[%d] args = %v, want %v", i, calls[i].args, w)
		}
	}
}

func TestGitRemoteAddOverwriteDoesNotFallbackOnCancel(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, nil)
	mock.streamErr = func(_ int, _ recordedGitCall) error {
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline exceeded"))
	}

	_, err := sbx.Git.RemoteAdd(context.Background(), "origin", "https://github.com/org/repo.git", GitRemoteAddOptions{
		GitOptions: GitOptions{RepoPath: "/repo"},
		Overwrite:  true,
		Fetch:      true,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if _, ok := errors.AsType[*TimeoutError](err); !ok {
		t.Fatalf("want *TimeoutError, got %T (%v)", err, err)
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected only 1 call (remote add) without fallback to set-url, got %d: %+v", len(calls), calls)
	}
}

func TestGitBasicLocalMethodsCoverage(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
		switch {
		case slices.Contains(call.args, "status"):
			return " M a.go\r\nA  b.go\r\n?? c.go\r\n", "", 0
		case slices.Contains(call.args, "branch") && slices.Contains(call.args, "-a"):
			return "* main\n  dev\n  remotes/origin/main\n", "", 0
		case slices.Contains(call.args, "get-url"):
			return "https://github.com/org/repo.git\n", "", 0
		default:
			return "", "", 0
		}
	})
	ctx := context.Background()
	repo := GitOptions{RepoPath: "/repo"}

	if _, err := sbx.Git.Add(ctx, "", repo); err != nil {
		t.Fatalf("Add default: %v", err)
	}
	if _, err := sbx.Git.Commit(ctx, "initial", repo); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := sbx.Git.CommitWithOptions(ctx, "empty commit", GitCommitOptions{
		GitOptions:  repo,
		AuthorName:  "Alice",
		AuthorEmail: "alice@example.com",
		AllowEmpty:  true,
	}); err != nil {
		t.Fatalf("CommitWithOptions: %v", err)
	}
	st, err := sbx.Git.Status(ctx, repo)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !slices.Equal(st.Modified, []string{"a.go"}) || !slices.Equal(st.Staged, []string{"b.go"}) || !slices.Equal(st.Untracked, []string{"c.go"}) {
		t.Fatalf("unexpected Status: %+v", st)
	}
	br, err := sbx.Git.Branches(ctx, repo)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if br.Current != "main" || !slices.Equal(br.Local, []string{"main", "dev"}) || !slices.Equal(br.Remote, []string{"origin/main"}) {
		t.Fatalf("unexpected Branches: %+v", br)
	}
	if _, err := sbx.Git.CreateBranch(ctx, "feat", repo); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := sbx.Git.CheckoutBranch(ctx, "feat", repo); err != nil {
		t.Fatalf("CheckoutBranch: %v", err)
	}
	if _, err := sbx.Git.DeleteBranch(ctx, "feat", repo); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if _, err := sbx.Git.SetRemoteURL(ctx, "origin", "https://github.com/org/new.git", repo); err != nil {
		t.Fatalf("SetRemoteURL: %v", err)
	}
	gotURL, err := sbx.Git.GetRemoteURL(ctx, "", repo)
	if err != nil || gotURL != "https://github.com/org/repo.git" {
		t.Fatalf("GetRemoteURL = %q, err = %v", gotURL, err)
	}
	if _, err := sbx.Git.RemoteAdd(ctx, "upstream", "https://github.com/org/up.git", GitRemoteAddOptions{
		GitOptions: repo,
		Fetch:      true,
	}); err != nil {
		t.Fatalf("RemoteAdd no-overwrite: %v", err)
	}

	// Empty argument validation errors.
	for _, fn := range []func() error{
		func() error { _, err := sbx.Git.CreateBranch(ctx, "", repo); return err },
		func() error { _, err := sbx.Git.DeleteBranch(ctx, "", repo); return err },
		func() error { _, err := sbx.Git.CheckoutBranch(ctx, "", repo); return err },
		func() error { _, err := sbx.Git.SetRemoteURL(ctx, "", "https://x/r.git", repo); return err },
		func() error { _, err := sbx.Git.SetRemoteURL(ctx, "origin", "", repo); return err },
	} {
		if _, ok := errors.AsType[*InvalidArgumentError](fn()); !ok {
			t.Fatal("expected *InvalidArgumentError for empty argument")
		}
	}

	mock.mu.Lock()
	calls := append([]recordedGitCall(nil), mock.calls...)
	mock.mu.Unlock()

	want := [][]string{
		{"-C", "/repo", "add", "--", "."},
		{"-C", "/repo", "commit", "-m", "initial"},
		{"-C", "/repo", "-c", "user.name=Alice", "-c", "user.email=alice@example.com", "commit", "-m", "empty commit", "--allow-empty"},
		{"-C", "/repo", "status", "--porcelain"},
		{"-C", "/repo", "branch", "-a"},
		{"-C", "/repo", "branch", "--", "feat"},
		{"-C", "/repo", "checkout", "feat"},
		{"-C", "/repo", "branch", "-D", "--", "feat"},
		{"-C", "/repo", "remote", "set-url", "--", "origin", "https://github.com/org/new.git"},
		{"-C", "/repo", "remote", "get-url", "--", "origin"},
		{"-C", "/repo", "remote", "add", "-f", "--", "upstream", "https://github.com/org/up.git"},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(calls), len(want))
	}
	for i, w := range want {
		if !slices.Equal(calls[i].args, w) {
			t.Errorf("call[%d] args = %v, want %v", i, calls[i].args, w)
		}
	}
}
