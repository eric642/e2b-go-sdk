package template

import (
	"errors"
	"os"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestCopy_EncodesUserAndMode(t *testing.T) {
	b := New().Copy("src", "/dst",
		WithCopyUser("root"), WithCopyMode(0o755), WithCopyForceUpload())
	if len(b.instructions) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(b.instructions))
	}
	in := b.instructions[0]
	if in.Type != instTypeCopy {
		t.Fatalf("type: %s", in.Type)
	}
	if in.Args[0] != "src" || in.Args[1] != "/dst" || in.Args[2] != "root" || in.Args[3] != "0755" {
		t.Fatalf("args: %v", in.Args)
	}
	if !in.HasForceUpload || !in.ForceUpload {
		t.Fatalf("forceUpload missing: %+v", in)
	}
}

func TestCopy_DefaultsWhenNoOpts(t *testing.T) {
	b := New().Copy("src", "/dst")
	in := b.instructions[0]
	if in.Args[2] != "" || in.Args[3] != "" {
		t.Fatalf("user/mode should be empty by default, got %v", in.Args)
	}
	if in.HasForceUpload || in.ForceUpload {
		t.Fatalf("forceUpload should be unset by default")
	}
	if in.ResolveSymlinks != nil {
		t.Fatalf("resolveSymlinks should be nil by default")
	}
}

func TestCopy_ResolveSymlinksOption(t *testing.T) {
	b := New().Copy("src", "/dst", WithCopyResolveSymlinks(true))
	rs := b.instructions[0].ResolveSymlinks
	if rs == nil || !*rs {
		t.Fatalf("expected *true, got %v", rs)
	}
}

func TestCopy_RejectsAbsolutePath(t *testing.T) {
	ctx := t.TempDir()
	b := New().FromImage("alpine:3").WithContext(ctx).Copy("/etc/passwd", "/dst")
	_, err := b.serialize(false)
	if err == nil {
		t.Fatal("expected error for absolute src")
	}
	var invalid *e2b.InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
	// No instructions should have been appended.
	if len(b.instructions) != 0 {
		t.Fatalf("expected no instructions, got %d", len(b.instructions))
	}
}

func TestCopy_RejectsEscapingPath(t *testing.T) {
	ctx := t.TempDir()
	b := New().FromImage("alpine:3").WithContext(ctx).Copy("../escape", "/dst")
	_, err := b.serialize(false)
	if err == nil {
		t.Fatal("expected error for escaping src")
	}
}

func TestCopy_ValidationErrorPreservedAcrossCalls(t *testing.T) {
	b := New().FromImage("alpine:3").Copy("/abs", "/dst").Run("echo still recorded?")
	_, err := b.serialize(false)
	if err == nil {
		t.Fatal("expected error from earlier Copy")
	}
}

func TestCopyItems_EncodesAllEntries(t *testing.T) {
	items := []CopyItem{
		{Src: "a.py", Dest: "/app/"},
		{Src: "b.py", Dest: "/app/", Mode: os.FileMode(0o644)},
		{Src: "c.py", Dest: "/app/", User: "root", ForceUpload: true},
	}
	b := New().CopyItems(items)
	if len(b.instructions) != 3 {
		t.Fatalf("got %d instructions", len(b.instructions))
	}
	if b.instructions[0].Args[2] != "" || b.instructions[0].Args[3] != "" {
		t.Fatalf("item 0 should have empty user/mode: %v", b.instructions[0].Args)
	}
	if b.instructions[1].Args[3] != "0644" {
		t.Fatalf("item 1 mode: %v", b.instructions[1].Args)
	}
	if b.instructions[2].Args[2] != "root" || !b.instructions[2].HasForceUpload || !b.instructions[2].ForceUpload {
		t.Fatalf("item 2: %+v", b.instructions[2])
	}
}
