package e2b

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
)

// Signal enumerates process signals accepted by Commands.SendSignal and
// CommandHandle.SendSignal.
type Signal int32

const (
	SignalSIGTERM Signal = Signal(procpb.Signal_SIGNAL_SIGTERM)
	SignalSIGKILL Signal = Signal(procpb.Signal_SIGNAL_SIGKILL)
)

// RunOptions configures Commands.Run and Commands.Start.
type RunOptions struct {
	Args           []string
	Shell          string
	Cwd            string
	User           string
	Envs           map[string]string
	TimeoutMs      int
	RequestTimeout time.Duration
	Stdin          io.Reader
	// EnableStdin keeps the child's stdin pipe open even when Stdin is nil so
	// callers can write to it later via SendStdin, StreamInput, or CloseStdin.
	EnableStdin bool
	Background  bool
	// RetainOutput controls whether stdout and stderr are accumulated in memory
	// for CommandHandle.Wait(). Nil defaults to true; set to false for high-volume
	// commands consumed via OnStdout/OnStderr or Stdout()/Stderr().
	RetainOutput *bool
	Tag          string
	OnStdout     func([]byte)
	OnStderr     func([]byte)
}

// CommandConnectOptions configures Commands.Connect.
type CommandConnectOptions struct {
	User           string
	TimeoutMs      int
	RequestTimeout time.Duration
	RetainOutput   *bool
	OnStdout       func([]byte)
	OnStderr       func([]byte)
}

// CommandRequestOptions configures per-call user and request timeout for unary
// and client-streaming command and PTY operations.
type CommandRequestOptions struct {
	User           string
	RequestTimeout time.Duration
}

func firstCommandReqOpts(opts []CommandRequestOptions) CommandRequestOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return CommandRequestOptions{}
}

// Run executes cmd and blocks until it exits (unless opts.Background is true,
// in which case Run returns a live handle immediately). Callers may observe
// output through the returned handle's channels or the OnStdout/OnStderr
// callbacks. When opts.Shell is non-empty, cmd is executed via `<Shell> -l -c <cmd>`;
// otherwise cmd is invoked directly with opts.Args.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
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
		if _, isExit := errors.AsType[*CommandExitError](err); !isExit {
			h.Disconnect()
		}
		return h, err
	}
	return h, nil
}

// Start launches cmd and returns a live *CommandHandle streaming process events.
// When opts.Shell is non-empty, cmd is executed via `<Shell> -l -c <cmd>`;
// otherwise cmd is executed directly with opts.Args.
//
// Endpoint: process.Process/Start (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) Start(ctx context.Context, cmd string, opts RunOptions) (*CommandHandle, error) {
	procCmd := cmd
	procArgs := opts.Args
	if opts.Shell != "" {
		procCmd = opts.Shell
		procArgs = []string{"-l", "-c", cmd}
	} else if procArgs != nil {
		procArgs = append([]string(nil), procArgs...)
	}

	req := connect.NewRequest(&procpb.StartRequest{
		Process: &procpb.ProcessConfig{
			Cmd:  procCmd,
			Args: procArgs,
			Envs: maps.Clone(opts.Envs),
			Cwd:  nilIfEmpty(opts.Cwd),
		},
		Tag:   nilIfEmpty(opts.Tag),
		Stdin: new(opts.Stdin != nil || opts.EnableStdin),
	})
	if opts.TimeoutMs > 0 {
		req.Header().Set("Connect-Timeout-Ms", strconv.Itoa(opts.TimeoutMs))
	}

	callCtx, optCancel := withEnvdCallOpts(ctx, c.sbx, opts.User, opts.RequestTimeout)
	streamCtx, streamCancel := context.WithCancel(callCtx)
	cancelAll := func() {
		streamCancel()
		optCancel()
	}

	stream, err := c.sbx.envdClients().Process.Start(streamCtx, req)
	if err != nil {
		cancelAll()
		return nil, mapConnectErr(err)
	}

	retain := opts.RetainOutput == nil || *opts.RetainOutput
	handle := newCommandHandle(c.sbx, opts.OnStdout, opts.OnStderr, nil, retain)
	handle.user = opts.User
	handle.cancel = cancelAll
	handle.startStream(&startStream{s: stream})
	if opts.Stdin != nil {
		go pumpStdin(ctx, streamCtx, handle, opts.Stdin)
	}
	return handle, nil
}

