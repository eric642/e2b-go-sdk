package e2b

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// CommandHandle represents a running command or PTY session. Events arrive on
// the buffered channels returned by Stdout(), Stderr(), and PtyOutput() (with
// non-blocking drops on slow consumers; use OnStdout/OnStderr/OnData or Wait
// for guaranteed delivery); the channels close when the command exits, the
// stream fails, or Disconnect is called. Call Wait to block and collect the
// final exit code.
type CommandHandle struct {
	sbx  *Sandbox
	user string

	stdoutCh chan []byte
	stderrCh chan []byte
	ptyCh    chan []byte

	onStdout func([]byte)
	onStderr func([]byte)
	onPty    func([]byte)

	retainOutput      bool
	clearStartTimeout func()
	cancel            context.CancelFunc
	cancelOnce        sync.Once
	started           chan struct{}
	startedOnce       sync.Once

	mu           sync.Mutex
	pid          uint32
	stdout       strings.Builder
	stderr       strings.Builder
	result       *CommandResult
	doneErr      error
	done         chan struct{}
	disconnected bool
}

func newCommandHandle(sbx *Sandbox, onStdout, onStderr, onPty func([]byte), retainOutput bool) *CommandHandle {
	return &CommandHandle{
		sbx:          sbx,
		stdoutCh:     make(chan []byte, 32),
		stderrCh:     make(chan []byte, 32),
		ptyCh:        make(chan []byte, 32),
		onStdout:     onStdout,
		onStderr:     onStderr,
		onPty:        onPty,
		retainOutput: retainOutput,
		started:      make(chan struct{}),
		done:         make(chan struct{}),
	}
}

func (h *CommandHandle) markStarted() {
	h.startedOnce.Do(func() {
		close(h.started)
	})
}

func (h *CommandHandle) invokeCancel() {
	h.cancelOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
}

func (h *CommandHandle) isDisconnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.disconnected
}

// PID returns the command's process ID once known (0 until the StartEvent
// arrives). Use WaitPID to block until the StartEvent has populated the PID.
func (h *CommandHandle) PID() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pid
}

// WaitPID blocks until the server emits the initial StartEvent (or the stream
// or ctx terminates) and returns the process ID.
func (h *CommandHandle) WaitPID(ctx context.Context) (uint32, error) {
	return h.waitPID(ctx)
}

func (h *CommandHandle) waitPID(ctx context.Context) (uint32, error) {
	if pid := h.PID(); pid != 0 {
		return pid, nil
	}
	if h.started != nil {
		select {
		case <-h.started:
		case <-h.done:
		case <-ctx.Done():
			return 0, contextErr(ctx.Err())
		}
	}
	if pid := h.PID(); pid != 0 {
		return pid, nil
	}
	h.mu.Lock()
	doneErr := h.doneErr
	disconnected := h.disconnected
	h.mu.Unlock()
	if doneErr != nil {
		return 0, doneErr
	}
	if disconnected {
		return 0, newSandboxError("command stream disconnected before start event", nil)
	}
	return 0, newSandboxError("command process ID is not available", nil)
}

// ExitCode returns the command's exit code and true once the process has
// exited, or (0, false) while the process is still running.
func (h *CommandHandle) ExitCode() (int32, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.result == nil {
		return 0, false
	}
	return h.result.ExitCode, true
}

func (h *CommandHandle) resolveReqOpts(opts []CommandRequestOptions) CommandRequestOptions {
	o := firstCommandReqOpts(opts)
	if o.User == "" {
		o.User = h.user
	}
	return o
}

// Stdout returns a buffered channel (capacity 32) of stdout byte slices.
// Sends are non-blocking: if the channel buffer is full because the consumer
// is slow or not reading, incoming chunks are dropped without blocking the
// event stream. Callers that need guaranteed delivery of every chunk should
// use RunOptions.OnStdout / CommandConnectOptions.OnStdout or inspect
// CommandResult.Stdout returned by Wait. The channel is closed when the
// command exits or the client stream ends.
func (h *CommandHandle) Stdout() <-chan []byte { return h.stdoutCh }

// Stderr returns a buffered channel (capacity 32) of stderr byte slices.
// Sends are non-blocking: if the channel buffer is full because the consumer
// is slow or not reading, incoming chunks are dropped without blocking the
// event stream. Callers that need guaranteed delivery of every chunk should
// use RunOptions.OnStderr / CommandConnectOptions.OnStderr or inspect
// CommandResult.Stderr returned by Wait. The channel is closed when the
// command exits or the client stream ends.
func (h *CommandHandle) Stderr() <-chan []byte { return h.stderrCh }

