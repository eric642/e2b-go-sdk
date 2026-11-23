package e2b

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	procpb "github.com/eric642/e2b-go-sdk/internal/envd/process"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

type cmdTestProcHandler struct {
	processconnect.UnimplementedProcessHandler

	onStart       func(context.Context, *connect.Request[procpb.StartRequest], *connect.ServerStream[procpb.StartResponse]) error
	onConnect     func(context.Context, *connect.Request[procpb.ConnectRequest], *connect.ServerStream[procpb.ConnectResponse]) error
	onSendInput   func(context.Context, *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error)
	onCloseStdin  func(context.Context, *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error)
	onSendSignal  func(context.Context, *connect.Request[procpb.SendSignalRequest]) (*connect.Response[procpb.SendSignalResponse], error)
	onStreamInput func(context.Context, *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error)
}

func (h *cmdTestProcHandler) Start(ctx context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
	if h.onStart != nil {
		return h.onStart(ctx, req, stream)
	}
	return nil
}

func (h *cmdTestProcHandler) Connect(ctx context.Context, req *connect.Request[procpb.ConnectRequest], stream *connect.ServerStream[procpb.ConnectResponse]) error {
	if h.onConnect != nil {
		return h.onConnect(ctx, req, stream)
	}
	return nil
}

func (h *cmdTestProcHandler) SendInput(ctx context.Context, req *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
	if h.onSendInput != nil {
		return h.onSendInput(ctx, req)
	}
	return connect.NewResponse(&procpb.SendInputResponse{}), nil
}

func (h *cmdTestProcHandler) CloseStdin(ctx context.Context, req *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error) {
	if h.onCloseStdin != nil {
		return h.onCloseStdin(ctx, req)
	}
	return connect.NewResponse(&procpb.CloseStdinResponse{}), nil
}

func (h *cmdTestProcHandler) SendSignal(ctx context.Context, req *connect.Request[procpb.SendSignalRequest]) (*connect.Response[procpb.SendSignalResponse], error) {
	if h.onSendSignal != nil {
		return h.onSendSignal(ctx, req)
	}
	return connect.NewResponse(&procpb.SendSignalResponse{}), nil
}

func (h *cmdTestProcHandler) StreamInput(ctx context.Context, stream *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error) {
	if h.onStreamInput != nil {
		return h.onStreamInput(ctx, stream)
	}
	for stream.Receive() {
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procpb.StreamInputResponse{}), nil
}

func newCommandTestSandbox(t *testing.T, envdVer string, cfgMod func(*Config), handler http.Handler) (*Sandbox, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{
		APIKey:     "test-key",
		Domain:     "example.com",
		SandboxURL: srv.URL,
	}
	if cfgMod != nil {
		cfgMod(&cfg)
	}
	cfg = cfg.Resolve()

	dpHTTP := cfg.DataPlaneHTTPClient()
	envdAuth := transport.EnvdAuth{
		Token:     "tok",
		User:      resolveEnvdUser(envdVer, ""),
		SandboxID: "sbx-cmd-test",
		Headers:   cfg.envdHeaders(),
	}
	envdCli, err := transport.NewEnvdClients(srv.URL, dpHTTP, envdAuth)
	if err != nil {
		t.Fatal(err)
	}
	sbx := &Sandbox{
		ID:              "sbx-cmd-test",
		Domain:          "example.com",
		EnvdVersion:     envdVer,
		EnvdAccessToken: "tok",
		cfg:             cfg,
		envd:            envdCli,
		httpCli:         dpHTTP,
	}
	sbx.Commands = &Commands{sbx: sbx}
	sbx.Pty = &Pty{sbx: sbx}
	return sbx, srv
}

func newProcTestSandbox(t *testing.T, envdVer string, cfgMod func(*Config), svc *cmdTestProcHandler) (*Sandbox, *httptest.Server) {
	t.Helper()
	procPath, procHandler := processconnect.NewProcessHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(procPath, procHandler)
	return newCommandTestSandbox(t, envdVer, cfgMod, mux)
}

