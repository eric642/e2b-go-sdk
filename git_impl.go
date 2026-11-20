package e2b

import (
	"context"
	"errors"
	"io"
	"maps"
	neturl "net/url"
	"strconv"
	"strings"
	"time"
)

// GitOptions holds optional configuration shared across Git operations.
type GitOptions struct {
	RepoPath       string
	User           string
	Cwd            string
	Envs           map[string]string
	TimeoutMs      int
	RequestTimeout time.Duration

	// Remote, Branch, SetUpstream, Username, and Password are optional per-call
	// parameters used by Push and Pull. When Username and Password are supplied
	// on Push or Pull, credentials are injected only for the duration of the
	// operation and scrubbed from the remote URL before returning.
	Remote      string
	Branch      string
	SetUpstream bool
	Username    string
	Password    string
}

// GitCmdOptions is an alias for GitOptions.
type GitCmdOptions = GitOptions

// GitCloneOptions configures Git.Clone.
type GitCloneOptions struct {
	GitOptions
	Branch   string
	Depth    int
	Username string
	Password string
	// DangerouslyStoreCredentials persists credentials in .git/config when true.
	// By default (false), inline HTTP(S) credentials are scrubbed from origin
	// immediately after cloning.
	DangerouslyStoreCredentials bool
}

// GitInitOptions configures Git.Init.
type GitInitOptions struct {
	GitOptions
	Bare          bool
	InitialBranch string
}

// GitRemoteAddOptions configures Git.RemoteAdd.
type GitRemoteAddOptions struct {
	GitOptions
	Fetch     bool
	Overwrite bool
}

// GitDeleteBranchOptions configures Git.DeleteBranchWithOptions.
type GitDeleteBranchOptions struct {
	GitOptions
	Force bool
}

// GitCommitOptions configures Git.CommitWithOptions.
type GitCommitOptions struct {
	GitOptions
	AuthorName  string
	AuthorEmail string
	AllowEmpty  bool
}

// GitAddOptions configures Git.AddWithOptions.
type GitAddOptions struct {
	GitOptions
	// Files lists paths to stage; when empty, All controls whether `-A` (true,
	// default) or `.` (false) is passed to `git add`.
	Files []string
	All   *bool
}

// GitResetMode enumerates supported modes for Git.Reset.
type GitResetMode string

const (
	GitResetSoft  GitResetMode = "soft"
	GitResetMixed GitResetMode = "mixed"
	GitResetHard  GitResetMode = "hard"
	GitResetMerge GitResetMode = "merge"
	GitResetKeep  GitResetMode = "keep"
)

// GitResetOptions configures Git.Reset.
type GitResetOptions struct {
	GitOptions
	Mode   GitResetMode
	Target string
	Paths  []string
}

// GitRestoreOptions configures Git.Restore.
type GitRestoreOptions struct {
	GitOptions
	Paths    []string
	Staged   *bool
	Worktree *bool
	Source   string
}

// GitConfigScope enumerates supported configuration scopes for Git.SetConfig
// and Git.GetConfig.
type GitConfigScope string

const (
	GitConfigGlobal GitConfigScope = "global"
	GitConfigLocal  GitConfigScope = "local"
	GitConfigSystem GitConfigScope = "system"
)

// GitConfigOptions configures Git.SetConfig, Git.GetConfig, and
// Git.ConfigureUser.
type GitConfigOptions struct {
	GitOptions
	Scope GitConfigScope
}

// GitDangerouslyAuthenticateOptions configures Git.DangerouslyAuthenticate.
type GitDangerouslyAuthenticateOptions struct {
	GitOptions
	Username string
	Password string
	Host     string
	Protocol string
}

// GitStatus is the parsed output of `git status --porcelain`.
type GitStatus struct {
	Modified  []string
	Staged    []string
	Untracked []string
}

// GitBranches is the parsed output of `git branch -a`.
type GitBranches struct {
	Current string
	Local   []string
	Remote  []string
}

var defaultGitEnv = map[string]string{"GIT_TERMINAL_PROMPT": "0"}

