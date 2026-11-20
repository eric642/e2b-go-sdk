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
	if err != exit {
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
	mu      sync.Mutex
	nextPID uint32
	calls   []recordedGitCall
	stdin   map[uint32][]byte
	closed  map[uint32]bool
	step    func(idx int, call recordedGitCall) (stdout, stderr string, exitCode int32)
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
	m.mu.Unlock()

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
		"clone", "--depth", "1", "--branch", "main",
		"https://x-access-token:ghs_secret123@github.com/org/private-repo.git",
		"/workspace/private-repo",
	}
	if !slices.Equal(calls[0].args, wantCloneArgs) {
		t.Fatalf("clone args = %v, want %v", calls[0].args, wantCloneArgs)
	}
	wantScrubArgs := []string{
		"-C", "/workspace/private-repo",
		"remote", "set-url", "origin", "https://github.com/org/private-repo.git",
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
	wantDerivedScrub := []string{"-C", "my-repo", "remote", "set-url", "origin", "https://github.com/org/my-repo.git"}
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
	if !slices.Equal(pushCalls[2].args, []string{"-C", "/repo", "remote", "set-url", "origin", "https://u:tok@github.com/org/repo.git"}) {
		t.Fatalf("inject creds args = %v", pushCalls[2].args)
	}
	if !slices.Equal(pushCalls[3].args, []string{"-C", "/repo", "push", "--set-upstream", "origin", "main"}) {
		t.Fatalf("push args = %v", pushCalls[3].args)
	}
	if !slices.Equal(pushCalls[4].args, []string{"-C", "/repo", "remote", "set-url", "origin", "https://github.com/org/repo.git"}) {
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
	if !slices.Equal(pullCalls[3].args, []string{"-C", "/repo", "remote", "set-url", "upstream", "https://github.com/org/repo.git"}) {
		t.Fatalf("pull restore args = %v", pullCalls[3].args)
	}
}

func TestGitParityMethodsAndValidation(t *testing.T) {
	sbx, mock := newGitMockSandbox(t, func(_ int, call recordedGitCall) (string, string, int32) {
		if slices.Equal(call.args, []string{"-C", "/repo", "remote", "add", "origin", "https://github.com/org/new.git"}) {
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

	if !slices.Equal(calls[0].args, []string{"init", "--initial-branch", "main", "--bare", "/repo"}) {
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