func TestCommandsRequestTimeoutBoundsHandshakeOnly(t *testing.T) {
	t.Run("RunLongerThanRequestTimeout", func(t *testing.T) {
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				select {
				case <-time.After(10 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				if err := stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 101}},
					},
				}); err != nil {
					return err
				}
				select {
				case <-time.After(200 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				if err := stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Data{
							Data: &procpb.ProcessEvent_DataEvent{
								Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("completed")},
							},
						},
					},
				}); err != nil {
					return err
				}
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		h, err := sbx.Commands.Run(context.Background(), "long-cmd", RunOptions{
			RequestTimeout: 80 * time.Millisecond,
			TimeoutMs:      0,
		})
		if err != nil {
			t.Fatalf("Commands.Run failed despite StartEvent arriving before RequestTimeout: %v", err)
		}
		res, err := h.Wait(context.Background())
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if res.Stdout != "completed" || res.ExitCode != 0 {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("ConfigRequestTimeoutBoundsHandshakeOnly", func(t *testing.T) {
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				if err := stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 102}},
					},
				}); err != nil {
					return err
				}
				select {
				case <-time.After(180 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", func(c *Config) {
			c.RequestTimeout = 70 * time.Millisecond
		}, svc)

		h, err := sbx.Commands.Run(context.Background(), "long-cmd", RunOptions{TimeoutMs: 0})
		if err != nil {
			t.Fatalf("Commands.Run should not be killed by Config.RequestTimeout after handshake: %v", err)
		}
		if _, err := h.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	})

	t.Run("StallsBeforeHeaders", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], _ *connect.ServerStream[procpb.StartResponse]) error {
				select {
				case <-ctx.Done():
				case <-release:
				}
				return ctx.Err()
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		_, err := sbx.Commands.Run(context.Background(), "stalled", RunOptions{
			RequestTimeout: 80 * time.Millisecond,
			TimeoutMs:      0,
		})
		te, ok := errors.AsType[*TimeoutError](err)
		if !ok {
			t.Fatalf("want *TimeoutError on stalled handshake, got %T: %v", err, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handshake timeout should wrap context.DeadlineExceeded, got %v", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("handshake timeout must not be reported as context.Canceled: %v", err)
		}
		if !strings.Contains(te.Message, "handshake") {
			t.Fatalf("expected handshake timeout message, got %q", te.Message)
		}
	})

	t.Run("StallsAfterHeadersBeforeStartEvent", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		mux := http.NewServeMux()
		mux.HandleFunc("/process.Process/Start", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/connect+json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		sbx, _ := newCommandTestSandbox(t, "0.6.13", nil, mux)

		_, err := sbx.Commands.Run(context.Background(), "stalled-after-headers", RunOptions{
			RequestTimeout: 80 * time.Millisecond,
			TimeoutMs:      0,
		})
		te, ok := errors.AsType[*TimeoutError](err)
		if !ok {
			t.Fatalf("want *TimeoutError when StartEvent never arrives, got %T: %v", err, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handshake timeout should wrap context.DeadlineExceeded, got %v", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("handshake timeout must not be reported as context.Canceled: %v", err)
		}
		if !strings.Contains(te.Message, "handshake") {
			t.Fatalf("expected handshake timeout message, got %q", te.Message)
		}
	})

	t.Run("ParentContextCancelDistinguishedFromHandshakeTimeout", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		entered := make(chan struct{})
		mux := http.NewServeMux()
		mux.HandleFunc("/process.Process/Start", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/connect+json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		sbx, _ := newCommandTestSandbox(t, "0.6.13", nil, mux)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-entered
			cancel()
		}()

		_, err := sbx.Commands.Run(ctx, "parent-cancel", RunOptions{
			RequestTimeout: 5 * time.Second,
			TimeoutMs:      0,
		})
		if err == nil {
			t.Fatal("expected error on parent ctx cancel")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("parent cancel should unwrap context.Canceled, got %T: %v", err, err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("parent cancel must not wrap context.DeadlineExceeded: %v", err)
		}
		if strings.Contains(err.Error(), "handshake") {
			t.Fatalf("parent cancel must not claim handshake timeout: %v", err)
		}
	})

	t.Run("ConnectAndPtyLongerThanRequestTimeout", func(t *testing.T) {
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				if err := stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 201}},
					},
				}); err != nil {
					return err
				}
				select {
				case <-time.After(160 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Data{
							Data: &procpb.ProcessEvent_DataEvent{
								Output: &procpb.ProcessEvent_DataEvent_Pty{Pty: []byte("pty-long-ok")},
							},
						},
					},
				})
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
			onConnect: func(ctx context.Context, req *connect.Request[procpb.ConnectRequest], stream *connect.ServerStream[procpb.ConnectResponse]) error {
				if err := stream.Send(&procpb.ConnectResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: req.Msg.GetProcess().GetPid()}},
					},
				}); err != nil {
					return err
				}
				select {
				case <-time.After(160 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				_ = stream.Send(&procpb.ConnectResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Data{
							Data: &procpb.ProcessEvent_DataEvent{
								Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("conn-long-ok")},
							},
						},
					},
				})
				return stream.Send(&procpb.ConnectResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		var ptyOut bytes.Buffer
		ptyH, err := sbx.Pty.Create(context.Background(), PtyOptions{
			RequestTimeout: 60 * time.Millisecond,
			OnData:         func(b []byte) { ptyOut.Write(b) },
		})
		if err != nil {
			t.Fatalf("Pty.Create: %v", err)
		}
		if _, err := ptyH.Wait(context.Background()); err != nil {
			t.Fatalf("Pty Wait: %v", err)
		}
		if ptyOut.String() != "pty-long-ok" {
			t.Fatalf("pty output=%q, want pty-long-ok", ptyOut.String())
		}

		connH, err := sbx.Commands.Connect(context.Background(), 201, CommandConnectOptions{
			RequestTimeout: 60 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Commands.Connect: %v", err)
		}
		res, err := connH.Wait(context.Background())
		if err != nil {
			t.Fatalf("Commands.Connect Wait: %v", err)
		}
		if res.Stdout != "conn-long-ok" {
			t.Fatalf("Connect stdout=%q, want conn-long-ok", res.Stdout)
		}
	})

	t.Run("StreamInputLongerThanRequestTimeout", func(t *testing.T) {
		var mu sync.Mutex
		var got []string
		svc := &cmdTestProcHandler{
			onStreamInput: func(_ context.Context, stream *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error) {
				for stream.Receive() {
					if data, ok := stream.Msg().GetEvent().(*procpb.StreamInputRequest_Data); ok {
						mu.Lock()
						got = append(got, string(data.Data.GetInput().GetStdin()))
						mu.Unlock()
					}
				}
				if err := stream.Err(); err != nil {
					return nil, err
				}
				return connect.NewResponse(&procpb.StreamInputResponse{}), nil
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		in, err := sbx.Commands.StreamInput(context.Background(), 42, CommandRequestOptions{
			RequestTimeout: 60 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("StreamInput: %v", err)
		}
		time.Sleep(140 * time.Millisecond)
		if err := in.SendData([]byte("after-req-timeout")); err != nil {
			t.Fatalf("SendData after RequestTimeout elapsed: %v", err)
		}
		if err := in.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 || got[0] != "after-req-timeout" {
			t.Fatalf("got=%v, want [after-req-timeout]", got)
		}
	})

	t.Run("StreamInputHandshakeStallTimesOut", func(t *testing.T) {
		stalledRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		sbx, _ := newProcTestSandbox(t, "0.6.13", func(c *Config) {
			c.HTTPClient = &http.Client{Transport: stalledRT}
		}, &cmdTestProcHandler{})

		_, err := sbx.Commands.StreamInput(context.Background(), 42, CommandRequestOptions{
			RequestTimeout: 60 * time.Millisecond,
		})
		te, ok := errors.AsType[*TimeoutError](err)
		if !ok {
			t.Fatalf("want *TimeoutError on stalled StreamInput handshake, got %T: %v", err, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cause chain for StreamInput handshake timeout: %v", err)
		}
		if !strings.Contains(te.Message, "handshake") {
			t.Fatalf("expected handshake timeout message, got %q", te.Message)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCommandHandleSliceIsolationChannelsAndDisconnect(t *testing.T) {
	t.Run("CallbackMutationDoesNotCorruptChannelOrResult", func(t *testing.T) {
		svc := &cmdTestProcHandler{
			onStart: func(_ context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 55}},
					},
				})
				if req.Msg.GetPty() != nil {
					_ = stream.Send(&procpb.StartResponse{
						Event: &procpb.ProcessEvent{
							Event: &procpb.ProcessEvent_Data{
								Data: &procpb.ProcessEvent_DataEvent{
									Output: &procpb.ProcessEvent_DataEvent_Pty{Pty: []byte("pty-original")},
								},
							},
						},
					})
				} else {
					_ = stream.Send(&procpb.StartResponse{
						Event: &procpb.ProcessEvent{
							Event: &procpb.ProcessEvent_Data{
								Data: &procpb.ProcessEvent_DataEvent{
									Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("stdout-original")},
								},
							},
						},
					})
					_ = stream.Send(&procpb.StartResponse{
						Event: &procpb.ProcessEvent{
							Event: &procpb.ProcessEvent_Data{
								Data: &procpb.ProcessEvent_DataEvent{
									Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte("stderr-original")},
								},
							},
						},
					})
				}
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		h, err := sbx.Commands.Start(context.Background(), "cmd", RunOptions{
			OnStdout: func(b []byte) {
				for i := range b {
					b[i] = 'X'
				}
			},
			OnStderr: func(b []byte) {
				for i := range b {
					b[i] = 'Y'
				}
			},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		res, err := h.Wait(context.Background())
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if res.Stdout != "stdout-original" || res.Stderr != "stderr-original" {
			t.Fatalf("CommandResult corrupted by callback mutation: %+v", res)
		}

		var chStdout, chStderr strings.Builder
		for chunk := range h.Stdout() {
			chStdout.Write(chunk)
		}
		for chunk := range h.Stderr() {
			chStderr.Write(chunk)
		}
		if chStdout.String() != "stdout-original" || chStderr.String() != "stderr-original" {
			t.Fatalf("channel slices aliased with callback: stdout=%q stderr=%q", chStdout.String(), chStderr.String())
		}

		// Verify PTY callback vs PtyOutput() isolation and retainOutput=false.
		ptyH, err := sbx.Pty.Create(context.Background(), PtyOptions{
			OnData: func(b []byte) {
				for i := range b {
					b[i] = 'Z'
				}
			},
		})
		if err != nil {
			t.Fatalf("Pty.Create: %v", err)
		}
		if ptyH.retainOutput {
			t.Fatal("Pty.Create should set retainOutput=false")
		}
		if _, err := ptyH.Wait(context.Background()); err != nil {
			t.Fatalf("Pty Wait: %v", err)
		}
		var chPty strings.Builder
		for chunk := range ptyH.PtyOutput() {
			chPty.Write(chunk)
		}
		if chPty.String() != "pty-original" {
			t.Fatalf("PtyOutput() aliased with OnData callback: got %q", chPty.String())
		}
	})

	t.Run("SlowChannelConsumerDropsWithoutBlockingAndDoneClosesLast", func(t *testing.T) {
		const totalChunks = 80
		svc := &cmdTestProcHandler{
			onStart: func(_ context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 56}},
					},
				})
				for range totalChunks {
					_ = stream.Send(&procpb.StartResponse{
						Event: &procpb.ProcessEvent{
							Event: &procpb.ProcessEvent_Data{
								Data: &procpb.ProcessEvent_DataEvent{
									Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("a")},
								},
							},
						},
					})
					_ = stream.Send(&procpb.StartResponse{
						Event: &procpb.ProcessEvent{
							Event: &procpb.ProcessEvent_Data{
								Data: &procpb.ProcessEvent_DataEvent{
									Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte("b")},
								},
							},
						},
					})
				}
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		var cbStdout, cbStderr int
		h, err := sbx.Commands.Start(context.Background(), "noisy", RunOptions{
			OnStdout: func([]byte) { cbStdout++ },
			OnStderr: func([]byte) { cbStderr++ },
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		res, err := h.Wait(context.Background())
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if cbStdout != totalChunks || cbStderr != totalChunks {
			t.Fatalf("callbacks got (%d, %d), want (%d, %d)", cbStdout, cbStderr, totalChunks, totalChunks)
		}
		if len(res.Stdout) != totalChunks || len(res.Stderr) != totalChunks {
			t.Fatalf("CommandResult lens=(%d, %d), want (%d, %d)", len(res.Stdout), len(res.Stderr), totalChunks, totalChunks)
		}

		// Because defer close(h.done) runs last, all channels must already be closed
		// when Wait() returns (draining buffered chunks must not block).
		drainImmediate := func(ch <-chan []byte) int {
			n := 0
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return n
					}
					n++
				default:
					t.Fatal("channel was not closed before Wait()/done returned")
				}
			}
		}
		if n := drainImmediate(h.Stdout()); n != 32 {
			t.Fatalf("Stdout buffered count=%d, want 32", n)
		}
		if n := drainImmediate(h.Stderr()); n != 32 {
			t.Fatalf("Stderr buffered count=%d, want 32", n)
		}
		if n := drainImmediate(h.PtyOutput()); n != 0 {
			t.Fatalf("PtyOutput buffered count=%d, want 0", n)
		}
	})

	t.Run("ReentrantDisconnectFromCallbackDoesNotDeadlock", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 57}},
					},
				})
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Data{
							Data: &procpb.ProcessEvent_DataEvent{
								Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("stop-here")},
							},
						},
					},
				})
				select {
				case <-ctx.Done():
				case <-release:
				}
				return ctx.Err()
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		var h *CommandHandle
		ready := make(chan struct{})
		var err error
		h, err = sbx.Commands.Start(context.Background(), "reentrant", RunOptions{
			OnStdout: func([]byte) {
				<-ready
				h.Disconnect()
			},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		close(ready)

		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err = h.Wait(waitCtx)
		if err == nil {
			t.Fatal("expected disconnect error from Wait")
		}
		if _, ok := errors.AsType[*SandboxError](err); !ok {
			t.Fatalf("want *SandboxError after Disconnect before exit, got %T: %v", err, err)
		}
	})
}