// Connect attaches to a running process by PID and streams its remaining output.
//
// Endpoint: process.Process/Connect (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) Connect(ctx context.Context, pid uint32, opts ...CommandConnectOptions) (*CommandHandle, error) {
	return c.connectSelector(ctx, &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}}, pid, opts...)
}

// ConnectByTag attaches to a running process by its custom tag and streams its
// remaining output.
//
// Endpoint: process.Process/Connect (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) ConnectByTag(ctx context.Context, tag string, opts ...CommandConnectOptions) (*CommandHandle, error) {
	if tag == "" {
		return nil, &InvalidArgumentError{Message: "process tag is required"}
	}
	return c.connectSelector(ctx, &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Tag{Tag: tag}}, 0, opts...)
}

func (c *Commands) connectSelector(ctx context.Context, selector *procpb.ProcessSelector, initialPID uint32, opts ...CommandConnectOptions) (*CommandHandle, error) {
	var o CommandConnectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	retain := o.RetainOutput == nil || *o.RetainOutput
	return connectProcessSelector(ctx, c.sbx, selector, initialPID, o.User, o.TimeoutMs, o.RequestTimeout, o.OnStdout, o.OnStderr, nil, retain)
}

func connectProcessSelector(
	ctx context.Context,
	sbx *Sandbox,
	selector *procpb.ProcessSelector,
	initialPID uint32,
	user string,
	timeoutMs int,
	reqTimeout time.Duration,
	onStdout, onStderr, onPty func([]byte),
	retainOutput bool,
) (*CommandHandle, error) {
	req := connect.NewRequest(&procpb.ConnectRequest{
		Process: selector,
	})
	if timeoutMs > 0 {
		req.Header().Set("Connect-Timeout-Ms", strconv.Itoa(timeoutMs))
	}

	callCtx, optCancel := withEnvdCallOpts(ctx, sbx, user, reqTimeout)
	streamCtx, streamCancel := context.WithCancel(callCtx)
	cancelAll := func() {
		streamCancel()
		optCancel()
	}

	stream, err := sbx.envdClients().Process.Connect(streamCtx, req)
	if err != nil {
		cancelAll()
		return nil, mapConnectErr(err)
	}

	handle := newCommandHandle(sbx, onStdout, onStderr, onPty, retainOutput)
	if initialPID != 0 {
		handle.pid = initialPID
		handle.markStarted()
	}
	handle.user = user
	handle.cancel = cancelAll
	handle.startStream(&connectStream{s: stream})
	return handle, nil
}

// List returns all commands and PTY sessions currently running inside the sandbox.
//
// Endpoint: process.Process/List (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) List(ctx context.Context, opts ...CommandRequestOptions) ([]ProcessInfo, error) {
	o := firstCommandReqOpts(opts)
	callCtx, cancel := withEnvdCallOpts(ctx, c.sbx, o.User, unaryEnvdTimeout(c.sbx, o.RequestTimeout))
	defer cancel()

	resp, err := c.sbx.envdClients().Process.List(callCtx, connect.NewRequest(&procpb.ListRequest{}))
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

// Kill sends SIGKILL to the command identified by PID. It returns (false, nil)
// if the process is no longer running (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) Kill(ctx context.Context, pid uint32, opts ...CommandRequestOptions) (bool, error) {
	return killProcess(ctx, c.sbx, pid, firstCommandReqOpts(opts))
}

// KillByTag sends SIGKILL to the command identified by tag. It returns
// (false, nil) if the process is no longer running (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) KillByTag(ctx context.Context, tag string, opts ...CommandRequestOptions) (bool, error) {
	if tag == "" {
		return false, &InvalidArgumentError{Message: "process tag is required"}
	}
	return sendProcessSignal(
		ctx,
		c.sbx,
		&procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Tag{Tag: tag}},
		procpb.Signal_SIGNAL_SIGKILL,
		firstCommandReqOpts(opts),
	)
}

// SendSignal sends sig (SignalSIGTERM or SignalSIGKILL) to the command
// identified by PID. It returns (false, nil) if the process is no longer
// running (Connect CodeNotFound).
//
// Endpoint: process.Process/SendSignal (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) SendSignal(ctx context.Context, pid uint32, sig Signal, opts ...CommandRequestOptions) (bool, error) {
	pbSig := procpb.Signal(sig)
	if pbSig != procpb.Signal_SIGNAL_SIGTERM && pbSig != procpb.Signal_SIGNAL_SIGKILL {
		return false, &InvalidArgumentError{Message: "signal must be SignalSIGTERM or SignalSIGKILL"}
	}
	return sendProcessSignal(
		ctx,
		c.sbx,
		&procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		pbSig,
		firstCommandReqOpts(opts),
	)
}

