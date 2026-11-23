package template

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e2b "github.com/eric642/e2b-go-sdk"
)

func TestFromDockerfileContent_SimpleFrom(t *testing.T) {
	b := New().FromDockerfileContent("FROM python:3.12\nRUN pip install numpy\n")
	if b.baseImage != "python:3.12" {
		t.Fatalf("baseImage: %q", b.baseImage)
	}
	if len(b.instructions) != 1 || b.instructions[0].Type != instTypeRun {
		t.Fatalf("instructions: %+v", b.instructions)
	}
	if b.instructions[0].Args[0] != "pip install numpy" {
		t.Fatalf("RUN cmd: %q", b.instructions[0].Args[0])
	}
}

func TestFromDockerfileContent_SkipsCommentsAndBlanks(t *testing.T) {
	src := "# header\n\nFROM alpine\n  # indented comment\nRUN apk add curl\n"
	b := New().FromDockerfileContent(src)
	if b.baseImage != "alpine" || len(b.instructions) != 1 {
		t.Fatalf("state: %q / %+v", b.baseImage, b.instructions)
	}
}

func TestFromDockerfileContent_LineContinuationsWithCommentsAndLongLines(t *testing.T) {
	longEcho := strings.Repeat("x", 80<<10) // > 64 KiB default bufio.Scanner limit
	src := "FROM --platform=linux/amd64 alpine:3.20 AS builder\n" +
		"RUN apk update && \\\n" +
		"    # install curl inside continuation\n" +
		"    apk add --no-cache \\\n" +
		"    curl\n" +
		"RUN echo " + longEcho + "\n"

	b := New().FromAWSRegistry("old", "ak", "sk", "us-east-1").FromDockerfileContent(src)
	if b.err != nil {
		t.Fatalf("unexpected err: %v", b.err)
	}
	if b.baseImage != "alpine:3.20" {
		t.Fatalf("baseImage = %q, want alpine:3.20", b.baseImage)
	}
	if b.registryConfig != nil {
		t.Fatal("FROM in Dockerfile must reset registryConfig to nil")
	}
	if len(b.instructions) != 2 {
		t.Fatalf("got %d instructions, want 2: %+v", len(b.instructions), b.instructions)
	}
	if got := b.instructions[0].Args[0]; got != "apk update && apk add --no-cache curl" {
		t.Fatalf("continued RUN = %q", got)
	}
	if got := b.instructions[1].Args[0]; got != "echo "+longEcho {
		t.Fatalf("long RUN length = %d, want %d", len(got), len("echo "+longEcho))
	}
}

func TestFromDockerfileContent_RejectsMultiStageAndCopyFrom(t *testing.T) {
	b1 := New().FromDockerfileContent("FROM golang:1.26 AS build\nFROM alpine:3\n")
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](b1.err); !ok {
		t.Fatalf("multi-stage FROM: want *e2b.InvalidArgumentError, got %T: %v", b1.err, b1.err)
	}

	b2 := New().FromDockerfileContent("FROM alpine:3\nCOPY --from=build /bin/app /app\n")
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](b2.err); !ok {
		t.Fatalf("COPY --from: want *e2b.InvalidArgumentError, got %T: %v", b2.err, b2.err)
	}
}