// Clone clones a git repository into the sandbox. When HTTP(S) credentials are
// provided (via opts.Username/opts.Password or embedded in repoURL) and
// opts.DangerouslyStoreCredentials is false, Clone automatically scrubs the
// credentials from `origin` via `git -C <targetDir> remote set-url origin <cleanURL>`
// so plaintext secrets never remain in .git/config.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Clone(ctx context.Context, repoURL string, opts GitCloneOptions) (*CommandResult, error) {
	if opts.Password != "" && opts.Username == "" {
		return nil, &InvalidArgumentError{Message: "username is required when using a password or token for git clone"}
	}

	credURL := repoURL
	if opts.Username != "" || opts.Password != "" {
		var err error
		credURL, err = withCredsChecked(repoURL, opts.Username, opts.Password)
		if err != nil {
			return nil, err
		}
	}
	cleanURL := stripCreds(credURL)
	stripInlineCreds := !opts.DangerouslyStoreCredentials && cleanURL != credURL

	targetDir := opts.RepoPath
	if stripInlineCreds && targetDir == "" {
		targetDir = deriveRepoDirFromURL(cleanURL)
		if targetDir == "" {
			return nil, &InvalidArgumentError{
				Message: "destination RepoPath is required when cloning with credentials without DangerouslyStoreCredentials",
			}
		}
	}

	args := []string{"clone"}
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch)
	}
	args = append(args, credURL)
	if opts.RepoPath != "" {
		args = append(args, opts.RepoPath)
	}

	// Do not pass RepoPath as -C to git clone (destination may not exist yet).
	cloneExecOpts := opts.GitOptions
	cloneExecOpts.RepoPath = ""
	res, err := g.run(ctx, args, cloneExecOpts)
	if err != nil {
		return res, err
	}

	if stripInlineCreds {
		scrubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		scrubOpts := cloneExecOpts
		scrubOpts.RepoPath = targetDir
		if _, scrubErr := g.run(scrubCtx, []string{"remote", "set-url", "origin", cleanURL}, scrubOpts); scrubErr != nil {
			return res, scrubErr
		}
	}
	return res, nil
}

// Init initializes a new git repository at repoPath (or opts.RepoPath when
// repoPath is empty).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Init(ctx context.Context, repoPath string, opts GitInitOptions) (*CommandResult, error) {
	target := repoPath
	if target == "" {
		target = opts.RepoPath
	}
	args := []string{"init"}
	if opts.InitialBranch != "" {
		args = append(args, "--initial-branch", opts.InitialBranch)
	}
	if opts.Bare {
		args = append(args, "--bare")
	}
	if target != "" {
		args = append(args, target)
	}
	execOpts := opts.GitOptions
	execOpts.RepoPath = ""
	return g.run(ctx, args, execOpts)
}

// RemoteAdd adds a remote (or updates its URL when opts.Overwrite is true).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) RemoteAdd(ctx context.Context, name, remoteURL string, opts GitRemoteAddOptions) (*CommandResult, error) {
	if name == "" || remoteURL == "" {
		return nil, &InvalidArgumentError{Message: "both remote name and URL are required"}
	}
	if !opts.Overwrite {
		args := []string{"remote", "add"}
		if opts.Fetch {
			args = append(args, "-f")
		}
		args = append(args, name, remoteURL)
		return g.run(ctx, args, opts.GitOptions)
	}

	res, err := g.run(ctx, []string{"remote", "add", name, remoteURL}, opts.GitOptions)
	if err != nil {
		res, err = g.run(ctx, []string{"remote", "set-url", name, remoteURL}, opts.GitOptions)
		if err != nil {
			return res, err
		}
	}
	if opts.Fetch {
		return g.run(ctx, []string{"fetch", name}, opts.GitOptions)
	}
	return res, nil
}

// Add runs `git add` for the given pattern (defaulting to "." when empty).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Add(ctx context.Context, pattern string, opts GitOptions) (*CommandResult, error) {
	if pattern == "" {
		pattern = "."
	}
	return g.run(ctx, []string{"add", pattern}, opts)
}

// AddWithOptions stages multiple files (`git add -- <files...>`) or stages all
// changes (`git add -A` when opts.All is nil or true; `git add .` when false).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) AddWithOptions(ctx context.Context, opts GitAddOptions) (*CommandResult, error) {
	args := []string{"add"}
	if len(opts.Files) == 0 {
		if opts.All == nil || *opts.All {
			args = append(args, "-A")
		} else {
			args = append(args, ".")
		}
	} else {
		args = append(args, "--")
		args = append(args, opts.Files...)
	}
	return g.run(ctx, args, opts.GitOptions)
}