func killProcess(ctx context.Context, sbx *Sandbox, pid uint32, opts CommandRequestOptions) (bool, error) {
	return sendProcessSignal(
		ctx,
		sbx,
		&procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		procpb.Signal_SIGNAL_SIGKILL,
		opts,
	)
}

func sendProcessSignal(ctx context.Context, sbx *Sandbox, selector *procpb.ProcessSelector, sig procpb.Signal, opts CommandRequestOptions) (bool, error) {
	callCtx, cancel := withEnvdCallOpts(ctx, sbx, opts.User, unaryEnvdTimeout(sbx, opts.RequestTimeout))
	defer cancel()

	req := connect.NewRequest(&procpb.SendSignalRequest{
		Process: selector,
		Signal:  sig,
	})
	if _, err := sbx.envdClients().Process.SendSignal(callCtx, req); err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return false, nil
		}
		return false, mapConnectErr(err)
	}
	return true, nil
}

// SendStdin writes bytes to the stdin of the running process identified by PID.
//
// Endpoint: process.Process/SendInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) SendStdin(ctx context.Context, pid uint32, data []byte, opts ...CommandRequestOptions) error {
	o := firstCommandReqOpts(opts)
	callCtx, cancel := withEnvdCallOpts(ctx, c.sbx, o.User, unaryEnvdTimeout(c.sbx, o.RequestTimeout))
	defer cancel()

	req := connect.NewRequest(&procpb.SendInputRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
		Input:   &procpb.ProcessInput{Input: &procpb.ProcessInput_Stdin{Stdin: data}},
	})
	_, err := c.sbx.envdClients().Process.SendInput(callCtx, req)
	return mapConnectErr(err)
}

// StreamInput opens a persistent, ordered client-streaming stdin connection to
// the command identified by PID. Call SendData to write ordered chunks and
// Close to finish the stream and await envd's acknowledgement.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (c *Commands) StreamInput(ctx context.Context, pid uint32, opts ...CommandRequestOptions) (*CommandInputStream, error) {
	is, err := newInputStream(ctx, c.sbx, pid, firstCommandReqOpts(opts))
	if err != nil {
		return nil, err
	}
	return &CommandInputStream{s: is}, nil
}

// CloseStdin closes the stdin descriptor of the process identified by PID to
// signal EOF.
//
// Closing stdin requires envd >= v0.5.2; on an older build this returns
// *TemplateError up front instead of failing opaquely at the RPC layer.
//
// Endpoint: process.Process/CloseStdin (envd Connect-RPC)
// Envd: >= v0.5.2
func (c *Commands) CloseStdin(ctx context.Context, pid uint32, opts ...CommandRequestOptions) error {
	if compareEnvdVersions(c.sbx.envdVersionForGating(), envdEnvdClose) < 0 {
		return &TemplateError{Message: fmt.Sprintf(
			"closing stdin requires envd >= %s, but this sandbox runs %s; rebuild the template to use it",
			envdEnvdClose, c.sbx.envdVersionRaw())}
	}
	o := firstCommandReqOpts(opts)
	callCtx, cancel := withEnvdCallOpts(ctx, c.sbx, o.User, unaryEnvdTimeout(c.sbx, o.RequestTimeout))
	defer cancel()

	req := connect.NewRequest(&procpb.CloseStdinRequest{
		Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
	})
	_, err := c.sbx.envdClients().Process.CloseStdin(callCtx, req)
	return mapConnectErr(err)
}

// CommandInputStream is a persistent client-streaming stdin handle opened by
// Commands.StreamInput. All methods are safe for concurrent use.
type CommandInputStream struct {
	s *inputStream
}

// SendData sends a chunk of stdin bytes to the command in call order.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (cs *CommandInputStream) SendData(data []byte) error {
	payload := append([]byte(nil), data...)
	return cs.s.send(&procpb.ProcessInput{
		Input: &procpb.ProcessInput_Stdin{Stdin: payload},
	})
}

// Send is an alias for SendData.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (cs *CommandInputStream) Send(data []byte) error {
	return cs.SendData(data)
}

