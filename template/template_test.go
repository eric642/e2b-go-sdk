package template

import (
	"strings"
	"testing"
)

func TestNewDefaultsToE2BBase(t *testing.T) {
	if got := New().BaseImage(); got != "e2bdev/base" {
		t.Fatalf("default base image: %q", got)
	}
}

func TestFromBaseImage(t *testing.T) {
	b := New().FromImage("alpine:3").FromBaseImage()
	if b.BaseImage() != "e2bdev/base" {
		t.Fatalf("FromBaseImage not reset: %s", b.BaseImage())
	}
}

func TestFromPythonImageDefault(t *testing.T) {
	if got := New().FromPythonImage("").BaseImage(); got != "python:3" {
		t.Fatalf("got %q", got)
	}
}

func TestFromPythonImageExplicit(t *testing.T) {
	if got := New().FromPythonImage("3.12").BaseImage(); got != "python:3.12" {
		t.Fatalf("got %q", got)
	}
}

func TestFromNodeImageDefault(t *testing.T) {
	if got := New().FromNodeImage("").BaseImage(); got != "node:lts" {
		t.Fatalf("got %q", got)
	}
}

func TestFromGoImageDefault(t *testing.T) {
	if got := New().FromGoImage("").BaseImage(); got != "golang:1" {
		t.Fatalf("got %q", got)
	}
}

func TestFromTemplateClearsImage(t *testing.T) {
	b := New().FromImage("alpine:3").FromTemplate("tpl-1")
	if b.BaseImage() != "" {
		t.Fatalf("FromTemplate should clear BaseImage, got %q", b.BaseImage())
	}
	if b.BaseTemplate() != "tpl-1" {
		t.Fatalf("BaseTemplate: %q", b.BaseTemplate())
	}
}

func TestFromDockerfileAppendsRaw(t *testing.T) {
	df, err := New().FromDockerfile("FROM alpine\nRUN ls\n").ToDockerfile()
	if err != nil {
		t.Fatal(err)
	}
	// FromDockerfile now parses the content: the FROM line overrides the
	// default base image ("e2bdev/base"), and RUN becomes a structured
	// instruction. ToDockerfile() then re-renders both.
	if !strings.Contains(df, "FROM alpine") || !strings.Contains(df, "RUN ls") {
		t.Fatalf("raw block missing: %s", df)
	}
}

func TestSetStartCmdStoresBoth(t *testing.T) {
	ready := WaitForPort(3000)
	f := New().SetStartCmd("node server.js", ready)
	b := f.Builder()
	if b.StartCmd() != "node server.js" {
		t.Fatalf("StartCmd: %q", b.StartCmd())
	}
	if b.ReadyCmdString() != ready.Cmd() {
		t.Fatalf("ReadyCmdString: %q vs %q", b.ReadyCmdString(), ready.Cmd())
	}
}

func TestSetStartCmdWithoutReady(t *testing.T) {
	f := New().SetStartCmd("run", ReadyCmd{})
	b := f.Builder()
	if b.StartCmd() != "run" {
		t.Fatalf("StartCmd: %q", b.StartCmd())
	}
	if b.ReadyCmdString() != "" {
		t.Fatalf("ReadyCmdString should be empty for zero ReadyCmd, got %q", b.ReadyCmdString())
	}
}

func TestSetReadyCmdOnly(t *testing.T) {
	r := WaitForFile("/tmp/ready")
	f := New().SetReadyCmd(r)
	b := f.Builder()
	if b.ReadyCmdString() != r.Cmd() {
		t.Fatalf("ReadyCmdString: %q vs %q", b.ReadyCmdString(), r.Cmd())
	}
}

func TestSetTagRoundTrip(t *testing.T) {
	b := New().SetTag("v2.0")
	if b.Tag() != "v2.0" {
		t.Fatalf("Tag: %q", b.Tag())
	}
}

func TestToDockerfileWithoutBaseImage(t *testing.T) {
	// A builder with baseImage="" (after FromTemplate, reset via another call)
	// still renders the RUN lines. In practice FromTemplate disallows serialization;
	// this exercises the "no FROM" edge case via a manual setup.
	b := New().FromImage("").Run("echo hi")
	df, err := b.ToDockerfile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(df, "FROM") {
		t.Fatalf("should emit no FROM line: %s", df)
	}
	if !strings.Contains(df, "RUN echo hi") {
		t.Fatalf("missing RUN: %s", df)
	}
}

