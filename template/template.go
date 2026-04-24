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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	e2b "github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// instType enumerates the kinds of instructions the Builder can record. It
// maps 1:1 to the server-side TemplateStep.Type string (except for instTypeRaw
// which is internal to ToDockerfile rendering).
type instType string

const (
	instTypeRun     instType = "RUN"
	instTypeCopy    instType = "COPY"
	instTypeWorkdir instType = "WORKDIR"
	instTypeEnv     instType = "ENV"
	instTypeUser    instType = "USER"
	instTypeExpose  instType = "EXPOSE"
	// instTypeRaw is an internal-only marker for raw Dockerfile text captured
	// via FromDockerfile; it has no server-side TemplateStep representation
	// and is skipped during serialize().
	instTypeRaw instType = "__raw"
)

// defaultResolveSymlinks is the default for per-COPY symlink resolution when
// callers don't opt in. Kept as a package-level constant for parity with the
// Python SDK.
const defaultResolveSymlinks = false

// instruction is a structured record of a single builder step.
type instruction struct {
	Type            instType
	Args            []string
	Force           bool
	ForceUpload     bool
	HasForceUpload  bool // whether ForceUpload was explicitly set
	ResolveSymlinks *bool
	FilesHash       string
}

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

	// Build-context and server-side build state.
	contextDir     string
	ignorePatterns []string
	forceNextLayer bool
	force          bool
	err            error
	registryConfig *apiclient.FromImageRegistry
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
	b.instructions = append(b.instructions, instruction{Type: instTypeRaw, Args: []string{contents}, Force: b.consumeForce()})
	return b
}

// WithContext sets the local directory used as the build context for COPY
// instructions. Subsequent COPY hashing and tar packaging reads files under
// this path.
func (b *Builder) WithContext(dir string) *Builder {
	b.contextDir = dir
	return b
}

// WithIgnore appends patterns (in .dockerignore syntax) that filter files
// during COPY hashing and tar packaging.
func (b *Builder) WithIgnore(patterns ...string) *Builder {
	b.ignorePatterns = append(b.ignorePatterns, patterns...)
	return b
}

// Run adds a RUN instruction.
func (b *Builder) Run(cmd string) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instTypeRun, Args: []string{cmd}, Force: b.consumeForce()})
	return b
}

// CopyItem mirrors the Python SDK's CopyItem dict. All fields except Src and
// Dest are optional.
type CopyItem struct {
	Src             string
	Dest            string
	User            string
	Mode            os.FileMode
	ForceUpload     bool
	ResolveSymlinks *bool
}

// CopyOption is a functional option for Copy.
type CopyOption func(*copyOpts)

type copyOpts struct {
	user            string
	mode            os.FileMode
	hasMode         bool
	forceUpload     bool
	hasForceUpload  bool
	resolveSymlinks *bool
}

// WithCopyUser sets the owning user for the copied files (USER arg of COPY).
func WithCopyUser(u string) CopyOption {
	return func(o *copyOpts) { o.user = u }
}

// WithCopyMode sets the file mode applied to the copied files.
func WithCopyMode(m os.FileMode) CopyOption {
	return func(o *copyOpts) {
		o.mode = m
		o.hasMode = true
	}
}

// WithCopyForceUpload forces re-upload of the COPY layer even when a matching
// hash already exists on the server.
func WithCopyForceUpload() CopyOption {
	return func(o *copyOpts) {
		o.forceUpload = true
		o.hasForceUpload = true
	}
}

// WithCopyResolveSymlinks overrides the default symlink resolution behavior
// for this COPY. Pass true to follow symlinks, false to preserve them.
func WithCopyResolveSymlinks(b bool) CopyOption {
	return func(o *copyOpts) {
		v := b
		o.resolveSymlinks = &v
	}
}

// validateRelativePath rejects absolute paths and paths that escape the
// build context directory.
func validateRelativePath(src string) error {
	if filepath.IsAbs(src) {
		return &e2b.InvalidArgumentError{Message: fmt.Sprintf("copy src %q must be a relative path", src)}
	}
	normalized := filepath.ToSlash(filepath.Clean(src))
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return &e2b.InvalidArgumentError{Message: fmt.Sprintf("copy src %q escapes the context directory", src)}
	}
	return nil
}

