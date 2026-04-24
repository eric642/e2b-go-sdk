// Package template provides a fluent builder for E2B sandbox templates.
// It mirrors the public surface of the Python Template and JS TemplateBase
// classes. The builder accumulates instructions that can be serialized to a
// Dockerfile.
//
// Note: the server-side build orchestration (tar packaging, file hash
// pre-check, multi-phase upload, build polling) is not implemented in v1;
// callers can serialize a template with ToDockerfile and build it through
// the existing `e2b` CLI until the Go Build() lands.
package template

import (
	"context"
	"fmt"
	"strings"

	"github.com/eric642/e2b-go-sdk"
)

// Builder constructs a template.
type Builder struct {
	baseImage    string
	baseTemplate string
	startCmd     string
	readyCmd     string
	instructions []instruction
	envs         map[string]string
	workdir      string
	tag          string
}

// instruction is an internal representation of one Dockerfile line.
type instruction struct {
	op   string
	args []string
}

// New returns an empty builder. Use FromImage / FromDockerfile to specify a
// base.
func New() *Builder {
	return &Builder{baseImage: "e2bdev/base"}
}

// FromImage sets the base Docker image.
func (b *Builder) FromImage(image string) *Builder {
	b.baseImage = image
	b.baseTemplate = ""
	return b
}

// FromBaseImage resets to the default e2bdev/base image.
func (b *Builder) FromBaseImage() *Builder {
	return b.FromImage("e2bdev/base")
}

// FromPythonImage uses the official python image with the given tag
// (default "3").
func (b *Builder) FromPythonImage(tag string) *Builder {
	if tag == "" {
		tag = "3"
	}
	return b.FromImage("python:" + tag)
}

// FromNodeImage uses the official node image.
func (b *Builder) FromNodeImage(tag string) *Builder {
	if tag == "" {
		tag = "lts"
	}
	return b.FromImage("node:" + tag)
}

// FromGoImage uses the official golang image.
func (b *Builder) FromGoImage(tag string) *Builder {
	if tag == "" {
		tag = "1"
	}
	return b.FromImage("golang:" + tag)
}

// FromTemplate bases this template on another E2B template.
func (b *Builder) FromTemplate(templateID string) *Builder {
	b.baseTemplate = templateID
	b.baseImage = ""
	return b
}

// FromDockerfile replaces the builder with a raw Dockerfile.
func (b *Builder) FromDockerfile(contents string) *Builder {
	b.instructions = append(b.instructions, instruction{op: "__raw", args: []string{contents}})
	return b
}

// Run adds a RUN instruction.
func (b *Builder) Run(cmd string) *Builder {
	b.instructions = append(b.instructions, instruction{op: "RUN", args: []string{cmd}})
	return b
}

// Copy adds a COPY instruction from the template build context.
func (b *Builder) Copy(src, dst string) *Builder {
	b.instructions = append(b.instructions, instruction{op: "COPY", args: []string{src, dst}})
	return b
}

// Workdir sets the working directory. It records a WORKDIR instruction and
// persists the value for later fluent calls.
func (b *Builder) Workdir(path string) *Builder {
	b.workdir = path
	b.instructions = append(b.instructions, instruction{op: "WORKDIR", args: []string{path}})
	return b
}

// Env sets an environment variable.
func (b *Builder) Env(key, value string) *Builder {
	if b.envs == nil {
		b.envs = map[string]string{}
	}
	b.envs[key] = value
	b.instructions = append(b.instructions, instruction{op: "ENV", args: []string{key, value}})
	return b
}

// Expose documents a port.
func (b *Builder) Expose(port int) *Builder {
	b.instructions = append(b.instructions, instruction{op: "EXPOSE", args: []string{fmt.Sprintf("%d", port)}})
	return b
}

// Entrypoint sets the container entrypoint.
func (b *Builder) Entrypoint(cmd string) *Builder {
	b.instructions = append(b.instructions, instruction{op: "ENTRYPOINT", args: []string{cmd}})
	return b
}

// SetStartCmd records the command run when a sandbox boots, plus an
// optional readiness check.
func (b *Builder) SetStartCmd(cmd string, ready *ReadyCmd) *Builder {
	b.startCmd = cmd
	if ready != nil {
		b.readyCmd = ready.cmd
	}
	return b
}

// SetReadyCmd sets only the readiness check.
func (b *Builder) SetReadyCmd(ready ReadyCmd) *Builder {
	b.readyCmd = ready.cmd
	return b
}

// SetTag tags the final template (e.g. "my-template:v2").
func (b *Builder) SetTag(tag string) *Builder {
	b.tag = tag
	return b
}

// StartCmd returns the configured start command.
func (b *Builder) StartCmd() string { return b.startCmd }

// ReadyCmdString returns the configured readiness command.
func (b *Builder) ReadyCmdString() string { return b.readyCmd }

// BaseImage returns the base image (empty when building on an E2B template).
func (b *Builder) BaseImage() string { return b.baseImage }

// BaseTemplate returns the base template ID (empty when building on an image).
func (b *Builder) BaseTemplate() string { return b.baseTemplate }

// Tag returns the tag associated with this template build.
func (b *Builder) Tag() string { return b.tag }

// ToDockerfile serializes the builder to a Dockerfile. Templates based on
// another E2B template cannot be serialized and return an error.
func (b *Builder) ToDockerfile() (string, error) {
	if b.baseTemplate != "" {
		return "", &e2b.InvalidArgumentError{Message: "templates based on other E2B templates cannot be serialized to Dockerfile"}
	}
	var sb strings.Builder
	if b.baseImage != "" {
		fmt.Fprintf(&sb, "FROM %s\n", b.baseImage)
	}
	for _, ins := range b.instructions {
		switch ins.op {
		case "__raw":
			sb.WriteString(ins.args[0])
			if !strings.HasSuffix(ins.args[0], "\n") {
				sb.WriteByte('\n')
			}
		case "RUN":
			fmt.Fprintf(&sb, "RUN %s\n", ins.args[0])
		case "COPY":
			fmt.Fprintf(&sb, "COPY %s %s\n", ins.args[0], ins.args[1])
		case "WORKDIR":
			fmt.Fprintf(&sb, "WORKDIR %s\n", ins.args[0])
		case "ENV":
			fmt.Fprintf(&sb, "ENV %s=%q\n", ins.args[0], ins.args[1])
		case "EXPOSE":
			fmt.Fprintf(&sb, "EXPOSE %s\n", ins.args[0])
		case "ENTRYPOINT":
			fmt.Fprintf(&sb, "ENTRYPOINT %s\n", ins.args[0])
		}
	}
	return sb.String(), nil
}

// BuildOptions configures a server-side build.
type BuildOptions struct {
	Config     e2b.Config
	CPUCount   int32
	MemoryMB   int32
	SkipCache  bool
	OnLogEntry func(LogEntry)
}

// BuildInfo holds the result of a successful build.
type BuildInfo struct {
	TemplateID string
	BuildID    string
	Aliases    []string
}

// Build submits the template for remote build and waits for completion.
// Not yet implemented; returns e2b.ErrNotImplemented.
func (b *Builder) Build(ctx context.Context, opts BuildOptions) (*BuildInfo, error) {
	return nil, e2b.ErrNotImplemented
}