// PtyOutput returns a buffered channel (capacity 32) of PTY output byte slices
// (empty for non-PTY commands). Sends are non-blocking: if the channel buffer
// is full because the consumer is slow or not reading, incoming chunks are
// dropped without blocking the event stream. Callers that need guaranteed
// delivery of every chunk should use PtyOptions.OnData or
// PtyConnectOptions.OnData. The channel is closed when the PTY session exits
// or the client stream ends.
func (h *CommandHandle) PtyOutput() <-chan []byte { return h.ptyCh }

// Wait blocks until the command stream finishes, returns a *CommandResult, and
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
		if h.disconnected {
			return nil, newSandboxError("command stream disconnected before exit", nil)
		}
		return nil, newSandboxError("command ended with no result", nil)
	}
	res := *h.result
	if res.ExitCode != 0 {
		return &res, &CommandExitError{Result: res}
	}
	return &res, nil
}

// Disconnect cancels the client event stream locally without killing the remote
// process inside the sandbox. You can reconnect later via Commands.Connect or
// Pty.Connect. Calling Disconnect multiple times is safe and idempotent.
//
// Stopping OnStdout/OnStderr/OnData callbacks is best-effort: Disconnect does
// not hold locks around callback execution or wait for an in-flight callback to
// return (so callbacks may safely call Disconnect re-entrantly without
// deadlocking), and a callback already being dispatched concurrently may still
// complete after Disconnect returns.
func (h *CommandHandle) Disconnect() {
	h.mu.Lock()
	h.disconnected = true
	h.mu.Unlock()
	h.invokeCancel()
}

// Kill sends SIGKILL to the running command or PTY session. It returns
// (false, nil) if the process has already exited (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) Kill(ctx context.Context, opts ...CommandRequestOptions) (bool, error) {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return false, err
	}
	return killProcess(ctx, h.sbx, pid, h.resolveReqOpts(opts))
}

// SendSignal sends sig (SignalSIGTERM or SignalSIGKILL) to the running command
// or PTY session. It returns (false, nil) if the process has already exited
// (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) SendSignal(ctx context.Context, sig Signal, opts ...CommandRequestOptions) (bool, error) {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return false, err
	}
	return (&Commands{sbx: h.sbx}).SendSignal(ctx, pid, sig, h.resolveReqOpts(opts))
}

// SendStdin writes data to the command's stdin.
//
// Endpoint: process.Process/SendInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) SendStdin(ctx context.Context, data []byte, opts ...CommandRequestOptions) error {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return err
	}
	return (&Commands{sbx: h.sbx}).SendStdin(ctx, pid, data, h.resolveReqOpts(opts))
}

// StreamInput opens a persistent, ordered client-streaming stdin connection to
// this command.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) StreamInput(ctx context.Context, opts ...CommandRequestOptions) (*CommandInputStream, error) {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return nil, err
	}
	return (&Commands{sbx: h.sbx}).StreamInput(ctx, pid, h.resolveReqOpts(opts))
}

// CloseStdin closes the command's stdin descriptor to signal EOF. Requires
// envd >= v0.5.2; returns *TemplateError on older envd builds.
//
// Endpoint: process.Process/CloseStdin (envd Connect-RPC)
// Envd: >= v0.5.2
func (h *CommandHandle) CloseStdin(ctx context.Context, opts ...CommandRequestOptions) error {
	if err := h.sbx.requireEnvdVersion(envdEnvdClose, "closing stdin"); err != nil {
		return err
	}
	pid, err := h.waitPID(ctx)
	if err != nil {
		return err
	}
	return (&Commands{sbx: h.sbx}).closeStdinChecked(ctx, pid, h.resolveReqOpts(opts))
}

// SendPty writes raw terminal bytes into this PTY session.
//
// Endpoint: process.Process/SendInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) SendPty(ctx context.Context, data []byte, opts ...CommandRequestOptions) error {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return err
	}
	return (&Pty{sbx: h.sbx}).SendInput(ctx, pid, data, h.resolveReqOpts(opts))
}

// StreamPtyInput opens a persistent, ordered client-streaming PTY input
// connection to this PTY session.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) StreamPtyInput(ctx context.Context, opts ...CommandRequestOptions) (*PtyInputStream, error) {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return nil, err
	}
	return (&Pty{sbx: h.sbx}).StreamInput(ctx, pid, h.resolveReqOpts(opts))
}

// ResizePty changes this PTY session's terminal dimensions (columns and rows).
//
// Endpoint: process.Process/Update (envd Connect-RPC)
// Envd: >= v0.1.0
func (h *CommandHandle) ResizePty(ctx context.Context, cols, rows uint32, opts ...CommandRequestOptions) error {
	pid, err := h.waitPID(ctx)
	if err != nil {
		return err
	}
	return (&Pty{sbx: h.sbx}).Resize(ctx, pid, cols, rows, h.resolveReqOpts(opts))
}