// Commit runs `git commit -m <message>`.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Commit(ctx context.Context, message string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"commit", "-m", message}, opts)
}

// CommitWithOptions runs `git commit` with optional inline author identity and
// --allow-empty support.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) CommitWithOptions(ctx context.Context, message string, opts GitCommitOptions) (*CommandResult, error) {
	var args []string
	if opts.AuthorName != "" {
		args = append(args, "-c", "user.name="+opts.AuthorName)
	}
	if opts.AuthorEmail != "" {
		args = append(args, "-c", "user.email="+opts.AuthorEmail)
	}
	args = append(args, "commit", "-m", message)
	if opts.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	return g.run(ctx, args, opts.GitOptions)
}

// Push runs `git push`. When per-call opts.Username and opts.Password are
// supplied, credentials are injected into the target remote URL temporarily and
// scrubbed back to the clean URL in defer before Push returns.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Push(ctx context.Context, opts GitOptions) (*CommandResult, error) {
	return g.runRemoteOp(ctx, "push", opts.SetUpstream, opts)
}

// Pull runs `git pull`. When per-call opts.Username and opts.Password are
// supplied, credentials are injected into the target remote URL temporarily and
// scrubbed back to the clean URL in defer before Pull returns.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Pull(ctx context.Context, opts GitOptions) (*CommandResult, error) {
	return g.runRemoteOp(ctx, "pull", false, opts)
}

func (g *Git) runRemoteOp(ctx context.Context, subcmd string, setUpstream bool, opts GitOptions) (*CommandResult, error) {
	if (opts.Username == "") != (opts.Password == "") {
		if opts.Password != "" {
			return nil, &InvalidArgumentError{Message: "username is required when using a password or token for git " + subcmd}
		}
		return nil, &InvalidArgumentError{Message: "password is required when using a username for git " + subcmd}
	}

	buildArgs := func(remoteName string) []string {
		args := []string{subcmd}
		targetRemote := remoteName
		if targetRemote == "" {
			targetRemote = opts.Remote
		}
		if setUpstream && targetRemote != "" {
			args = append(args, "--set-upstream")
		}
		if targetRemote != "" {
			args = append(args, targetRemote)
		}
		if opts.Branch != "" {
			args = append(args, opts.Branch)
		}
		return args
	}

	if opts.Username != "" && opts.Password != "" {
		return g.withTemporaryRemoteCreds(ctx, opts, func(remoteName string) (*CommandResult, error) {
			return g.run(ctx, buildArgs(remoteName), opts)
		})
	}
	return g.run(ctx, buildArgs(""), opts)
}

// Status parses the porcelain output of `git status --porcelain`.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Status(ctx context.Context, opts GitOptions) (*GitStatus, error) {
	res, err := g.run(ctx, []string{"status", "--porcelain"}, opts)
	if err != nil {
		return nil, err
	}
	return parseGitStatus(res.Stdout), nil
}

// Branches parses `git branch -a`.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Branches(ctx context.Context, opts GitOptions) (*GitBranches, error) {
	res, err := g.run(ctx, []string{"branch", "-a"}, opts)
	if err != nil {
		return nil, err
	}
	return parseGitBranches(res.Stdout), nil
}

// CreateBranch creates a branch with the given name.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) CreateBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	if name == "" {
		return nil, &InvalidArgumentError{Message: "branch name is required"}
	}
	return g.run(ctx, []string{"branch", name}, opts)
}

// DeleteBranch force-deletes a branch (`git branch -D <name>`). Use
// DeleteBranchWithOptions to control `-d` vs `-D`.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) DeleteBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	if name == "" {
		return nil, &InvalidArgumentError{Message: "branch name is required"}
	}
	return g.run(ctx, []string{"branch", "-D", name}, opts)
}

// DeleteBranchWithOptions deletes a branch using `-d` by default or `-D` when
// opts.Force is true.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) DeleteBranchWithOptions(ctx context.Context, name string, opts GitDeleteBranchOptions) (*CommandResult, error) {
	if name == "" {
		return nil, &InvalidArgumentError{Message: "branch name is required"}
	}
	flag := "-d"
	if opts.Force {
		flag = "-D"
	}
	return g.run(ctx, []string{"branch", flag, name}, opts.GitOptions)
}

