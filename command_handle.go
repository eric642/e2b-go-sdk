package e2b

import (
	"context"
	"errors"
	"io"
	"sync"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// CommandHandle represents a running command. Events arrive on the
// channels returned by Stdout() and Stderr(); the channels close when the
// command exits or the stream fails. Call Wait to block and collect the
// final exit code.
type CommandHandle struct {
	sbx *Sandbox

	pid uint32
	tag string

	stdoutCh chan []byte
	stderrCh chan []byte
	ptyCh    chan []byte

	onStdout func([]byte)
	onStderr func([]byte)

	// protected by mu
	mu       sync.Mutex
	stdout   []byte
	stderr   []byte
	result   *CommandResult
	doneErr  error
	done     chan struct{}
	streamCancel context.CancelFunc
	streaming bool
}

// newCommandHandle wires up channels and internal state.
func newCommandHandle(sbx *Sandbox, onStdout, onStderr func([]byte)) *CommandHandle {
	return &CommandHandle{
		sbx:      sbx,
		stdoutCh: make(chan []byte, 32),
		stderrCh: make(chan []byte, 32),
		ptyCh:    make(chan []byte, 32),
		onStdout: onStdout,
		onStderr: onStderr,
		done:     make(chan struct{}),
	}
}

// PID returns the command's process ID once known.
func (h *CommandHandle) PID() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pid
}

// Stdout returns a channel of stdout byte slices. The channel is closed when
// the command exits.
func (h *CommandHandle) Stdout() <-chan []byte { return h.stdoutCh }

// Stderr returns a channel of stderr byte slices.
func (h *CommandHandle) Stderr() <-chan []byte { return h.stderrCh }

// PtyOutput returns a channel of PTY data. Empty for non-PTY commands.
func (h *CommandHandle) PtyOutput() <-chan []byte { return h.ptyCh }

// Wait blocks until the command finishes, returns a *CommandResult, and
// wraps non-zero exit codes in a *CommandExitError.
func (h *CommandHandle) Wait(ctx context.Context) (*CommandResult, error) {
	select {
	case <-h.done:
	case <-ctx.Done():
		return nil, contextErr(ctx.Err())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.doneErr != nil {
		return nil, h.doneErr
	}
	if h.result == nil {
		return nil, newSandboxError("command ended with no result", nil)
	}
	res := *h.result
	res.Stdout = string(h.stdout)
	res.Stderr = string(h.stderr)
	if res.ExitCode != 0 {
		return &res, &CommandExitError{Result: res}
	}
	return &res, nil
}

// Kill signals the running command.
func (h *CommandHandle) Kill(ctx context.Context) (bool, error) {
	req := connect.NewRequest(&procpb.SendSignalRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: h.pid}},
		Signal:  procpb.Signal_SIGNAL_SIGKILL,
	})
	if _, err := h.sbx.envd.Process.SendSignal(ctx, req); err != nil {
		return false, mapConnectErr(err)
	}
	return true, nil
}

// SendStdin writes data to the command's stdin.
func (h *CommandHandle) SendStdin(ctx context.Context, data []byte) error {
	req := connect.NewRequest(&procpb.SendInputRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: h.pid}},
		Input:   &procpb.ProcessInput{Input: &procpb.ProcessInput_Stdin{Stdin: data}},
	})
	_, err := h.sbx.envd.Process.SendInput(ctx, req)
	return mapConnectErr(err)
}

// startStream consumes the provided server stream in a goroutine and fans
// events out to the channels. It stores the final CommandResult for Wait().
// Caller must have ensured h.streamCancel captures the ctx cancel func.
func (h *CommandHandle) startStream(stream eventStream) {
	go func() {
		defer close(h.stdoutCh)
		defer close(h.stderrCh)
		defer close(h.ptyCh)
		defer close(h.done)

		for stream.Receive() {
			ev := stream.Event()
			if ev == nil {
				continue
			}
			switch e := ev.Event.(type) {
			case *procpb.ProcessEvent_Start:
				h.mu.Lock()
				h.pid = e.Start.GetPid()
				h.mu.Unlock()
			case *procpb.ProcessEvent_Data:
				switch data := e.Data.Output.(type) {
				case *procpb.ProcessEvent_DataEvent_Stdout:
					h.mu.Lock()
					h.stdout = append(h.stdout, data.Stdout...)
					h.mu.Unlock()
					if h.onStdout != nil {
						h.onStdout(data.Stdout)
					}
					select {
					case h.stdoutCh <- append([]byte(nil), data.Stdout...):
					default:
					}
				case *procpb.ProcessEvent_DataEvent_Stderr:
					h.mu.Lock()
					h.stderr = append(h.stderr, data.Stderr...)
					h.mu.Unlock()
					if h.onStderr != nil {
						h.onStderr(data.Stderr)
					}
					select {
					case h.stderrCh <- append([]byte(nil), data.Stderr...):
					default:
					}
				case *procpb.ProcessEvent_DataEvent_Pty:
					select {
					case h.ptyCh <- append([]byte(nil), data.Pty...):
					default:
					}
				}
			case *procpb.ProcessEvent_End:
				h.mu.Lock()
				h.result = &CommandResult{
					ExitCode: e.End.GetExitCode(),
					Error:    e.End.GetError(),
				}
				h.mu.Unlock()
			case *procpb.ProcessEvent_Keepalive:
				// ignore
			}
		}
		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			h.mu.Lock()
			h.doneErr = mapConnectErr(err)
			h.mu.Unlock()
		}
	}()
}

// eventStream is an abstraction over Connect server streams that carry
// ProcessEvent envelopes (Start() and Connect() RPCs).
type eventStream interface {
	Receive() bool
	Event() *procpb.ProcessEvent
	Err() error
}

type startStream struct{ s *connect.ServerStreamForClient[procpb.StartResponse] }

func (s *startStream) Receive() bool { return s.s.Receive() }
func (s *startStream) Err() error    { return s.s.Err() }
func (s *startStream) Event() *procpb.ProcessEvent {
	if s.s.Msg() == nil {
		return nil
	}
	return s.s.Msg().Event
}

type connectStream struct{ s *connect.ServerStreamForClient[procpb.ConnectResponse] }

func (s *connectStream) Receive() bool { return s.s.Receive() }
func (s *connectStream) Err() error    { return s.s.Err() }
func (s *connectStream) Event() *procpb.ProcessEvent {
	if s.s.Msg() == nil {
		return nil
	}
	return s.s.Msg().Event
}