func TestFromDockerfileContent_CopyTwoTokensAndFlagsAndMultiSrc(t *testing.T) {
	b := New().FromDockerfileContent(`FROM alpine
COPY app.py /app/
COPY --chown=user:user --chmod=0755 a.py b.py /opt/
`)
	if b.err != nil {
		t.Fatalf("unexpected err: %v", b.err)
	}
	if len(b.instructions) != 3 {
		t.Fatalf("expected 3 COPY instructions, got %+v", b.instructions)
	}
	if got := b.instructions[0].Args; got[0] != "app.py" || got[1] != "/app/" || got[2] != "" || got[3] != "" {
		t.Fatalf("copy 0 args: %v", got)
	}
	if got := b.instructions[1].Args; got[0] != "a.py" || got[1] != "/opt/" || got[2] != "user:user" || got[3] != "0755" {
		t.Fatalf("copy 1 args: %v", got)
	}
	if got := b.instructions[2].Args; got[0] != "b.py" || got[1] != "/opt/" || got[2] != "user:user" || got[3] != "0755" {
		t.Fatalf("copy 2 args: %v", got)
	}

	bBad := New().FromDockerfileContent("FROM alpine\nCOPY --chmod=999 a.py /app/\n")
	if _, ok := errors.AsType[*e2b.InvalidArgumentError](bBad.err); !ok {
		t.Fatalf("invalid --chmod: want *e2b.InvalidArgumentError, got %T: %v", bBad.err, bBad.err)
	}
}

func TestFromDockerfileContent_EnvKVAndLegacyForms(t *testing.T) {
	b := New().FromDockerfileContent(`FROM alpine
ENV FOO=bar BAZ="qux quux" SINGLE='hello world'
ENV LEGACY_KEY legacy value with spaces
ENV LEGACY_QUOTED "quoted legacy value"
`)
	if b.err != nil {
		t.Fatalf("unexpected err: %v", b.err)
	}
	if len(b.instructions) != 3 {
		t.Fatalf("expected 3 ENV instructions, got %+v", b.instructions)
	}
	toMap := func(args []string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(args); i += 2 {
			m[args[i]] = args[i+1]
		}
		return m
	}
	kv := toMap(b.instructions[0].Args)
	if kv["FOO"] != "bar" || kv["BAZ"] != "qux quux" || kv["SINGLE"] != "hello world" {
		t.Fatalf("kv env = %#v", kv)
	}
	leg := toMap(b.instructions[1].Args)
	if leg["LEGACY_KEY"] != "legacy value with spaces" {
		t.Fatalf("legacy env = %#v", leg)
	}
	legQ := toMap(b.instructions[2].Args)
	if legQ["LEGACY_QUOTED"] != "quoted legacy value" {
		t.Fatalf("legacy quoted env = %#v", legQ)
	}
}

func TestFromDockerfileContent_WorkdirUser(t *testing.T) {
	b := New().FromDockerfileContent("FROM alpine\nWORKDIR /app\nUSER root\n")
	if len(b.instructions) != 2 {
		t.Fatalf("got %d", len(b.instructions))
	}
	if b.instructions[0].Type != instTypeWorkdir || b.instructions[0].Args[0] != "/app" {
		t.Fatalf("workdir: %+v", b.instructions[0])
	}
	if b.instructions[1].Type != instTypeUser || b.instructions[1].Args[0] != "root" {
		t.Fatalf("user: %+v", b.instructions[1])
	}
}

func TestFromDockerfileContent_IgnoresUnsupported(t *testing.T) {
	b := New().FromDockerfileContent("FROM alpine\nLABEL key=value\nHEALTHCHECK NONE\nRUN ls\n")
	if len(b.instructions) != 1 || b.instructions[0].Type != instTypeRun {
		t.Fatalf("expected one RUN, got %+v", b.instructions)
	}
}

func TestFromDockerfileFile_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM alpine\nRUN apk add curl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := New().FromDockerfileFile(p)
	if b.baseImage != "alpine" {
		t.Fatalf("baseImage: %q", b.baseImage)
	}
}

func TestFromDockerfileFile_MissingSetsE2BError(t *testing.T) {
	b := New().FromDockerfileFile("/definitely/not/there.Dockerfile")
	if b.err == nil {
		t.Fatal("expected err from missing file to be stored")
	}
	if _, ok := errors.AsType[e2b.Error](b.err); !ok {
		t.Fatalf("missing Dockerfile error must satisfy e2b.Error, got %T: %v", b.err, b.err)
	}
	if !errors.Is(b.err, os.ErrNotExist) {
		t.Fatalf("missing Dockerfile error should wrap os.ErrNotExist, got %v", b.err)
	}
}
