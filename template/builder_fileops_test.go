package template

import "testing"

func TestRemove_ShellEscape(t *testing.T) {
	b := New().Remove([]string{"/tmp/cache"},
		WithRemoveRecursive(), WithRemoveForce(), WithRemoveUser("root"))
	if len(b.instructions) != 1 {
		t.Fatalf("got %d", len(b.instructions))
	}
	in := b.instructions[0]
	if in.Type != instTypeRun {
		t.Fatal("not a RUN")
	}
	if in.Args[0] != "rm -r -f /tmp/cache" {
		t.Fatalf("got %q", in.Args[0])
	}
	if in.Args[1] != "root" {
		t.Fatalf("user: %q", in.Args[1])
	}
}

func TestRemove_Defaults(t *testing.T) {
	b := New().Remove([]string{"/tmp/a", "/tmp/b"})
	if b.instructions[0].Args[0] != "rm /tmp/a /tmp/b" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
	if len(b.instructions[0].Args) != 1 {
		t.Fatalf("no user expected, got %d args", len(b.instructions[0].Args))
	}
}

func TestRename_Force(t *testing.T) {
	b := New().Rename("/a", "/b", WithRenameForce())
	if b.instructions[0].Args[0] != "mv /a /b -f" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestRename_Plain(t *testing.T) {
	b := New().Rename("/a", "/b")
	if b.instructions[0].Args[0] != "mv /a /b" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestMakeDir_WithMode(t *testing.T) {
	b := New().MakeDir([]string{"/a", "/b"}, WithMkdirMode(0o755))
	if b.instructions[0].Args[0] != "mkdir -p -m 0755 /a /b" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestMakeDir_NoMode(t *testing.T) {
	b := New().MakeDir([]string{"/a"})
	if b.instructions[0].Args[0] != "mkdir -p /a" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestMakeSymlink_Force(t *testing.T) {
	b := New().MakeSymlink("/usr/bin/python3", "/usr/bin/python", WithSymlinkForce())
	if b.instructions[0].Args[0] != "ln -s -f /usr/bin/python3 /usr/bin/python" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestMakeSymlink_Plain(t *testing.T) {
	b := New().MakeSymlink("/usr/bin/python3", "/usr/bin/python")
	if b.instructions[0].Args[0] != "ln -s /usr/bin/python3 /usr/bin/python" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestFileOps_SkipCacheIsStickyForSubsequentSteps(t *testing.T) {
	b := New().MakeDir([]string{"/before"}).SkipCache().Remove([]string{"/a"}).MakeDir([]string{"/b"})
	if b.instructions[0].Force {
		t.Fatalf("step before SkipCache should not have Force: %+v", b.instructions[0])
	}
	if !b.instructions[1].Force || !b.instructions[2].Force {
		t.Fatalf("SkipCache should be sticky for all subsequent steps, got force flags: %v %v",
			b.instructions[1].Force, b.instructions[2].Force)
	}
}

func TestFileOps_ShellQuotesUnsafePaths(t *testing.T) {
	b := New().
		Remove([]string{"/tmp/my dir; rm -rf /"}, WithRemoveForce()).
		Rename("/a b", "/c'd").
		MakeDir([]string{"/dir with spaces"}).
		MakeSymlink("/src path", "/dst path")
	if got := b.instructions[0].Args[0]; got != "rm -f '/tmp/my dir; rm -rf /'" {
		t.Fatalf("Remove quote: %q", got)
	}
	if got := b.instructions[1].Args[0]; got != `mv '/a b' '/c'"'"'d'` {
		t.Fatalf("Rename quote: %q", got)
	}
	if got := b.instructions[2].Args[0]; got != "mkdir -p '/dir with spaces'" {
		t.Fatalf("MakeDir quote: %q", got)
	}
	if got := b.instructions[3].Args[0]; got != "ln -s '/src path' '/dst path'" {
		t.Fatalf("MakeSymlink quote: %q", got)
	}
}