type fakeStalledInputStream struct {
	ctx        context.Context
	onSend     func(*procpb.StreamInputRequest) error
	onClose    func() (*connect.Response[procpb.StreamInputResponse], error)
	sendCount  atomic.Int32
	closeCount atomic.Int32
}

func (f *fakeStalledInputStream) Send(req *procpb.StreamInputRequest) error {
	f.sendCount.Add(1)
	if f.onSend != nil {
		return f.onSend(req)
	}
	return nil
}

func (f *fakeStalledInputStream) CloseAndReceive() (*connect.Response[procpb.StreamInputResponse], error) {
	f.closeCount.Add(1)
	if f.onClose != nil {
		return f.onClose()
	}
	return connect.NewResponse(&procpb.StreamInputResponse{}), nil
}

func TestInputStreamStalledSendAndKeepaliveErrors(t *testing.T) {
	t.Run("StalledKeepaliveSendUnblockedByClose", func(t *testing.T) {
		streamCtx, cancel := context.WithCancel(context.Background())
		inKeepalive := make(chan struct{})
		var once sync.Once
		fake := &fakeStalledInputStream{
			ctx: streamCtx,
			onSend: func(req *procpb.StreamInputRequest) error {
				if _, ok := req.GetEvent().(*procpb.StreamInputRequest_Keepalive); ok {
					once.Do(func() { close(inKeepalive) })
					<-streamCtx.Done()
					return streamCtx.Err()
				}
				return nil
			},
			onClose: func() (*connect.Response[procpb.StreamInputResponse], error) {
				return nil, streamCtx.Err()
			},
		}

		is := startInputStream(streamCtx, cancel, fake, 5*time.Millisecond)
		select {
		case <-inKeepalive:
		case <-time.After(time.Second):
			t.Fatal("keepalive never triggered")
		}

		closeDone := make(chan error, 1)
		go func() {
			closeDone <- is.close()
		}()

		select {
		case err := <-closeDone:
			if err == nil {
				t.Fatal("expected error from closing stalled keepalive stream")
			}
		case <-time.After(time.Second):
			t.Fatal("Close() hung on stalled keepalive Send")
		}

		select {
		case <-is.keepaliveDone:
		case <-time.After(time.Second):
			t.Fatal("writerLoop did not exit after Close()")
		}
	})

	t.Run("StalledDataSendUnblockedByClose", func(t *testing.T) {
		streamCtx, cancel := context.WithCancel(context.Background())
		inSend := make(chan struct{})
		fake := &fakeStalledInputStream{
			ctx: streamCtx,
			onSend: func(_ *procpb.StreamInputRequest) error {
				close(inSend)
				<-streamCtx.Done()
				return streamCtx.Err()
			},
			onClose: func() (*connect.Response[procpb.StreamInputResponse], error) {
				return nil, streamCtx.Err()
			},
		}

		is := startInputStream(streamCtx, cancel, fake, time.Hour)
		cs := &CommandInputStream{s: is}

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- cs.SendData([]byte("stalled-payload"))
		}()

		select {
		case <-inSend:
		case <-time.After(time.Second):
			t.Fatal("SendData never reached stream.Send")
		}

		closeDone := make(chan error, 1)
		go func() {
			closeDone <- cs.Close()
		}()

		select {
		case err := <-closeDone:
			if err == nil {
				t.Fatal("expected error from Close()")
			}
		case <-time.After(time.Second):
			t.Fatal("Close() hung while SendData was stalled")
		}
		select {
		case err := <-sendDone:
			if err == nil {
				t.Fatal("expected error from unblocked SendData()")
			}
		case <-time.After(time.Second):
			t.Fatal("SendData() did not unblock after Close()")
		}
		select {
		case <-is.keepaliveDone:
		case <-time.After(time.Second):
			t.Fatal("writerLoop leaked after stalled SendData + Close()")
		}
	})

	t.Run("KeepaliveSendErrorTerminatesLoopInsteadOfSpinning", func(t *testing.T) {
		streamCtx, cancel := context.WithCancel(context.Background())
		brokenErr := connect.NewError(connect.CodeUnavailable, errors.New("connection broken"))
		fake := &fakeStalledInputStream{
			ctx: streamCtx,
			onSend: func(_ *procpb.StreamInputRequest) error {
				return brokenErr
			},
			onClose: func() (*connect.Response[procpb.StreamInputResponse], error) {
				return nil, brokenErr
			},
		}

		is := startInputStream(streamCtx, cancel, fake, 5*time.Millisecond)
		select {
		case <-is.keepaliveDone:
		case <-time.After(time.Second):
			t.Fatal("writerLoop kept spinning after keepalive Send error")
		}

		if got := fake.sendCount.Load(); got != 1 {
			t.Fatalf("keepalive Send count=%d, want 1 (must stop on first error)", got)
		}
		if err := is.close(); err == nil {
			t.Fatal("Close() should surface keepalive stream error")
		} else if _, ok := errors.AsType[*TimeoutError](err); !ok {
			t.Fatalf("want *TimeoutError from CodeUnavailable, got %T: %v", err, err)
		}
		if err := is.send(&procpb.ProcessInput{Input: &procpb.ProcessInput_Stdin{Stdin: []byte("x")}}); err == nil {
			t.Fatal("send after dead keepalive stream should fail")
		}
	})

	t.Run("ConcurrentSendDataAndCloseUnderRace", func(t *testing.T) {
		streamCtx, cancel := context.WithCancel(context.Background())
		fake := &fakeStalledInputStream{ctx: streamCtx}
		is := startInputStream(streamCtx, cancel, fake, 2*time.Millisecond)
		cs := &CommandInputStream{s: is}

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 20 {
					_ = cs.SendData([]byte("chunk"))
				}
			})
		}
		wg.Go(func() {
			time.Sleep(2 * time.Millisecond)
			_ = cs.Close()
		})
		wg.Wait()
		_ = cs.Close()

		select {
		case <-is.keepaliveDone:
		case <-time.After(time.Second):
			t.Fatal("writerLoop did not terminate")
		}
	})
}