// Copy adds a COPY instruction from the template build context. The Args
// slice carries four positional slots — src, dst, user, mode. When mode is
// unset the slot stays "", otherwise it is formatted as a zero-padded 4-digit
// octal string.
func (b *Builder) Copy(src, dst string, opts ...CopyOption) *Builder {
	if err := validateRelativePath(src); err != nil {
		if b.err == nil {
			b.err = err
		}
		return b
	}

	o := copyOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	modeStr := ""
	if o.hasMode {
		modeStr = fmt.Sprintf("%04o", o.mode)
	}

	b.instructions = append(b.instructions, instruction{
		Type:            instTypeCopy,
		Args:            []string{src, dst, o.user, modeStr},
		Force:           b.consumeForce(),
		ForceUpload:     o.forceUpload,
		HasForceUpload:  o.hasForceUpload,
		ResolveSymlinks: o.resolveSymlinks,
	})
	return b
}

// CopyItems adds a COPY instruction for each item, translating struct fields
// to CopyOptions so validation and defaults match single Copy calls.
func (b *Builder) CopyItems(items []CopyItem) *Builder {
	for _, it := range items {
		opts := make([]CopyOption, 0, 4)
		if it.User != "" {
			opts = append(opts, WithCopyUser(it.User))
		}
		if it.Mode != 0 {
			opts = append(opts, WithCopyMode(it.Mode))
		}
		if it.ForceUpload {
			opts = append(opts, WithCopyForceUpload())
		}
		if it.ResolveSymlinks != nil {
			opts = append(opts, WithCopyResolveSymlinks(*it.ResolveSymlinks))
		}
		b.Copy(it.Src, it.Dest, opts...)
	}
	return b
}

// Workdir sets the working directory. It records a WORKDIR instruction and
// persists the value for later fluent calls.
func (b *Builder) Workdir(path string) *Builder {
	b.workdir = path
	b.instructions = append(b.instructions, instruction{Type: instTypeWorkdir, Args: []string{path}, Force: b.consumeForce()})
	return b
}

// Env sets an environment variable.
func (b *Builder) Env(key, value string) *Builder {
	if b.envs == nil {
		b.envs = map[string]string{}
	}
	b.envs[key] = value
	b.instructions = append(b.instructions, instruction{Type: instTypeEnv, Args: []string{key, value}, Force: b.consumeForce()})
	return b
}

// Expose documents a port.
func (b *Builder) Expose(port int) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instTypeExpose, Args: []string{fmt.Sprintf("%d", port)}, Force: b.consumeForce()})
	return b
}

// Entrypoint sets the container entrypoint. Retained for Dockerfile-level
// parity; the server build API has no direct ENTRYPOINT step, so serialize()
// does not emit one.
func (b *Builder) Entrypoint(cmd string) *Builder {
	b.instructions = append(b.instructions, instruction{Type: instType("ENTRYPOINT"), Args: []string{cmd}, Force: b.consumeForce()})
	return b
}

// SkipCache marks the next instruction as force-rebuilt, ignoring the
// upstream cache. Chain before any instruction to invalidate just that
// layer; the flag resets after the next instruction is added.
func (b *Builder) SkipCache() *Builder {
	b.forceNextLayer = true
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
		switch ins.Type {
		case instTypeRaw:
			sb.WriteString(ins.Args[0])
			if !strings.HasSuffix(ins.Args[0], "\n") {
				sb.WriteByte('\n')
			}
		case instTypeRun:
			fmt.Fprintf(&sb, "RUN %s\n", ins.Args[0])
		case instTypeCopy:
			fmt.Fprintf(&sb, "COPY %s %s\n", ins.Args[0], ins.Args[1])
		case instTypeWorkdir:
			fmt.Fprintf(&sb, "WORKDIR %s\n", ins.Args[0])
		case instTypeEnv:
			fmt.Fprintf(&sb, "ENV %s=%q\n", ins.Args[0], ins.Args[1])
		case instTypeExpose:
			fmt.Fprintf(&sb, "EXPOSE %s\n", ins.Args[0])
		case instType("ENTRYPOINT"):
			fmt.Fprintf(&sb, "ENTRYPOINT %s\n", ins.Args[0])
		}
	}
	return sb.String(), nil
}

