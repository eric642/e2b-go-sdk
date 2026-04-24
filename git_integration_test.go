package e2b

import (
	"strings"
	"testing"
)

// ensureGitConfig sets user.name/user.email; required for `git commit`.
func ensureGitConfig(t *testing.T, sbx *Sandbox) {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	commands := [][]string{
		{"-c", "git config --global user.email sandbox@example.com"},
		{"-c", "git config --global user.name SandboxBot"},
	}
	for _, argv := range commands {
		h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: argv})
		if err != nil {
			t.Fatalf("git config: %v", err)
		}
		if _, err := h.Wait(ctx); err != nil {
			t.Fatalf("git config wait: %v", err)
		}
	}
}

func TestIntegrationGitInitAddCommitStatus(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()
	ensureGitConfig(t, sbx)

	repo := "/tmp/repo-" + sbx.ID
	// Init repo via shell (the Git client assumes an existing repo).
	if h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", "mkdir -p " + repo + " && cd " + repo + " && git init -b main"}}); err != nil {
		t.Fatalf("init: %v", err)
	} else if _, err := h.Wait(ctx); err != nil {
		t.Fatalf("init wait: %v", err)
	}

	// Write a file.
	if _, err := sbx.Files.WriteString(ctx, repo+"/README.md", "hello\n", FsOptions{}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// git status — untracked.
	status, err := sbx.Git.Status(ctx, GitOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var foundUntracked bool
	for _, f := range status.Untracked {
		if strings.Contains(f, "README.md") {
			foundUntracked = true
			break
		}
	}
	if !foundUntracked {
		t.Fatalf("README.md should be untracked, got %+v", status)
	}

	if _, err := sbx.Git.Add(ctx, ".", GitOptions{RepoPath: repo}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := sbx.Git.Commit(ctx, "init", GitOptions{RepoPath: repo}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Status should now be clean.
	status, err = sbx.Git.Status(ctx, GitOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Status after commit: %v", err)
	}
	if len(status.Modified)+len(status.Staged)+len(status.Untracked) != 0 {
		t.Fatalf("working tree should be clean: %+v", status)
	}
}

func TestIntegrationGitBranches(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()
	ensureGitConfig(t, sbx)

	repo := "/tmp/branches-" + sbx.ID
	setup := "mkdir -p " + repo + " && cd " + repo +
		" && git init -b main && echo x > a && git add a && git commit -m init"
	if h, err := sbx.Commands.Run(ctx, "sh", RunOptions{Args: []string{"-c", setup}}); err != nil {
		t.Fatal(err)
	} else if _, err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	// Create and check out a new branch.
	if _, err := sbx.Git.CreateBranch(ctx, "feature", GitOptions{RepoPath: repo}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := sbx.Git.CheckoutBranch(ctx, "feature", GitOptions{RepoPath: repo}); err != nil {
		t.Fatalf("CheckoutBranch: %v", err)
	}

	b, err := sbx.Git.Branches(ctx, GitOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if b.Current != "feature" {
		t.Fatalf("Current: %q", b.Current)
	}
	var foundMain bool
	for _, l := range b.Local {
		if l == "main" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Fatalf("main branch missing: %+v", b.Local)
	}
}