func TestPumpStdinStreamCtxAndCloseOnError(t *testing.T) {
	t.Run("DisconnectAbortsInFlightSendStdin", func(t *testing.T) {
		inSendInput := make(chan struct{})
		sendAborted := make(chan struct{})
		releaseStart := make(chan struct{})
		defer close(releaseStart)

		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 301}},
					},
				})
				select {
				case <-ctx.Done():
				case <-releaseStart:
				}
				return ctx.Err()
			},
			onSendInput: func(ctx context.Context, _ *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
				close(inSendInput)
				<-ctx.Done()
				close(sendAborted)
				return nil, ctx.Err()
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		h, err := sbx.Commands.Start(context.Background(), "cat", RunOptions{
			Stdin: bytes.NewReader([]byte("hello stdin")),
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		select {
		case <-inSendInput:
		case <-time.After(2 * time.Second):
			t.Fatal("pumpStdin never called SendStdin")
		}

		// Parent context is context.Background() (never canceled); Disconnect must
		// cancel streamCtx and abort the in-flight SendStdin immediately.
		h.Disconnect()

		select {
		case <-sendAborted:
		case <-time.After(2 * time.Second):
			t.Fatal("Disconnect() did not abort in-flight SendStdin")
		}
	})

	t.Run("SendStdinErrorWhileStreamAliveAttemptsCloseStdin", func(t *testing.T) {
		closeStdinCalled := make(chan struct{})
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
				_ = stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 302}},
					},
				})
				select {
				case <-closeStdinCalled:
				case <-ctx.Done():
					return ctx.Err()
				}
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
					},
				})
			},
			onSendInput: func(_ context.Context, _ *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
				return nil, connect.NewError(connect.CodeInternal, errors.New("write error"))
			},
			onCloseStdin: func(_ context.Context, _ *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error) {
				close(closeStdinCalled)
				return connect.NewResponse(&procpb.CloseStdinResponse{}), nil
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)

		h, err := sbx.Commands.Run(context.Background(), "cat", RunOptions{
			Stdin: bytes.NewReader([]byte("trigger-send-error")),
		})
		if err != nil {
			t.Fatalf("Commands.Run: %v", err)
		}
		if _, err := h.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	})

	t.Run("OldEnvdRejectsCloseStdinFast", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		var closeCalls atomic.Int32
		svc := &cmdTestProcHandler{
			onStart: func(ctx context.Context, _ *connect.Request[procpb.StartRequest], _ *connect.ServerStream[procpb.StartResponse]) error {
				select {
				case <-ctx.Done():
				case <-release:
				}
				return ctx.Err()
			},
			onCloseStdin: func(_ context.Context, _ *connect.Request[procpb.CloseStdinRequest]) (*connect.Response[procpb.CloseStdinResponse], error) {
				closeCalls.Add(1)
				return connect.NewResponse(&procpb.CloseStdinResponse{}), nil
			},
		}
		sbx, _ := newProcTestSandbox(t, "0.5.1", nil, svc)

		if err := sbx.Commands.CloseStdin(context.Background(), 42); err == nil {
			t.Fatal("expected *TemplateError for Commands.CloseStdin on envd 0.5.1")
		} else if _, ok := errors.AsType[*TemplateError](err); !ok {
			t.Fatalf("want *TemplateError, got %T: %v", err, err)
		}

		// CommandHandle.CloseStdin must fail fast without waiting for PID.
		h := newCommandHandle(sbx, nil, nil, nil, false)
		if err := h.CloseStdin(context.Background()); err == nil {
			t.Fatal("expected *TemplateError for CommandHandle.CloseStdin on envd 0.5.1")
		} else if _, ok := errors.AsType[*TemplateError](err); !ok {
			t.Fatalf("want *TemplateError, got %T: %v", err, err)
		}
		if closeCalls.Load() != 0 {
			t.Fatalf("CloseStdin RPC should not have been called on envd 0.5.1")
		}
	})
}

