package template

import (
	"errors"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestAddMCPServer_RequiresMCPGatewayBase(t *testing.T) {
	b := New().FromImage("alpine:3").AddMCPServer([]string{"brave"})
	if _, err := b.serialize(false); err == nil {
		t.Fatal("expected error because base isn't mcp-gateway")
	}
	var invalid *e2b.InvalidArgumentError
	_, err := b.serialize(false)
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
	if len(b.instructions) != 0 {
		t.Fatalf("should not emit any instruction, got %d", len(b.instructions))
	}
}

func TestAddMCPServer_Pulls(t *testing.T) {
	b := New().FromTemplate("mcp-gateway").AddMCPServer([]string{"brave", "exa"})
	in := b.instructions[0]
	if in.Type != instTypeRun {
		t.Fatalf("type: %s", in.Type)
	}
	if in.Args[0] != "mcp-gateway pull brave exa" {
		t.Fatalf("cmd: %q", in.Args[0])
	}
	if in.Args[1] != "root" {
		t.Fatalf("should run as root: %v", in.Args)
	}
}

func TestBetaDevContainerPrebuild_RequiresDevcontainerBase(t *testing.T) {
	b := New().FromImage("alpine:3").BetaDevContainerPrebuild("/work")
	if _, err := b.serialize(false); err == nil {
		t.Fatal("expected error for wrong base")
	}
	if len(b.instructions) != 0 {
		t.Fatalf("should not emit instruction on wrong base")
	}
}

func TestBetaDevContainerPrebuild_Valid(t *testing.T) {
	b := New().FromTemplate("devcontainer").BetaDevContainerPrebuild("/work")
	in := b.instructions[0]
	if in.Args[0] != "devcontainer build --workspace-folder /work" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}

func TestBetaMethods_DontOverwritePriorErr(t *testing.T) {
	// First validation error comes from Copy("/abs"). Second AddMCPServer with
	// wrong base must not overwrite it.
	b := New().FromImage("alpine:3").Copy("/abs/path", "/dst").AddMCPServer([]string{"brave"})
	_, err := b.serialize(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := err.Error(); !contains(msg, "must be a relative path") {
		t.Fatalf("expected first error (absolute path) to be preserved, got %q", msg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
