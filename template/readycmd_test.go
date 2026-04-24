package template

import (
	"strings"
	"testing"
)

func TestWaitForPort(t *testing.T) {
	got := WaitForPort(8080).Cmd()
	if !strings.Contains(got, ":8080") || !strings.Contains(got, "ss ") {
		t.Fatalf("unexpected cmd: %q", got)
	}
}

func TestWaitForPort80(t *testing.T) {
	got := WaitForPort(80).Cmd()
	if !strings.Contains(got, ":80") {
		t.Fatalf("expected port 80, got %q", got)
	}
}

func TestWaitForURLDefaultsTo200(t *testing.T) {
	got := WaitForURL("http://localhost/health", 0).Cmd()
	if !strings.Contains(got, "200") {
		t.Fatalf("expected 200 default, got %q", got)
	}
}

func TestWaitForURLCustomStatus(t *testing.T) {
	got := WaitForURL("http://localhost/health", 204).Cmd()
	if !strings.Contains(got, "204") {
		t.Fatalf("expected 204, got %q", got)
	}
	if strings.Contains(got, "200") {
		t.Fatalf("custom status 204 should not emit 200: %q", got)
	}
}

func TestWaitForProcess(t *testing.T) {
	got := WaitForProcess("nginx").Cmd()
	if got != "pgrep nginx > /dev/null" {
		t.Fatalf("got %q", got)
	}
}

func TestWaitForTimeoutClampsToOneSecond(t *testing.T) {
	got := WaitForTimeoutMs(10).Cmd()
	if got != "sleep 1" {
		t.Fatalf("expected sleep 1, got %q", got)
	}
}

func TestWaitForTimeoutDoesNotClampAboveOneSecond(t *testing.T) {
	got := WaitForTimeoutMs(5000).Cmd()
	if got != "sleep 5" {
		t.Fatalf("expected sleep 5, got %q", got)
	}
}

func TestWaitForFile(t *testing.T) {
	got := WaitForFile("/tmp/ready").Cmd()
	if got != "[ -f /tmp/ready ]" {
		t.Fatalf("got %q", got)
	}
}

func TestBuilderToDockerfile(t *testing.T) {
	df, err := New().
		FromImage("alpine:3").
		Workdir("/app").
		Env("FOO", "bar").
		Run("apk add --no-cache curl").
		Copy("./src", "/app/src").
		Expose(8080).
		Entrypoint("/app/server").
		ToDockerfile()
	if err != nil {
		t.Fatalf("ToDockerfile: %v", err)
	}
	for _, want := range []string{
		"FROM alpine:3",
		"WORKDIR /app",
		"ENV FOO=\"bar\"",
		"RUN apk add --no-cache curl",
		"COPY ./src /app/src",
		"EXPOSE 8080",
		"ENTRYPOINT /app/server",
	} {
		if !strings.Contains(df, want) {
			t.Fatalf("expected %q in dockerfile:\n%s", want, df)
		}
	}
}

func TestBuilderFromTemplateCannotSerialize(t *testing.T) {
	if _, err := New().FromTemplate("some-template-id").ToDockerfile(); err == nil {
		t.Fatal("expected error for FromTemplate")
	}
}