// consumeForce returns the current per-layer force flag and resets it.
// Called by every method that appends an instruction, so SkipCache() only
// marks the immediately following instruction.
func (b *Builder) consumeForce() bool {
	f := b.forceNextLayer
	b.forceNextLayer = false
	return f
}

// runAs appends a RUN instruction with an optional user argument. The user,
// when provided, becomes Args[1] so later serialization can route it to the
// server's RUN step user field.
func (b *Builder) runAs(cmd, user string) *Builder {
	args := []string{cmd}
	if user != "" {
		args = append(args, user)
	}
	b.instructions = append(b.instructions, instruction{
		Type:  instTypeRun,
		Args:  args,
		Force: b.consumeForce(),
	})
	return b
}

// -- Remove ---------------------------------------------------------------

// RemoveOption configures Builder.Remove.
type RemoveOption func(*removeOpts)
type removeOpts struct {
	force     bool
	recursive bool
	user      string
}

// WithRemoveForce adds -f to the emitted rm command.
func WithRemoveForce() RemoveOption { return func(o *removeOpts) { o.force = true } }

// WithRemoveRecursive adds -r to the emitted rm command.
func WithRemoveRecursive() RemoveOption { return func(o *removeOpts) { o.recursive = true } }

// WithRemoveUser runs the rm command as the given user.
func WithRemoveUser(u string) RemoveOption {
	return func(o *removeOpts) { o.user = u }
}

// Remove deletes files or directories inside the template during build.
func (b *Builder) Remove(paths []string, opts ...RemoveOption) *Builder {
	o := removeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"rm"}
	if o.recursive {
		cmd = append(cmd, "-r")
	}
	if o.force {
		cmd = append(cmd, "-f")
	}
	cmd = append(cmd, paths...)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

// -- Rename ---------------------------------------------------------------

// RenameOption configures Builder.Rename.
type RenameOption func(*renameOpts)
type renameOpts struct {
	force bool
	user  string
}

// WithRenameForce adds -f to the emitted mv command.
func WithRenameForce() RenameOption { return func(o *renameOpts) { o.force = true } }

// WithRenameUser runs the mv command as the given user.
func WithRenameUser(u string) RenameOption {
	return func(o *renameOpts) { o.user = u }
}

// Rename moves src to dest inside the template during build.
func (b *Builder) Rename(src, dest string, opts ...RenameOption) *Builder {
	o := renameOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"mv", src, dest}
	if o.force {
		cmd = append(cmd, "-f")
	}
	return b.runAs(strings.Join(cmd, " "), o.user)
}

// -- MakeDir --------------------------------------------------------------

// MkdirOption configures Builder.MakeDir.
type MkdirOption func(*mkdirOpts)
type mkdirOpts struct {
	mode    os.FileMode
	hasMode bool
	user    string
}

// WithMkdirMode sets the mode bits passed to mkdir via -m.
func WithMkdirMode(m os.FileMode) MkdirOption {
	return func(o *mkdirOpts) { o.mode = m; o.hasMode = true }
}

// WithMkdirUser runs the mkdir command as the given user.
func WithMkdirUser(u string) MkdirOption { return func(o *mkdirOpts) { o.user = u } }

// MakeDir creates one or more directories inside the template during build.
func (b *Builder) MakeDir(paths []string, opts ...MkdirOption) *Builder {
	o := mkdirOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"mkdir", "-p"}
	if o.hasMode {
		cmd = append(cmd, fmt.Sprintf("-m %04o", o.mode))
	}
	cmd = append(cmd, paths...)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

// -- MakeSymlink ----------------------------------------------------------

// SymlinkOption configures Builder.MakeSymlink.
type SymlinkOption func(*symOpts)
type symOpts struct {
	force bool
	user  string
}