func TestCommandsConnectHandleKillChannelsAndPtySend(t *testing.T) {
	var (
		mu         sync.Mutex
		alivePIDs  = map[uint32]bool{42: true, 77: true}
		ptyInputs  []string
		streamPtys []string
	)
	svc := &cmdTestProcHandler{
		onStart: func(_ context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
			_ = stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 42}},
				},
			})
			if req.Msg.GetProcess().GetCmd() == "fail-exit" {
				return stream.Send(&procpb.StartResponse{
					Event: &procpb.ProcessEvent{
						Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 9, Error: new("boom"), Exited: true}},
					},
				})
			}
			_ = stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Data{
						Data: &procpb.ProcessEvent_DataEvent{
							Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("std-out")},
						},
					},
				},
			})
			_ = stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Data{
						Data: &procpb.ProcessEvent_DataEvent{
							Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte("std-err")},
						},
					},
				},
			})
			return stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
				},
			})
		},
		onConnect: func(_ context.Context, req *connect.Request[procpb.ConnectRequest], stream *connect.ServerStream[procpb.ConnectResponse]) error {
			pid := req.Msg.GetProcess().GetPid()
			_ = stream.Send(&procpb.ConnectResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: pid}},
				},
			})
			_ = stream.Send(&procpb.ConnectResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Data{
						Data: &procpb.ProcessEvent_DataEvent{
							Output: &procpb.ProcessEvent_DataEvent_Stdout{Stdout: []byte("connected-out")},
						},
					},
				},
			})
			_ = stream.Send(&procpb.ConnectResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Data{
						Data: &procpb.ProcessEvent_DataEvent{
							Output: &procpb.ProcessEvent_DataEvent_Stderr{Stderr: []byte("connected-err")},
						},
					},
				},
			})
			return stream.Send(&procpb.ConnectResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
				},
			})
		},
		onSendSignal: func(_ context.Context, req *connect.Request[procpb.SendSignalRequest]) (*connect.Response[procpb.SendSignalResponse], error) {
			pid := req.Msg.GetProcess().GetPid()
			mu.Lock()
			ok := alivePIDs[pid]
			delete(alivePIDs, pid)
			mu.Unlock()
			if !ok {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
			}
			return connect.NewResponse(&procpb.SendSignalResponse{}), nil
		},
		onSendInput: func(_ context.Context, req *connect.Request[procpb.SendInputRequest]) (*connect.Response[procpb.SendInputResponse], error) {
			mu.Lock()
			ptyInputs = append(ptyInputs, string(req.Msg.GetInput().GetPty()))
			mu.Unlock()
			return connect.NewResponse(&procpb.SendInputResponse{}), nil
		},
		onStreamInput: func(_ context.Context, stream *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error) {
			for stream.Receive() {
				if d, ok := stream.Msg().GetEvent().(*procpb.StreamInputRequest_Data); ok {
					mu.Lock()
					streamPtys = append(streamPtys, string(d.Data.GetInput().GetPty()))
					mu.Unlock()
				}
			}
			return connect.NewResponse(&procpb.StreamInputResponse{}), stream.Err()
		},
	}
	sbx, _ := newProcTestSandbox(t, "0.6.13", nil, svc)
	ctx := context.Background()

	// Commands.Connect + Stdout/Stderr channels + CommandHandle.Kill
	connH, err := sbx.Commands.Connect(ctx, 42)
	if err != nil {
		t.Fatalf("Commands.Connect: %v", err)
	}
	if connH.PID() != 42 {
		t.Fatalf("PID=%d, want 42", connH.PID())
	}
	killed, err := connH.Kill(ctx)
	if err != nil || !killed {
		t.Fatalf("CommandHandle.Kill (1st) = (%v, %v), want (true, nil)", killed, err)
	}
	killed2, err := connH.Kill(ctx)
	if err != nil || killed2 {
		t.Fatalf("CommandHandle.Kill (2nd) = (%v, %v), want (false, nil)", killed2, err)
	}
	res, err := connH.Wait(ctx)
	if err != nil {
		t.Fatalf("Connect Wait: %v", err)
	}
	if res.Stdout != "connected-out" || res.Stderr != "connected-err" {
		t.Fatalf("unexpected Connect result: %+v", res)
	}

	// Pty.SendInput + PtyInputStream.Send
	if err := sbx.Pty.SendInput(ctx, 77, []byte("uname\r")); err != nil {
		t.Fatalf("Pty.SendInput: %v", err)
	}
	ptyIn, err := sbx.Pty.StreamInput(ctx, 77)
	if err != nil {
		t.Fatalf("Pty.StreamInput: %v", err)
	}
	if err := ptyIn.Send([]byte("whoami\r")); err != nil {
		t.Fatalf("PtyInputStream.Send: %v", err)
	}
	if err := ptyIn.Close(); err != nil {
		t.Fatalf("PtyInputStream.Close: %v", err)
	}
	mu.Lock()
	gotPtyInputs := append([]string(nil), ptyInputs...)
	gotStreamPtys := append([]string(nil), streamPtys...)
	mu.Unlock()
	if len(gotPtyInputs) != 1 || gotPtyInputs[0] != "uname\r" {
		t.Fatalf("ptyInputs=%v, want [uname\\r]", gotPtyInputs)
	}
	if len(gotStreamPtys) != 1 || gotStreamPtys[0] != "whoami\r" {
		t.Fatalf("streamPtys=%v, want [whoami\\r]", gotStreamPtys)
	}

	// Commands.CloseStdin happy path
	if err := sbx.Commands.CloseStdin(ctx, 42); err != nil {
		t.Fatalf("Commands.CloseStdin: %v", err)
	}

	// Non-zero exit returns *CommandExitError
	_, err = sbx.Commands.Run(ctx, "fail-exit", RunOptions{})
	if cee, ok := errors.AsType[*CommandExitError](err); !ok || cee.Result.ExitCode != 9 {
		t.Fatalf("want *CommandExitError(9), got %T: %v", err, err)
	}

	// Validation error paths
	if _, err := sbx.Commands.SendSignal(ctx, 42, Signal(999)); err == nil {
		t.Fatal("expected *InvalidArgumentError for invalid signal")
	} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T", err)
	}
	if _, err := sbx.Commands.ConnectByTag(ctx, ""); err == nil {
		t.Fatal("expected *InvalidArgumentError for empty ConnectByTag")
	} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T", err)
	}
	if _, err := sbx.Commands.KillByTag(ctx, ""); err == nil {
		t.Fatal("expected *InvalidArgumentError for empty KillByTag")
	} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T", err)
	}
	if _, err := sbx.Pty.ConnectByTag(ctx, "", PtyConnectOptions{}); err == nil {
		t.Fatal("expected *InvalidArgumentError for empty Pty.ConnectByTag")
	} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
		t.Fatalf("want *InvalidArgumentError, got %T", err)
	}
}

