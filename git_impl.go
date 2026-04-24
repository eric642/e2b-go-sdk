package e2b

import (
	"context"
	"strings"
)

// GitOptions holds optional knobs shared by most Git methods.
type GitOptions struct {
	RepoPath string
	User     string
	Cwd      string
	Envs     map[string]string
}

// GitCloneOptions configures Git.Clone.
type GitCloneOptions struct {
	GitOptions
	Branch   string
	Depth    int
	Username string
	Password string
	// DangerouslyStoreCredentials persists creds in the repo config.
	DangerouslyStoreCredentials bool
}

// GitStatus is the parsed output of "git status".
type GitStatus struct {
	Modified  []string
	Staged    []string
	Untracked []string
}

// GitBranches is the parsed output of "git branch".
type GitBranches struct {
	Current string
	Local   []string
	Remote  []string
}

var defaultGitEnv = map[string]string{"GIT_TERMINAL_PROMPT": "0"}

// Clone clones a repository into the sandbox. When creds are provided they
// are injected into the URL for a single invocation; pass
// DangerouslyStoreCredentials to persist them.
func (g *Git) Clone(ctx context.Context, url string, opts GitCloneOptions) (*CommandResult, error) {
	args := []string{"clone"}
	if opts.Depth > 0 {
		args = append(args, "--depth", itoa(opts.Depth))
	}
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch)
	}
	args = append(args, withCreds(url, opts.Username, opts.Password))
	if opts.RepoPath != "" {
		args = append(args, opts.RepoPath)
	}
	return g.run(ctx, args, opts.GitOptions)
}

// Add runs `git add` for the given pattern(s).
func (g *Git) Add(ctx context.Context, pattern string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"add", pattern}, opts)
}

// Commit runs `git commit -m <message>`.
func (g *Git) Commit(ctx context.Context, message string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"commit", "-m", message}, opts)
}

// Push runs `git push`.
func (g *Git) Push(ctx context.Context, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"push"}, opts)
}

// Pull runs `git pull`.
func (g *Git) Pull(ctx context.Context, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"pull"}, opts)
}

// Status parses the porcelain output of `git status`.
func (g *Git) Status(ctx context.Context, opts GitOptions) (*GitStatus, error) {
	res, err := g.run(ctx, []string{"status", "--porcelain"}, opts)
	if err != nil {
		return nil, err
	}
	return parseGitStatus(res.Stdout), nil
}

// Branches parses `git branch -a`.
func (g *Git) Branches(ctx context.Context, opts GitOptions) (*GitBranches, error) {
	res, err := g.run(ctx, []string{"branch", "-a"}, opts)
	if err != nil {
		return nil, err
	}
	return parseGitBranches(res.Stdout), nil
}

// CreateBranch creates a branch with the given name.
func (g *Git) CreateBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"branch", name}, opts)
}

// DeleteBranch deletes a branch.
func (g *Git) DeleteBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"branch", "-D", name}, opts)
}

// CheckoutBranch switches to a branch.
func (g *Git) CheckoutBranch(ctx context.Context, name string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"checkout", name}, opts)
}

// GetRemoteURL returns the URL of the named remote (default "origin").
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
func (g *Git) SetRemoteURL(ctx context.Context, name, url string, opts GitOptions) (*CommandResult, error) {
	return g.run(ctx, []string{"remote", "set-url", name, url}, opts)
}

// run executes git with the given argv plus -C flag, merging the default
// git env with any caller-supplied env.
func (g *Git) run(ctx context.Context, args []string, opts GitOptions) (*CommandResult, error) {
	envs := make(map[string]string, len(defaultGitEnv)+len(opts.Envs))
	for k, v := range defaultGitEnv {
		envs[k] = v
	}
	for k, v := range opts.Envs {
		envs[k] = v
	}
	cwd := opts.Cwd
	if opts.RepoPath != "" {
		args = append([]string{"-C", opts.RepoPath}, args...)
	}
	runOpts := RunOptions{
		Args: args,
		Envs: envs,
		Cwd:  cwd,
		User: opts.User,
	}
	h, err := g.cmds.Run(ctx, "git", runOpts)
	if err != nil {
		// Non-zero exits come back as CommandExitError. Let caller inspect.
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

// withCreds injects basic-auth creds into an HTTP(S) git URL for a single
// operation. Returns the original URL unchanged if creds are empty or the
// scheme isn't http/https.
func withCreds(url, user, pass string) string {
	if user == "" && pass == "" {
		return url
	}
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	if scheme != "http" && scheme != "https" {
		return url
	}
	return scheme + "://" + user + ":" + pass + "@" + rest
}

func classifyGitErr(exit *CommandExitError) error {
	msg := strings.ToLower(exit.Result.Stderr)
	switch {
	case strings.Contains(msg, "authentication failed"),
		strings.Contains(msg, "403"),
		strings.Contains(msg, "could not read username"):
		return &GitAuthError{Message: exit.Result.Stderr}
	case strings.Contains(msg, "no upstream"):
		return &GitUpstreamError{Message: exit.Result.Stderr}
	}
	return exit
}

// parseGitStatus reads `git status --porcelain` output into a GitStatus.
func parseGitStatus(out string) *GitStatus {
	s := &GitStatus{}
	for _, line := range strings.Split(out, "\n") {
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

// parseGitBranches parses `git branch -a` output.
func parseGitBranches(out string) *GitBranches {
	b := &GitBranches{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "* ") {
			b.Current = strings.TrimSpace(line[2:])
			b.Local = append(b.Local, b.Current)
			continue
		}
		if strings.HasPrefix(line, "remotes/") {
			b.Remote = append(b.Remote, strings.TrimPrefix(line, "remotes/"))
			continue
		}
		b.Local = append(b.Local, line)
	}
	return b
}