// WithSymlinkForce adds -f to the emitted ln command.
func WithSymlinkForce() SymlinkOption { return func(o *symOpts) { o.force = true } }

// WithSymlinkUser runs the ln command as the given user.
func WithSymlinkUser(u string) SymlinkOption {
	return func(o *symOpts) { o.user = u }
}

// MakeSymlink creates a symbolic link at dest pointing to src inside the
// template during build.
func (b *Builder) MakeSymlink(src, dest string, opts ...SymlinkOption) *Builder {
	o := symOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	cmd := []string{"ln", "-s"}
	if o.force {
		cmd = append(cmd, "-f")
	}
	cmd = append(cmd, src, dest)
	return b.runAs(strings.Join(cmd, " "), o.user)
}

// instructionsWithHashes returns a copy of b.instructions with FilesHash
// populated for every COPY step. When any COPY exists but no build context is
// configured it returns an InvalidArgumentError.
func (b *Builder) instructionsWithHashes() ([]instruction, error) {
	out := make([]instruction, len(b.instructions))
	copy(out, b.instructions)

	hasCopy := false
	for i := range out {
		if out[i].Type == instTypeCopy {
			hasCopy = true
			break
		}
	}
	if hasCopy && b.contextDir == "" {
		return nil, &e2b.InvalidArgumentError{Message: "COPY requires WithContext(dir) to be set on the builder"}
	}

	// Combine explicit ignore patterns with anything found in .dockerignore
	// inside the context, mirroring the Python SDK. Caller-supplied patterns
	// apply whether or not a context dir is set, so they are never dropped.
	ignore := append([]string(nil), b.ignorePatterns...)
	if b.contextDir != "" {
		ignore = append(ignore, readDockerignore(b.contextDir)...)
	}

	for i := range out {
		if out[i].Type != instTypeCopy {
			continue
		}
		src := out[i].Args[0]
		dst := ""
		if len(out[i].Args) > 1 {
			dst = out[i].Args[1]
		}
		resolve := defaultResolveSymlinks
		if out[i].ResolveSymlinks != nil {
			resolve = *out[i].ResolveSymlinks
		}
		h, err := calculateFilesHash(src, dst, b.contextDir, ignore, resolve)
		if err != nil {
			return nil, err
		}
		out[i].FilesHash = h
	}
	return out, nil
}

// serialize converts the builder into the TemplateBuildStartV2 body expected
// by the build API. It runs COPY hashing via instructionsWithHashes and
// skips raw-Dockerfile entries which have no structured representation.
func (b *Builder) serialize(force bool) (*apiclient.TemplateBuildStartV2, error) {
	if b.err != nil {
		return nil, b.err
	}
	steps, err := b.instructionsWithHashes()
	if err != nil {
		return nil, err
	}

	body := &apiclient.TemplateBuildStartV2{}
	if force {
		f := true
		body.Force = &f
	}
	if b.baseImage != "" {
		img := b.baseImage
		body.FromImage = &img
	}
	if b.baseTemplate != "" {
		tpl := b.baseTemplate
		body.FromTemplate = &tpl
	}
	if b.registryConfig != nil {
		body.FromImageRegistry = b.registryConfig
	}
	if b.startCmd != "" {
		s := b.startCmd
		body.StartCmd = &s
	}
	if b.readyCmd != "" {
		r := b.readyCmd
		body.ReadyCmd = &r
	}

	apiSteps := make([]apiclient.TemplateStep, 0, len(steps))
	for _, ins := range steps {
		// Skip raw Dockerfile blobs: they have no TemplateStep mapping.
		if ins.Type == instTypeRaw {
			continue
		}
		step := apiclient.TemplateStep{Type: string(ins.Type)}
		if len(ins.Args) > 0 {
			args := make([]string, len(ins.Args))
			copy(args, ins.Args)
			step.Args = &args
		}
		if ins.Force {
			f := true
			step.Force = &f
		}
		if ins.FilesHash != "" {
			h := ins.FilesHash
			step.FilesHash = &h
		}
		apiSteps = append(apiSteps, step)
	}
	body.Steps = &apiSteps
	return body, nil
}

