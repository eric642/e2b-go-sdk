package template

import (
	"slices"
	"testing"
)

func TestRunCmd_WithUser(t *testing.T) {
	b := New().RunCmd("apt-get update", WithRunUser("root"))
	in := b.instructions[0]
	if in.Type != instTypeRun {
		t.Fatalf("type: %s", in.Type)
	}
	if in.Args[0] != "apt-get update" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}

func TestRunCmd_NoUser(t *testing.T) {
	b := New().RunCmd("apt-get update")
	in := b.instructions[0]
	if len(in.Args) != 1 {
		t.Fatalf("unexpected extra args: %v", in.Args)
	}
	if in.Args[0] != "apt-get update" {
		t.Fatalf("cmd: %q", in.Args[0])
	}
}

func TestRunCmds_JoinsWithAmpAmp(t *testing.T) {
	b := New().RunCmds([]string{"a", "b"})
	if b.instructions[0].Args[0] != "a && b" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestRunCmds_PropagatesUser(t *testing.T) {
	b := New().RunCmds([]string{"a", "b"}, WithRunUser("root"))
	in := b.instructions[0]
	if in.Args[0] != "a && b" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}

func TestSetWorkdir(t *testing.T) {
	b := New().SetWorkdir("/app")
	in := b.instructions[0]
	if in.Type != instTypeWorkdir || in.Args[0] != "/app" {
		t.Fatalf("%+v", in)
	}
}

func TestSetUser(t *testing.T) {
	b := New().SetUser("root")
	in := b.instructions[0]
	if in.Type != instTypeUser || in.Args[0] != "root" {
		t.Fatalf("%+v", in)
	}
}

func TestSetEnvs_InterleavedArgs(t *testing.T) {
	b := New().SetEnvs(map[string]string{"A": "1", "B": "2"})
	if len(b.instructions) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(b.instructions))
	}
	in := b.instructions[0]
	if in.Type != instTypeEnv || len(in.Args) != 4 {
		t.Fatalf("%+v", in)
	}
	set := map[string]string{}
	for i := 0; i+1 < len(in.Args); i += 2 {
		set[in.Args[i]] = in.Args[i+1]
	}
	if set["A"] != "1" || set["B"] != "2" {
		t.Fatalf("envs: %v", set)
	}
}

func TestSetEnvs_EmptyMapNoOp(t *testing.T) {
	b := New().SetEnvs(nil)
	if len(b.instructions) != 0 {
		t.Fatalf("expected no instruction, got %d", len(b.instructions))
	}
}

func TestSetWorkdirUser_SkipCacheIsSticky(t *testing.T) {
	b := New().RunCmd("before").SkipCache().SetWorkdir("/app").SetUser("root").Env("K", "V")
	if b.instructions[0].Force {
		t.Fatalf("step before SkipCache should have Force=false")
	}
	for i := 1; i < len(b.instructions); i++ {
		if !b.instructions[i].Force {
			t.Fatalf("step %d after SkipCache should have Force=true: %+v", i, b.instructions[i])
		}
	}
}

func TestSetEnvs_DeterministicSortedKeyOrder(t *testing.T) {
	envs := map[string]string{
		"ZEBRA":  "26",
		"ALPHA":  "1",
		"MIDDLE": "13",
		"BETA":   "2",
	}
	want := []string{"ALPHA", "1", "BETA", "2", "MIDDLE", "13", "ZEBRA", "26"}
	for range 50 {
		b := New().SetEnvs(envs)
		got := b.instructions[0].Args
		if !slices.Equal(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
