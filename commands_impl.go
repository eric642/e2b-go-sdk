package e2b

import (
	"context"
	"io"
	"time"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// RunOptions configures Commands.Run and Commands.Start.
type RunOptions struct {
	Args       []string
	Cwd        string
	User       string
	Envs       map[string]string
	TimeoutMs  int
	Stdin      io.Reader
	Background bool
	Tag        string
	OnStdout   func([]byte)
	OnStderr   func([]byte)
}

// Run executes cmd and blocks until it exits (unless opts.Background is
// true, in which case Run returns a handle immediately). Callers may
// observe output through the returned handle's channels or the
// OnStdout/OnStderr callbacks.
func (c *Commands) Run(ctx context.Context, cmd string, opts RunOptions) (*CommandHandle, error) {
	h, err := c.Start(ctx, cmd, opts)
	if err != nil {
		return nil, err
	}
	if opts.Background {
		return h, nil
	}
	waitCtx := ctx
	if opts.TimeoutMs > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	if _, err := h.Wait(waitCtx); err != nil {
		return h, err
	}
	return h, nil
}

// Start launches cmd and returns a live *CommandHandle.
func (c *Commands) Start(ctx context.Context, cmd string, opts RunOptions) (*CommandHandle, error) {
	req := connect.NewRequest(&procpb.StartRequest{
		Process: &procpb.ProcessConfig{
			Cmd:  cmd,
			Args: opts.Args,
			Envs: opts.Envs,
			Cwd:  nilIfEmpty(opts.Cwd),
		},
		Tag:   nilIfEmpty(opts.Tag),
		Stdin: boolPtr(opts.Stdin != nil),
	})
	stream, err := c.sbx.envd.Process.Start(ctx, req)
	if err != nil {
		return nil, mapConnectErr(err)
	}
	handle := newCommandHandle(c.sbx, opts.OnStdout, opts.OnStderr)
	handle.streaming = true
	handle.startStream(&startStream{s: stream})
	if opts.Stdin != nil {
		go pumpStdin(ctx, handle, opts.Stdin)
	}
	return handle, nil
}

// Connect attaches to a running process by PID and streams remaining output.
func (c *Commands) Connect(ctx context.Context, pid uint32) (*CommandHandle, error) {
	req := connect.NewRequest(&procpb.ConnectRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
	})
	stream, err := c.sbx.envd.Process.Connect(ctx, req)
	if err != nil {
		return nil, mapConnectErr(err)
	}
	handle := newCommandHandle(c.sbx, nil, nil)
	handle.pid = pid
	handle.streaming = true
	handle.startStream(&connectStream{s: stream})
	return handle, nil
}

// List returns all processes currently running inside the sandbox.
func (c *Commands) List(ctx context.Context) ([]ProcessInfo, error) {
	resp, err := c.sbx.envd.Process.List(ctx, connect.NewRequest(&procpb.ListRequest{}))
	if err != nil {
		return nil, mapConnectErr(err)
	}
	out := make([]ProcessInfo, 0, len(resp.Msg.GetProcesses()))
	for _, p := range resp.Msg.GetProcesses() {
		pi := ProcessInfo{PID: p.GetPid()}
		if cfg := p.GetConfig(); cfg != nil {
			pi.Cmd = cfg.GetCmd()
			pi.Args = cfg.GetArgs()
			pi.Envs = cfg.GetEnvs()
			pi.Cwd = cfg.GetCwd()
		}
		if p.Tag != nil {
			pi.Tag = *p.Tag
		}
		out = append(out, pi)
	}
	return out, nil
}

// Kill signals the command identified by PID.
func (c *Commands) Kill(ctx context.Context, pid uint32) (bool, error) {
	req := connect.NewRequest(&procpb.SendSignalRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Signal:  procpb.Signal_SIGNAL_SIGKILL,
	})
	if _, err := c.sbx.envd.Process.SendSignal(ctx, req); err != nil {
		return false, mapConnectErr(err)
	}
	return true, nil
}

// SendStdin writes to the stdin of the running process.
func (c *Commands) SendStdin(ctx context.Context, pid uint32, data []byte) error {
	req := connect.NewRequest(&procpb.SendInputRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Input:   &procpb.ProcessInput{Input: &procpb.ProcessInput_Stdin{Stdin: data}},
	})
	_, err := c.sbx.envd.Process.SendInput(ctx, req)
	return mapConnectErr(err)
}

// CloseStdin closes the stdin side of the process.
func (c *Commands) CloseStdin(ctx context.Context, pid uint32) error {
	req := connect.NewRequest(&procpb.CloseStdinRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
	})
	_, err := c.sbx.envd.Process.CloseStdin(ctx, req)
	return mapConnectErr(err)
}

// pumpStdin streams data from r into the process via SendInput calls.
func pumpStdin(ctx context.Context, h *CommandHandle, r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_ = h.SendStdin(ctx, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolPtr(b bool) *bool { return &b }