// CheckoutBranch switches to an existing branch.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) CheckoutBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	if name == "" {
		return nil, &InvalidArgumentError{Message: "branch name is required"}
	}
	return g.run(ctx, []string{"checkout", name}, opts)
}

// Reset resets the current HEAD to the specified state (`soft`, `mixed`,
// `hard`, `merge`, or `keep`).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Reset(ctx context.Context, opts GitResetOptions) (*CommandResult, error) {
	switch opts.Mode {
	case "", GitResetSoft, GitResetMixed, GitResetHard, GitResetMerge, GitResetKeep:
	default:
		return nil, &InvalidArgumentError{
			Message: "reset mode must be one of: soft, mixed, hard, merge, keep",
		}
	}
	args := []string{"reset"}
	if opts.Mode != "" {
		args = append(args, "--"+string(opts.Mode))
	}
	if opts.Target != "" {
		args = append(args, opts.Target)
	}
	if len(opts.Paths) > 0 {
		args = append(args, "--")
		args = append(args, opts.Paths...)
	}
	return g.run(ctx, args, opts.GitOptions)
}

// Restore restores working-tree files and/or unstages index changes.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) Restore(ctx context.Context, opts GitRestoreOptions) (*CommandResult, error) {
	if len(opts.Paths) == 0 {
		return nil, &InvalidArgumentError{Message: "at least one path is required for git restore"}
	}

	staged := opts.Staged != nil && *opts.Staged
	worktree := opts.Staged == nil && opts.Worktree == nil
	if opts.Worktree != nil {
		worktree = *opts.Worktree
	}
	if !staged && !worktree {
		return nil, &InvalidArgumentError{Message: "at least one of Staged or Worktree must be true"}
	}

	args := []string{"restore"}
	if worktree {
		args = append(args, "--worktree")
	}
	if staged {
		args = append(args, "--staged")
	}
	if opts.Source != "" {
		args = append(args, "--source", opts.Source)
	}
	args = append(args, "--")
	args = append(args, opts.Paths...)
	return g.run(ctx, args, opts.GitOptions)
}

// GetRemoteURL returns the URL of the named remote (default "origin").
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) GetRemoteURL(ctx context.Context, name string, opts GitOptions) (string, error) {
	if name == "" {
		name = "origin"
	}
	res, err := g.run(ctx, []string{"remote", "get-url", name}, opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// SetRemoteURL sets the URL of the named remote.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) SetRemoteURL(ctx context.Context, name, remoteURL string, opts GitOptions) (*CommandResult, error) {
	if name == "" || remoteURL == "" {
		return nil, &InvalidArgumentError{Message: "both remote name and URL are required"}
	}
	return g.run(ctx, []string{"remote", "set-url", name, remoteURL}, opts)
}

// SetConfig sets a git configuration entry in the requested scope (default
// "global"; "local" requires opts.RepoPath).
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) SetConfig(ctx context.Context, key, value string, opts GitConfigOptions) (*CommandResult, error) {
	if key == "" {
		return nil, &InvalidArgumentError{Message: "git config key is required"}
	}
	scopeFlag, execOpts, err := resolveGitConfigScope(opts)
	if err != nil {
		return nil, err
	}
	return g.run(ctx, []string{"config", scopeFlag, key, value}, execOpts)
}

