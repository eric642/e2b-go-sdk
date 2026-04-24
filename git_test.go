package e2b

import (
	"errors"
	"testing"
)

func TestParseGitStatus(t *testing.T) {
	out := " M file1\nA  file2\n?? file3\nMM file4\n"
	s := parseGitStatus(out)
	// file1 has modified (y=M)
	if len(s.Modified) == 0 || s.Modified[0] != "file1" {
		t.Fatalf("unexpected modified: %+v", s.Modified)
	}
	// file2 is staged only
	foundFile2 := false
	for _, f := range s.Staged {
		if f == "file2" {
			foundFile2 = true
		}
	}
	if !foundFile2 {
		t.Fatalf("file2 should be staged: %+v", s.Staged)
	}
	// file3 is untracked
	if len(s.Untracked) == 0 || s.Untracked[0] != "file3" {
		t.Fatalf("unexpected untracked: %+v", s.Untracked)
	}
	// file4 is both staged and modified
	foundFile4Staged := false
	foundFile4Modified := false
	for _, f := range s.Staged {
		if f == "file4" {
			foundFile4Staged = true
		}
	}
	for _, f := range s.Modified {
		if f == "file4" {
			foundFile4Modified = true
		}
	}
	if !foundFile4Staged || !foundFile4Modified {
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
	// Lines shorter than 3 chars (status code + space) should be ignored.
	s := parseGitStatus("\n \nMM\n")
	if len(s.Modified)+len(s.Staged) != 0 {
		t.Fatalf("short lines should be skipped, got %+v", s)
	}
}

func TestParseGitStatusConflict(t *testing.T) {
	// `UU path` means both sides modified — in the porcelain layout it lands
	// in Staged (x != ' ') and Modified (y != ' '), mirroring our generic
	// both-dirty rule.
	s := parseGitStatus("UU merge.go\n")
	var gotStaged, gotModified bool
	for _, f := range s.Staged {
		if f == "merge.go" {
			gotStaged = true
		}
	}
	for _, f := range s.Modified {
		if f == "merge.go" {
			gotModified = true
		}
	}
	if !gotStaged || !gotModified {
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
	// git uses a parenthesised label for detached HEAD. Our parser treats
	// everything after "* " as the current branch.
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

func TestWithCreds(t *testing.T) {
	if got := withCreds("https://github.com/me/r.git", "u", "p"); got != "https://u:p@github.com/me/r.git" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := withCreds("git@github.com:me/r.git", "u", "p"); got != "git@github.com:me/r.git" {
		t.Fatalf("ssh URL should be untouched: %s", got)
	}
}

func TestWithCredsSSHSchemeUntouched(t *testing.T) {
	in := "ssh://git@github.com/me/r.git"
	if got := withCreds(in, "u", "p"); got != in {
		t.Fatalf("ssh:// scheme must be untouched, got %s", got)
	}
}

func TestWithCredsEmptyCredsReturnsOriginal(t *testing.T) {
	in := "https://github.com/me/r.git"
	if got := withCreds(in, "", ""); got != in {
		t.Fatalf("empty creds must return original, got %s", got)
	}
}

func TestWithCredsPathOnlyReturnsOriginal(t *testing.T) {
	// URL with no scheme separator — Cut returns ok=false, so original string
	// is returned untouched.
	in := "/local/path.git"
	if got := withCreds(in, "u", "p"); got != in {
		t.Fatalf("scheme-less path must be untouched, got %s", got)
	}
}

func TestClassifyGitErrAuth(t *testing.T) {
	exit := &CommandExitError{Result: CommandResult{Stderr: "fatal: Authentication failed for 'https://...'", ExitCode: 128}}
	err := classifyGitErr(exit)
	var ae *GitAuthError
	if !errors.As(err, &ae) {
		t.Fatalf("want *GitAuthError, got %T", err)
	}
}

func TestClassifyGitErrUpstream(t *testing.T) {
	exit := &CommandExitError{Result: CommandResult{Stderr: "fatal: no upstream configured", ExitCode: 128}}
	err := classifyGitErr(exit)
	var ue *GitUpstreamError
	if !errors.As(err, &ue) {
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
