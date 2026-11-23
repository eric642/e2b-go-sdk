package template

import (
	"errors"
	"strings"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestSetStartCmd_ReturnsFinalBuilder(t *testing.T) {
	f := New().FromImage("alpine:3").SetStartCmd("run", WaitForPort(8000))
	if f == nil {
		t.Fatal("nil FinalBuilder")
	}
	if _, err := f.ToDockerfile(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalBuilder_BuilderAccessAndSnapshotIsolation(t *testing.T) {
	b := New().FromImage("alpine:3").RunCmd("echo initial")
	f := b.SetReadyCmd(WaitForFile("/tmp/ready"))
	if f.Builder() == nil {
		t.Fatal("Builder() should return a non-nil builder")
	}

	// Mutating the original Builder after finalization must not affect FinalBuilder.
	b.RunCmd("echo mutated").SetTag("mutated:tag")
	if f.Tag() != "" {
		t.Fatalf("FinalBuilder tag aliased original Builder: %q", f.Tag())
	}
	df, err := f.ToDockerfile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(df, "echo mutated") {
		t.Fatalf("FinalBuilder aliased original Builder instructions: %s", df)
	}

	// Mutating the Builder returned by f.Builder() must not affect FinalBuilder either.
	unwrapped := f.Builder()
	unwrapped.RunCmd("echo unwrapped")
	df2, err := f.ToDockerfile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(df2, "echo unwrapped") {
		t.Fatalf("FinalBuilder aliased Builder() return value: %s", df2)
	}
}

func TestBetaSetDevContainerStart_RequiresDevcontainerBase(t *testing.T) {
	f := New().FromImage("alpine:3").BetaSetDevContainerStart("/work")
	_, err := f.Builder().serialize(false)
	if err == nil {
		t.Fatal("expected error for wrong base")
	}
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](err); !ok {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestBetaSetDevContainerStart_Valid(t *testing.T) {
	f := New().FromTemplate("devcontainer").BetaSetDevContainerStart("/work")
	b := f.Builder()
	if b.startCmd == "" || b.readyCmd == "" {
		t.Fatalf("start/ready not set: start=%q ready=%q", b.startCmd, b.readyCmd)
	}
	if !strings.Contains(b.startCmd, "devcontainer up --workspace-folder /work") {
		t.Fatalf("startCmd missing dir: %q", b.startCmd)
	}
	if !strings.Contains(b.readyCmd, "/devcontainer.up") {
		t.Fatalf("readyCmd missing marker file: %q", b.readyCmd)
	}
}
