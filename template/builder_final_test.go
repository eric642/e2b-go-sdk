package template

import (
	"errors"
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

func TestFinalBuilder_BuilderAccess(t *testing.T) {
	f := New().FromImage("alpine:3").SetReadyCmd(WaitForFile("/tmp/ready"))
	if f.Builder() == nil {
		t.Fatal("Builder() should return the underlying builder")
	}
}

func TestBetaSetDevContainerStart_RequiresDevcontainerBase(t *testing.T) {
	f := New().FromImage("alpine:3").BetaSetDevContainerStart("/work")
	_, err := f.Builder().serialize(false)
	if err == nil {
		t.Fatal("expected error for wrong base")
	}
	var invalid *e2b.InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestBetaSetDevContainerStart_Valid(t *testing.T) {
	f := New().FromTemplate("devcontainer").BetaSetDevContainerStart("/work")
	b := f.Builder()
	if b.startCmd == "" || b.readyCmd == "" {
		t.Fatalf("start/ready not set: start=%q ready=%q", b.startCmd, b.readyCmd)
	}
	if !containsFinal(b.startCmd, "devcontainer up --workspace-folder /work") {
		t.Fatalf("startCmd missing dir: %q", b.startCmd)
	}
	if !containsFinal(b.readyCmd, "/devcontainer.up") {
		t.Fatalf("readyCmd missing marker file: %q", b.readyCmd)
	}
}

func containsFinal(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