// GetConfig reads a git configuration entry in the requested scope (default
// "global"; "local" requires opts.RepoPath). Returns ("", nil) when the key is
// not set.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) GetConfig(ctx context.Context, key string, opts GitConfigOptions) (string, error) {
	if key == "" {
		return "", &InvalidArgumentError{Message: "git config key is required"}
	}
	scopeFlag, execOpts, err := resolveGitConfigScope(opts)
	if err != nil {
		return "", err
	}
	res, err := g.run(ctx, []string{"config", scopeFlag, "--get", key}, execOpts)
	if err != nil {
		var exitErr *CommandExitError
		if errors.As(err, &exitErr) && exitErr.Result.ExitCode == 1 && strings.TrimSpace(exitErr.Result.Stderr) == "" {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// ConfigureUser sets user.name and user.email in the requested scope (default
// "global").
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) ConfigureUser(ctx context.Context, name, email string, opts GitConfigOptions) (*CommandResult, error) {
	if name == "" || email == "" {
		return nil, &InvalidArgumentError{Message: "both name and email are required"}
	}
	if _, err := g.SetConfig(ctx, "user.name", name, opts); err != nil {
		return nil, err
	}
	return g.SetConfig(ctx, "user.email", email, opts)
}

// DangerouslyAuthenticate configures `git config --global credential.helper store`
// and approves the given credentials via `git credential approve` over stdin so
// credentials do not appear in process arguments. Prefer short-lived tokens or
// per-call Clone/Push/Pull credentials instead.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (g *Git) DangerouslyAuthenticate(ctx context.Context, opts GitDangerouslyAuthenticateOptions) (*CommandResult, error) {
	if opts.Username == "" || opts.Password == "" {
		return nil, &InvalidArgumentError{Message: "both username and password are required to authenticate git"}
	}
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = "github.com"
	}
	protocol := strings.TrimSpace(opts.Protocol)
	if protocol == "" {
		protocol = "https"
	}
	for _, v := range []string{protocol, host, opts.Username, opts.Password} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, &InvalidArgumentError{Message: "git credential fields must not contain newlines"}
		}
	}

	execOpts := opts.GitOptions
	execOpts.RepoPath = ""
	if _, err := g.run(ctx, []string{"config", "--global", "credential.helper", "store"}, execOpts); err != nil {
		return nil, err
	}

	credInput := strings.Join([]string{
		"protocol=" + protocol,
		"host=" + host,
		"username=" + opts.Username,
		"password=" + opts.Password,
		"",
		"",
	}, "\n")
	return g.runWithStdin(ctx, []string{"credential", "approve"}, execOpts, strings.NewReader(credInput))
}

func resolveGitConfigScope(opts GitConfigOptions) (string, GitOptions, error) {
	scope := opts.Scope
	if scope == "" {
		scope = GitConfigGlobal
	}
	execOpts := opts.GitOptions
	switch scope {
	case GitConfigGlobal, GitConfigSystem:
		execOpts.RepoPath = ""
	case GitConfigLocal:
		if opts.RepoPath == "" {
			return "", execOpts, &InvalidArgumentError{Message: `RepoPath is required when using git config scope "local"`}
		}
	default:
		return "", execOpts, &InvalidArgumentError{Message: "git config scope must be one of: global, local, system"}
	}
	return "--" + string(scope), execOpts, nil
}

func (g *Git) resolveRemoteName(ctx context.Context, opts GitOptions) (string, error) {
	if opts.Remote != "" {
		return opts.Remote, nil
	}
	res, err := g.run(ctx, []string{"remote"}, opts)
	if err != nil {
		return "", err
	}
	var remotes []string
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if r := strings.TrimSpace(line); r != "" {
			remotes = append(remotes, r)
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	if len(remotes) == 0 {
		return "origin", nil
	}
	return "", &InvalidArgumentError{
		Message: "Remote is required when using Username/Password and the repository has multiple remotes",
	}
}

func (g *Git) withTemporaryRemoteCreds(ctx context.Context, opts GitOptions, fn func(remoteName string) (*CommandResult, error)) (res *CommandResult, retErr error) {
	remoteName, err := g.resolveRemoteName(ctx, opts)
	if err != nil {
		return nil, err
	}
	origURL, err := g.GetRemoteURL(ctx, remoteName, opts)
	if err != nil {
		return nil, err
	}
	if origURL == "" {
		return nil, &InvalidArgumentError{Message: "remote URL not found for " + remoteName}
	}
	credURL, err := withCredsChecked(origURL, opts.Username, opts.Password)
	if err != nil {
		return nil, err
	}
	cleanURL := stripCreds(origURL)

	if _, err := g.run(ctx, []string{"remote", "set-url", remoteName, credURL}, opts); err != nil {
		return nil, err
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if _, restoreErr := g.run(restoreCtx, []string{"remote", "set-url", remoteName, cleanURL}, opts); restoreErr != nil && retErr == nil {
			retErr = restoreErr
		}
	}()

	return fn(remoteName)
}

func (g *Git) run(ctx context.Context, args []string, opts GitOptions) (*CommandResult, error) {
	return g.runWithStdin(ctx, args, opts, nil)
}

func (g *Git) runWithStdin(ctx context.Context, args []string, opts GitOptions, stdin io.Reader) (*CommandResult, error) {
	envs := make(map[string]string, len(defaultGitEnv)+len(opts.Envs))
	maps.Copy(envs, defaultGitEnv)
	maps.Copy(envs, opts.Envs)
	fullArgs := args
	if opts.RepoPath != "" {
		fullArgs = make([]string, 0, len(args)+2)
		fullArgs = append(fullArgs, "-C", opts.RepoPath)
		fullArgs = append(fullArgs, args...)
	}
	runOpts := RunOptions{
		Args:           fullArgs,
		Envs:           envs,
		Cwd:            opts.Cwd,
		User:           opts.User,
		TimeoutMs:      opts.TimeoutMs,
		RequestTimeout: opts.RequestTimeout,
		Stdin:          stdin,
	}
	h, err := g.cmds.Run(ctx, "git", runOpts)
	if err != nil {
		if exitErr, ok := err.(*CommandExitError); ok {
			return &exitErr.Result, classifyGitErr(exitErr)
		}
		return nil, err
	}
	res, err := h.Wait(ctx)
	if err != nil {
		if exitErr, ok := err.(*CommandExitError); ok {
			return &exitErr.Result, classifyGitErr(exitErr)
		}
		return nil, err
	}
	return res, nil
}

func withCredsChecked(rawURL, user, pass string) (string, error) {
	if user == "" && pass == "" {
		return rawURL, nil
	}
	if user == "" || pass == "" {
		return "", &InvalidArgumentError{Message: "both username and password are required when using git credentials"}
	}
	scheme, _, ok := strings.Cut(rawURL, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return "", &InvalidArgumentError{Message: "only http(s) git URLs support username/password credentials"}
	}
	u, err := neturl.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", &InvalidArgumentError{Message: "invalid git URL: " + rawURL}
	}
	u.User = neturl.UserPassword(user, pass)
	return u.String(), nil
}

