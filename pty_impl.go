package e2b

import (
	"context"
	"maps"
	"strconv"
	"time"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// PtyOptions configures Pty.Create.
type PtyOptions struct {
	Cmd            string
	Args           []string
	User           string
	Envs           map[string]string
	Cwd            string
	Cols           uint32
	Rows           uint32
	TimeoutMs      int
	RequestTimeout time.Duration
	Tag            string
	OnData         func([]byte)
}

// PtyConnectOptions configures Pty.Connect.
type PtyConnectOptions struct {
	User           string
	TimeoutMs      int
	RequestTimeout time.Duration
	OnData         func([]byte)
}

var defaultPtyEnvs = map[string]string{
	"TERM":   "xterm-256color",
	"LANG":   "C.UTF-8",
	"LC_ALL": "C.UTF-8",
}

// Create starts an interactive PTY session inside the sandbox. When Cmd is
// empty it defaults to "/bin/bash"; when Cmd is "/bin/bash" and Args is nil,
// Args defaults to []string{"-i", "-l"}. Default environment variables
// (TERM=xterm-256color, LANG=C.UTF-8, LC_ALL=C.UTF-8) are injected unless
// overridden in opts.Envs.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) Create(ctx context.Context, opts PtyOptions) (*CommandHandle, error) {
	cmd := opts.Cmd
	if cmd == "" {
		cmd = "/bin/bash"
	}
	args := opts.Args
	if cmd == "/bin/bash" && args == nil {
		args = []string{"-i", "-l"}
	} else if args != nil {
		args = append([]string(nil), args...)
	}
	cols := opts.Cols
	if cols == 0 {
		cols = 80
	}
	rows := opts.Rows
	if rows == 0 {
		rows = 24
	}

	envs := make(map[string]string, len(defaultPtyEnvs)+len(opts.Envs))
	maps.Copy(envs, defaultPtyEnvs)
	maps.Copy(envs, opts.Envs)

	req := connect.NewRequest(&procpb.StartRequest{
		Process: &procpb.ProcessConfig{
			Cmd:  cmd,
			Args: args,
			Envs: envs,
			Cwd:  nilIfEmpty(opts.Cwd),
		},
		Pty: &procpb.PTY{Size: &procpb.PTY_Size{Cols: cols, Rows: rows}},
		Tag: nilIfEmpty(opts.Tag),
	})
	if opts.TimeoutMs > 0 {
		req.Header().Set("Connect-Timeout-Ms", strconv.Itoa(opts.TimeoutMs))
	}

	streamCtx, clearStartTimeout, cancelAll := startStreamContext(ctx, p.sbx, opts.User, opts.RequestTimeout)

	stream, err := p.sbx.envdClients().Process.Start(streamCtx, req)
	if err != nil {
		mapped := mapStreamErr(streamCtx, err)
		cancelAll()
		return nil, mapped
	}
	handle := newCommandHandle(p.sbx, nil, nil, opts.OnData, false)
	handle.user = opts.User
	handle.clearStartTimeout = clearStartTimeout
	handle.cancel = cancelAll
	handle.startStream(streamCtx, &startStream{s: stream})
	return handle, nil
}

// Connect attaches to an existing PTY session by PID and streams its output.
//
// Endpoint: process.Process/Connect (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) Connect(ctx context.Context, pid uint32, opts PtyConnectOptions) (*CommandHandle, error) {
	return p.connectSelector(ctx, &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}}, pid, opts)
}

// ConnectByTag attaches to an existing PTY session by its custom tag and
// streams its output.
//
// Endpoint: process.Process/Connect (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) ConnectByTag(ctx context.Context, tag string, opts PtyConnectOptions) (*CommandHandle, error) {
	if tag == "" {
		return nil, &InvalidArgumentError{Message: "process tag is required"}
	}
	return p.connectSelector(ctx, &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Tag{Tag: tag}}, 0, opts)
}

func (p *Pty) connectSelector(ctx context.Context, selector *procpb.ProcessSelector, initialPID uint32, opts PtyConnectOptions) (*CommandHandle, error) {
	return connectProcessSelector(ctx, p.sbx, selector, initialPID, opts.User, opts.TimeoutMs, opts.RequestTimeout, nil, nil, opts.OnData, false)
}

// Resize changes the PTY terminal dimensions (columns and rows).
//
// Endpoint: process.Process/Update (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) Resize(ctx context.Context, pid uint32, cols, rows uint32, opts ...CommandRequestOptions) error {
	o := firstCommandReqOpts(opts)
	callCtx, cancel := withEnvdCallOpts(ctx, p.sbx, o.User, unaryEnvdTimeout(p.sbx, o.RequestTimeout))
	defer cancel()

	req := connect.NewRequest(&procpb.UpdateRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Pty:     &procpb.PTY{Size: &procpb.PTY_Size{Cols: cols, Rows: rows}},
	})
	_, err := p.sbx.envdClients().Process.Update(callCtx, req)
	return mapConnectErr(err)
}

// SendInput writes raw terminal bytes into the PTY identified by PID.
//
// Endpoint: process.Process/SendInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) SendInput(ctx context.Context, pid uint32, data []byte, opts ...CommandRequestOptions) error {
	o := firstCommandReqOpts(opts)
	callCtx, cancel := withEnvdCallOpts(ctx, p.sbx, o.User, unaryEnvdTimeout(p.sbx, o.RequestTimeout))
	defer cancel()

	req := connect.NewRequest(&procpb.SendInputRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Input:   &procpb.ProcessInput{Input: &procpb.ProcessInput_Pty{Pty: data}},
	})
	_, err := p.sbx.envdClients().Process.SendInput(callCtx, req)
	return mapConnectErr(err)
}

// StreamInput opens a persistent, ordered client-streaming PTY input connection
// to the PTY identified by PID. Call SendPty to write ordered terminal bytes
// and Close to finish the stream and await envd's acknowledgement.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) StreamInput(ctx context.Context, pid uint32, opts ...CommandRequestOptions) (*PtyInputStream, error) {
	is, err := newInputStream(ctx, p.sbx, pid, firstCommandReqOpts(opts))
	if err != nil {
		return nil, err
	}
	return &PtyInputStream{s: is}, nil
}

// Kill sends SIGKILL to the PTY session identified by PID. It returns
// (false, nil) if the PTY process is no longer running (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (p *Pty) Kill(ctx context.Context, pid uint32, opts ...CommandRequestOptions) (bool, error) {
	return killProcess(ctx, p.sbx, pid, firstCommandReqOpts(opts))
}

// PtyInputStream is a persistent client-streaming PTY input handle opened by
// Pty.StreamInput. All methods are safe for concurrent use.
type PtyInputStream struct {
	s *inputStream
}

// SendPty sends a chunk of terminal bytes to the PTY in call order.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (ps *PtyInputStream) SendPty(data []byte) error {
	return ps.s.send(&procpb.ProcessInput{
		Input: &procpb.ProcessInput_Pty{Pty: data},
	})
}

// SendData is an alias for SendPty.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (ps *PtyInputStream) SendData(data []byte) error {
	return ps.SendPty(data)
}

// Send is an alias for SendPty.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (ps *PtyInputStream) Send(data []byte) error {
	return ps.SendPty(data)
}

// Close ends the client PTY input stream and waits for envd to acknowledge it.
// Calling Close multiple times is safe and returns the same error.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (ps *PtyInputStream) Close() error {
	return ps.s.close()
}