// Close ends the client input stream and waits for envd to acknowledge it.
// Calling Close multiple times is safe and returns the same error.
//
// Endpoint: process.Process/StreamInput (envd Connect-RPC)
// Envd: >= v0.1.0
func (cs *CommandInputStream) Close() error {
	return cs.s.close()
}

type inputStream struct {
	mu            sync.Mutex
	stream        *connect.ClientStreamForClient[procpb.StreamInputRequest, procpb.StreamInputResponse]
	cancel        context.CancelFunc
	stopKeepalive chan struct{}
	keepaliveDone chan struct{}
	finished      chan struct{}
	closed        bool
	closeErr      error
}

func newInputStream(ctx context.Context, sbx *Sandbox, pid uint32, opts CommandRequestOptions) (*inputStream, error) {
	callCtx, optCancel := withEnvdCallOpts(ctx, sbx, opts.User, opts.RequestTimeout)
	streamCtx, streamCancel := context.WithCancel(callCtx)
	cancelAll := func() {
		streamCancel()
		optCancel()
	}

	stream := sbx.envdClients().Process.StreamInput(streamCtx)
	startReq := &procpb.StreamInputRequest{
		Event: &procpb.StreamInputRequest_Start{
			Start: &procpb.StreamInputRequest_StartEvent{
				Process: &procpb.ProcessSelector{Selector: &procpb.ProcessSelector_Pid{Pid: pid}},
			},
		},
	}
	if err := stream.Send(startReq); err != nil {
		_, recvErr := stream.CloseAndReceive()
		cancelAll()
		if recvErr != nil {
			return nil, mapConnectErr(recvErr)
		}
		return nil, mapConnectErr(err)
	}

	is := &inputStream{
		stream:        stream,
		cancel:        cancelAll,
		stopKeepalive: make(chan struct{}),
		keepaliveDone: make(chan struct{}),
		finished:      make(chan struct{}),
	}
	go is.keepaliveLoop(streamCtx)
	return is, nil
}

func (is *inputStream) keepaliveLoop(ctx context.Context) {
	defer close(is.keepaliveDone)
	ticker := time.NewTicker(time.Duration(KeepAlivePingIntervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-is.stopKeepalive:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			is.mu.Lock()
			if !is.closed {
				_ = is.stream.Send(&procpb.StreamInputRequest{
					Event: &procpb.StreamInputRequest_Keepalive{
						Keepalive: &procpb.StreamInputRequest_KeepAlive{},
					},
				})
			}
			is.mu.Unlock()
		}
	}
}

func (is *inputStream) send(in *procpb.ProcessInput) error {
	is.mu.Lock()
	if is.closed {
		is.mu.Unlock()
		return newSandboxError("cannot send input after stream is closed", nil)
	}
	err := is.stream.Send(&procpb.StreamInputRequest{
		Event: &procpb.StreamInputRequest_Data{
			Data: &procpb.StreamInputRequest_DataEvent{Input: in},
		},
	})
	if err == nil {
		is.mu.Unlock()
		return nil
	}
	return is.finishLocked(err)
}

func (is *inputStream) close() error {
	is.mu.Lock()
	if is.closed {
		is.mu.Unlock()
		<-is.finished
		return is.closeErr
	}
	return is.finishLocked(nil)
}

func (is *inputStream) finishLocked(sendErr error) error {
	is.closed = true
	close(is.stopKeepalive)
	is.mu.Unlock()

	<-is.keepaliveDone

	is.mu.Lock()
	_, err := is.stream.CloseAndReceive()
	if err == nil {
		err = sendErr
	}
	mapped := mapConnectErr(err)
	is.closeErr = mapped
	is.mu.Unlock()

	is.cancel()
	close(is.finished)
	return mapped
}

func pumpStdin(ctx, streamCtx context.Context, h *CommandHandle, r io.Reader) {
	select {
	case <-h.started:
	case <-h.done:
		return
	case <-streamCtx.Done():
		return
	case <-ctx.Done():
		return
	}
	if h.PID() == 0 {
		return
	}
	buf := make([]byte, 32<<10)
	for {
		if streamCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		n, err := r.Read(buf)
		if streamCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		if n > 0 {
			if sendErr := h.SendStdin(ctx, buf[:n]); sendErr != nil {
				return
			}
		}
		if err != nil {
			if streamCtx.Err() == nil && ctx.Err() == nil &&
				errors.Is(err, io.EOF) && compareEnvdVersions(h.sbx.envdVersionForGating(), envdEnvdClose) >= 0 {
				_ = h.CloseStdin(ctx)
			}
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