func (h *CommandHandle) startStream(streamCtx context.Context, stream eventStream) {
	go func() {
		defer close(h.done)
		defer h.markStarted()
		defer h.invokeCancel()
		defer func() { _ = stream.Close() }()
		defer close(h.stdoutCh)
		defer close(h.stderrCh)
		defer close(h.ptyCh)

		for stream.Receive() {
			if h.isDisconnected() {
				break
			}
			ev := stream.Event()
			if ev == nil {
				continue
			}
			switch e := ev.Event.(type) {
			case *procpb.ProcessEvent_Start:
				if h.clearStartTimeout != nil {
					h.clearStartTimeout()
				}
				h.mu.Lock()
				h.pid = e.Start.GetPid()
				h.mu.Unlock()
				h.markStarted()
			case *procpb.ProcessEvent_Data:
				h.handleDataEvent(e.Data)
			case *procpb.ProcessEvent_End:
				if h.clearStartTimeout != nil {
					h.clearStartTimeout()
				}
				h.mu.Lock()
				h.result = &CommandResult{
					ExitCode: e.End.GetExitCode(),
					Error:    e.End.GetError(),
				}
				h.mu.Unlock()
			case *procpb.ProcessEvent_Keepalive:
			}
		}

		streamErr := stream.Err()
		h.mu.Lock()
		if !h.disconnected {
			if streamErr != nil && !errors.Is(streamErr, io.EOF) {
				h.doneErr = mapStreamErr(streamCtx, streamErr)
			} else if h.result == nil && streamCtx != nil {
				if te, ok := errors.AsType[*TimeoutError](context.Cause(streamCtx)); ok {
					h.doneErr = te
				}
			}
		}
		if h.retainOutput && h.result != nil {
			h.result.Stdout = h.stdout.String()
			h.result.Stderr = h.stderr.String()
		}
		h.stdout.Reset()
		h.stderr.Reset()
		h.mu.Unlock()
	}()
}

func sendChunkNonBlocking(ch chan []byte, src []byte) {
	if len(ch) == cap(ch) {
		return
	}
	select {
	case ch <- bytes.Clone(src):
	default:
	}
}

func (h *CommandHandle) handleDataEvent(d *procpb.ProcessEvent_DataEvent) {
	if d == nil || h.isDisconnected() {
		return
	}
	switch data := d.Output.(type) {
	case *procpb.ProcessEvent_DataEvent_Stdout:
		if h.retainOutput && len(data.Stdout) > 0 {
			h.mu.Lock()
			_, _ = h.stdout.Write(data.Stdout)
			h.mu.Unlock()
		}
		if h.onStdout != nil && !h.isDisconnected() {
			h.onStdout(bytes.Clone(data.Stdout))
		}
		if !h.isDisconnected() {
			sendChunkNonBlocking(h.stdoutCh, data.Stdout)
		}
	case *procpb.ProcessEvent_DataEvent_Stderr:
		if h.retainOutput && len(data.Stderr) > 0 {
			h.mu.Lock()
			_, _ = h.stderr.Write(data.Stderr)
			h.mu.Unlock()
		}
		if h.onStderr != nil && !h.isDisconnected() {
			h.onStderr(bytes.Clone(data.Stderr))
		}
		if !h.isDisconnected() {
			sendChunkNonBlocking(h.stderrCh, data.Stderr)
		}
	case *procpb.ProcessEvent_DataEvent_Pty:
		if h.onPty != nil && !h.isDisconnected() {
			h.onPty(bytes.Clone(data.Pty))
		}
		if !h.isDisconnected() {
			sendChunkNonBlocking(h.ptyCh, data.Pty)
		}
	}
}

type eventStream interface {
	Receive() bool
	Event() *procpb.ProcessEvent
	Err() error
	Close() error
}

type startStream struct {
	s *connect.ServerStreamForClient[procpb.StartResponse]
}

func (s *startStream) Receive() bool { return s.s.Receive() }
func (s *startStream) Err() error    { return s.s.Err() }
func (s *startStream) Close() error  { return s.s.Close() }
func (s *startStream) Event() *procpb.ProcessEvent {
	if s.s.Msg() == nil {
		return nil
	}
	return s.s.Msg().Event
}

type connectStream struct {
	s *connect.ServerStreamForClient[procpb.ConnectResponse]
}

func (s *connectStream) Receive() bool { return s.s.Receive() }
func (s *connectStream) Err() error    { return s.s.Err() }
func (s *connectStream) Close() error  { return s.s.Close() }
func (s *connectStream) Event() *procpb.ProcessEvent {
	if s.s.Msg() == nil {
		return nil
	}
	return s.s.Msg().Event
}