func stripCreds(rawURL string) string {
	scheme, _, ok := strings.Cut(rawURL, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return rawURL
	}
	u, err := neturl.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

func deriveRepoDirFromURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimRight(u.Path, "/")
	if trimmed == "" {
		return ""
	}
	idx := strings.LastIndex(trimmed, "/")
	seg := trimmed
	if idx >= 0 {
		seg = trimmed[idx+1:]
	}
	seg = strings.TrimSuffix(seg, ".git")
	return seg
}

func classifyGitErr(exit *CommandExitError) error {
	msg := strings.ToLower(exit.Result.Stderr + "\n" + exit.Result.Stdout)
	switch {
	case strings.Contains(msg, "authentication failed"),
		strings.Contains(msg, "403"),
		strings.Contains(msg, "could not read username"),
		strings.Contains(msg, "terminal prompts disabled"),
		strings.Contains(msg, "invalid username or password"),
		strings.Contains(msg, "access denied"),
		strings.Contains(msg, "not authorized"):
		return &GitAuthError{Message: exit.Result.Stderr}
	case strings.Contains(msg, "no upstream"),
		strings.Contains(msg, "no tracking information"),
		strings.Contains(msg, "set the remote as upstream"),
		strings.Contains(msg, "set the upstream branch"),
		strings.Contains(msg, "please specify which branch you want to merge with"):
		return &GitUpstreamError{Message: exit.Result.Stderr}
	}
	return exit
}

func parseGitStatus(out string) *GitStatus {
	s := &GitStatus{}
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 3 {
			continue
		}
		x, y, rest := line[0], line[1], line[3:]
		switch {
		case x == '?' && y == '?':
			s.Untracked = append(s.Untracked, rest)
		case x != ' ':
			s.Staged = append(s.Staged, rest)
			if y != ' ' {
				s.Modified = append(s.Modified, rest)
			}
		case y != ' ':
			s.Modified = append(s.Modified, rest)
		}
	}
	return s
}

func parseGitBranches(out string) *GitBranches {
	b := &GitBranches{}
	for raw := range strings.SplitSeq(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if after, ok := strings.CutPrefix(line, "* "); ok {
			b.Current = strings.TrimSpace(after)
			b.Local = append(b.Local, b.Current)
			continue
		}
		if after, ok := strings.CutPrefix(line, "remotes/"); ok {
			b.Remote = append(b.Remote, after)
			continue
		}
		b.Local = append(b.Local, line)
	}
	return b
}
