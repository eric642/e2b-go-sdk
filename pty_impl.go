package e2b

import (
	"context"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// PtyOptions configures Pty.Create.
type PtyOptions struct {
	Cmd     string
	Args    []string
	Envs    map[string]string
	Cwd     string
	Cols    uint32
	Rows    uint32
	Tag     string
	OnData  func([]byte)
}

// Create starts an interactive PTY session.
func (p *Pty) Create(ctx context.Context, opts PtyOptions) (*CommandHandle, error) {
	if opts.Cmd == "" {
		opts.Cmd = "/bin/bash"
	}
	if opts.Cols == 0 {
		opts.Cols = 80
	}
	if opts.Rows == 0 {
		opts.Rows = 24
	}
	req := connect.NewRequest(&procpb.StartRequest{
		Process: &procpb.ProcessConfig{
			Cmd:  opts.Cmd,
			Args: opts.Args,
			Envs: opts.Envs,
			Cwd:  nilIfEmpty(opts.Cwd),
		},
		Pty: &procpb.PTY{Size: &procpb.PTY_Size{Cols: opts.Cols, Rows: opts.Rows}},
		Tag: nilIfEmpty(opts.Tag),
	})
	stream, err := p.sbx.envd.Process.Start(ctx, req)
	if err != nil {
		return nil, mapConnectErr(err)
	}
	handle := newCommandHandle(p.sbx, nil, nil)
	handle.streaming = true
	handle.startStream(&startStream{s: stream})
	if opts.OnData != nil {
		go func() {
			for data := range handle.PtyOutput() {
				opts.OnData(data)
			}
		}()
	}
	return handle, nil
}

// Resize changes the PTY terminal size.
func (p *Pty) Resize(ctx context.Context, pid uint32, cols, rows uint32) error {
	req := connect.NewRequest(&procpb.UpdateRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Pty:     &procpb.PTY{Size: &procpb.PTY_Size{Cols: cols, Rows: rows}},
	})
	_, err := p.sbx.envd.Process.Update(ctx, req)
	return mapConnectErr(err)
}

// SendInput writes bytes into the PTY.
func (p *Pty) SendInput(ctx context.Context, pid uint32, data []byte) error {
	req := connect.NewRequest(&procpb.SendInputRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Input:   &procpb.ProcessInput{Input: &procpb.ProcessInput_Pty{Pty: data}},
	})
	_, err := p.sbx.envd.Process.SendInput(ctx, req)
	return mapConnectErr(err)
}

// Kill terminates the PTY.
func (p *Pty) Kill(ctx context.Context, pid uint32) (bool, error) {
	req := connect.NewRequest(&procpb.SendSignalRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Signal:  procpb.Signal_SIGNAL_SIGKILL,
	})
	if _, err := p.sbx.envd.Process.SendSignal(ctx, req); err != nil {
		return false, mapConnectErr(err)
	}
	return true, nil
}