func TestCommandStreamsNoGoroutineLeaks(t *testing.T) {
	release := make(chan struct{})
	svc := &cmdTestProcHandler{
		onStart: func(ctx context.Context, req *connect.Request[procpb.StartRequest], stream *connect.ServerStream[procpb.StartResponse]) error {
			_ = stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_Start{Start: &procpb.ProcessEvent_StartEvent{Pid: 400}},
				},
			})
			if req.Msg.GetProcess().GetCmd() == "hold" {
				select {
				case <-ctx.Done():
				case <-release:
				}
				return ctx.Err()
			}
			return stream.Send(&procpb.StartResponse{
				Event: &procpb.ProcessEvent{
					Event: &procpb.ProcessEvent_End{End: &procpb.ProcessEvent_EndEvent{ExitCode: 0, Exited: true}},
				},
			})
		},
		onStreamInput: func(_ context.Context, stream *connect.ClientStream[procpb.StreamInputRequest]) (*connect.Response[procpb.StreamInputResponse], error) {
			for stream.Receive() {
			}
			return connect.NewResponse(&procpb.StreamInputResponse{}), stream.Err()
		},
	}
	sbx, srv := newProcTestSandbox(t, "0.6.13", nil, svc)

	// Warm up one cycle so HTTP client connection pool & test server are steady.
	if _, err := sbx.Commands.Run(context.Background(), "warmup", RunOptions{}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	srv.CloseClientConnections()
	sbx.httpCli.CloseIdleConnections()
	runtime.GC()
	time.Sleep(30 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const cycles = 10
	for range cycles {
		h, err := sbx.Commands.Run(context.Background(), "fast", RunOptions{
			RequestTimeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if _, err := h.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}

		bg, err := sbx.Commands.Start(context.Background(), "hold", RunOptions{
			RequestTimeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Start hold: %v", err)
		}
		if _, err := bg.WaitPID(context.Background()); err != nil {
			t.Fatalf("WaitPID: %v", err)
		}
		bg.Disconnect()
		<-bg.done

		in, err := sbx.Commands.StreamInput(context.Background(), 400, CommandRequestOptions{
			RequestTimeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("StreamInput: %v", err)
		}
		if err := in.SendData([]byte("ping")); err != nil {
			t.Fatalf("SendData: %v", err)
		}
		if err := in.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	close(release)

	srv.CloseClientConnections()
	sbx.httpCli.CloseIdleConnections()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseline+4 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak detected after %d stream cycles: baseline=%d current=%d", cycles, baseline, runtime.NumGoroutine())
}

func TestCloseStdinRejectedOnOldEnvd(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.5.1", cfg: Config{}}
	c := &Commands{sbx: sbx}
	err := c.CloseStdin(context.Background(), 123)
	if err == nil {
		t.Fatal("CloseStdin should error on envd<0.5.2")
	}
	if _, ok := errors.AsType[*TemplateError](err); !ok {
		t.Fatalf("want *TemplateError, got %T: %v", err, err)
	}
}
