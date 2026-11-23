package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type fakeStep struct {
	status int
	hdr    map[string]string
	err    error
	fn     func(*http.Request) (*http.Response, error)
}

type fakeRT struct {
	mu     sync.Mutex
	calls  int
	bodies []string
	steps  []fakeStep
	closed []*trackBody
}

type trackBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackBody) Close() error { b.closed.Store(true); return nil }

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
	}
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	step := f.steps[i]
	if step.fn != nil {
		return step.fn(req)
	}
	if step.status == 0 && step.err != nil {
		return nil, step.err
	}
	return f.makeResp(req, step.status, step.hdr), step.err
}

func (f *fakeRT) makeResp(req *http.Request, status int, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	body := &trackBody{Reader: strings.NewReader("body")}
	f.mu.Lock()
	f.closed = append(f.closed, body)
	f.mu.Unlock()
	return &http.Response{StatusCode: status, Header: h, Body: body, Request: req}
}

func (f *fakeRT) respond(status int, hdr map[string]string) fakeStep {
	return fakeStep{status: status, hdr: hdr}
}

type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err()
}

func newTestRetry(base http.RoundTripper, n int) (*RetryTransport, *sleepRecorder) {
	rec := &sleepRecorder{}
	return &RetryTransport{
		Base:       base,
		MaxRetries: n,
		sleep:      rec.sleep,
		random:     func() float64 { return 0 },
	}, rec
}

func doReq(t *testing.T, rt http.RoundTripper, method, body string) (*http.Response, error) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "https://api.example.com/sandboxes", r)
	if err != nil {
		t.Fatal(err)
	}
	return rt.RoundTrip(req)
}

func TestRetryOnRetryableStatuses(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		f := &fakeRT{}
		f.steps = []fakeStep{f.respond(status, nil), f.respond(200, nil)}
		rt, rec := newTestRetry(f, 3)
		resp, err := doReq(t, rt, http.MethodPost, `{"a":1}`)
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || f.calls != 2 {
			t.Fatalf("%d: status=%d calls=%d", status, resp.StatusCode, f.calls)
		}
		if len(f.bodies) != 2 || f.bodies[0] != `{"a":1}` || f.bodies[1] != `{"a":1}` {
			t.Fatalf("%d: body not replayed: %q", status, f.bodies)
		}
		if !f.closed[0].closed.Load() {
			t.Fatalf("%d: discarded response body not closed", status)
		}
		if len(rec.delays) != 1 || rec.delays[0] != 50*time.Millisecond {
			t.Fatalf("%d: delays=%v, want [50ms] (100ms * 0.5 jitter)", status, rec.delays)
		}
	}
}

func TestRetryNotOnOtherStatuses(t *testing.T) {
	for _, status := range []int{400, 401, 404, 409, 500} {
		f := &fakeRT{}
		f.steps = []fakeStep{f.respond(status, nil)}
		rt, _ := newTestRetry(f, 3)
		resp, err := doReq(t, rt, http.MethodGet, "")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if f.calls != 1 {
			t.Fatalf("%d retried: calls=%d", status, f.calls)
		}
	}
}

func TestRetryShouldRetryHeaderAuthoritative(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{
		f.respond(500, map[string]string{"Should-Retry": "true"}), f.respond(201, nil),
	}
	rt, _ := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodPost, "x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 201 || f.calls != 2 {
		t.Fatalf("Should-Retry: true not honored: status=%d calls=%d", resp.StatusCode, f.calls)
	}

	f2 := &fakeRT{}
	f2.steps = []fakeStep{f2.respond(503, map[string]string{"Should-Retry": "False"})}
	rt2, _ := newTestRetry(f2, 3)
	resp, err = doReq(t, rt2, http.MethodPost, "x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 || f2.calls != 1 {
		t.Fatalf("Should-Retry: false not honored: calls=%d", f2.calls)
	}
}

func TestRetryExhaustsAndReturnsLastResponse(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{f.respond(503, nil)}
	rt, rec := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodGet, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 503 || f.calls != 4 {
		t.Fatalf("status=%d calls=%d, want 503 after 4 attempts", resp.StatusCode, f.calls)
	}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if !slices.Equal(rec.delays, want) {
		t.Fatalf("delays=%v want %v", rec.delays, want)
	}
	for i, b := range f.closed[:3] {
		if !b.closed.Load() {
			t.Fatalf("discarded body %d not closed", i)
		}
	}
	if f.closed[3].closed.Load() {
		t.Fatal("returned response body must stay open for the caller")
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{
		f.respond(429, map[string]string{"Retry-After": "2"}), f.respond(200, nil),
	}
	rt, rec := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodGet, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(rec.delays) != 1 || rec.delays[0] != 2*time.Second {
		t.Fatalf("delays=%v, want [2s]", rec.delays)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	maxSecs := int64(maxRetryAfter / time.Second)
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"0", 0, true},
		{" 7 ", 7 * time.Second, true},
		{"-1", 0, false},
		{"1.5", 0, false},
		{"abc", 0, false},
		{strconv.FormatInt(maxSecs, 10), maxRetryAfter, true},
		{strconv.FormatInt(maxSecs+1, 10), 0, false},
		// Values that fit in int64 seconds but would overflow int64 nanoseconds
		// if multiplied by time.Second before range-checking.
		{"10000000000", 0, false},
		{"9223372036854775807", 0, false},
		{"99999999999999999999", 0, false},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
	}
	for _, c := range cases {
		got, ok := ParseRetryAfter(c.in, now)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRetryAfter(%q)=(%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRetryBackoffCapAndJitter(t *testing.T) {
	rt := &RetryTransport{random: func() float64 { return 0.999999 }}
	if d := rt.backoff(20); d > retryBackoffMax || d < retryBackoffMax*9/10 {
		t.Fatalf("backoff(20)=%v, want ~cap %v", d, retryBackoffMax)
	}
	rt.random = func() float64 { return 0 }
	if d := rt.backoff(0); d != 50*time.Millisecond {
		t.Fatalf("backoff(0)=%v", d)
	}
	if d := rt.backoff(1000); d != retryBackoffMax/2 {
		t.Fatalf("backoff overflow guard: %v", d)
	}
}

func TestRetrySkipsWhenWaitExceedsDeadline(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{
		f.respond(503, map[string]string{"Retry-After": "120"}), f.respond(200, nil),
	}
	rt, rec := newTestRetry(f, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://x/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 503 || f.calls != 1 || len(rec.delays) != 0 {
		t.Fatalf("status=%d calls=%d delays=%v; retry should be skipped", resp.StatusCode, f.calls, rec.delays)
	}
	if f.closed[0].closed.Load() {
		t.Fatal("returned body must not be closed")
	}
}

func TestRetryNoDeadlineUsesTotalCap(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{
		f.respond(503, map[string]string{"Retry-After": "61"}), f.respond(200, nil),
	}
	rt, _ := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodGet, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if f.calls != 1 {
		t.Fatalf("a 61s wait without deadline should not be attempted, calls=%d", f.calls)
	}
}

func TestRetryStopsOnContextCancelDuringWait(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{f.respond(503, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &RetryTransport{Base: f, MaxRetries: 3, random: func() float64 { return 0 },
		sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://x/", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatal("expected nil response on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
	if !f.closed[0].closed.Load() {
		t.Fatal("drained body not closed on cancellation")
	}
}

func TestRetryRealWaitAbortsOnCancel(t *testing.T) {
	rt := &RetryTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := rt.wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait did not abort promptly")
	}
	// Non-positive delay must still observe a canceled context first.
	if err := rt.wait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait(canceled, 0)=%v, want context.Canceled", err)
	}
	if err := rt.wait(ctx, -time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait(canceled, -1s)=%v, want context.Canceled", err)
	}
	if err := rt.wait(context.Background(), 0); err != nil {
		t.Fatalf("wait(bg, 0)=%v", err)
	}
	if err := rt.wait(context.Background(), -time.Millisecond); err != nil {
		t.Fatalf("wait(bg, -1ms)=%v", err)
	}
	if err := rt.wait(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestRetryDoesNotRetryAfterContextDone(t *testing.T) {
	f := &fakeRT{}
	ctx, cancel := context.WithCancel(context.Background())
	f.steps = []fakeStep{{fn: func(req *http.Request) (*http.Response, error) {
		cancel()
		return f.makeResp(req, 503, nil), nil
	}}}
	rt, _ := newTestRetry(f, 3)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://x/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
}

func TestRetryNonReplayableBodySingleAttempt(t *testing.T) {
	f := &fakeRT{}
	f.steps = []fakeStep{f.respond(503, nil), f.respond(200, nil)}
	rt, _ := newTestRetry(f, 3)
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte("stream")); _ = pw.Close() }()
	req, _ := http.NewRequest(http.MethodPut, "https://x/", pr)
	if req.GetBody != nil {
		t.Fatal("pipe body unexpectedly replayable")
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 || f.calls != 1 {
		t.Fatalf("streaming body retried: calls=%d", f.calls)
	}
}

func TestRetryConnectionErrors(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	f := &fakeRT{}
	f.steps = []fakeStep{
		{err: dialErr},
		f.respond(200, nil),
	}
	rt, rec := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodPost, "b")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if f.calls != 2 || len(rec.delays) != 1 {
		t.Fatalf("dial error not retried: calls=%d", f.calls)
	}

	resetErr := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	f2 := &fakeRT{}
	f2.steps = []fakeStep{{err: resetErr}}
	rt2, _ := newTestRetry(f2, 3)
	resp2, err := doReq(t, rt2, http.MethodPost, "b")
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("err=%v", err)
	}
	if f2.calls != 1 {
		t.Fatalf("reset retried: calls=%d", f2.calls)
	}

	f3 := &fakeRT{}
	f3.steps = []fakeStep{{err: &net.DNSError{Err: "no such host", Name: "x"}}}
	rt3, _ := newTestRetry(f3, 2)
	resp3, err := doReq(t, rt3, http.MethodGet, "")
	if resp3 != nil {
		_ = resp3.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if f3.calls != 3 {
		t.Fatalf("calls=%d want 3", f3.calls)
	}
}

func TestRetryClosesBodyWhenRoundTripReturnsRespAndErr(t *testing.T) {
	// Non-retryable error returned alongside a non-nil response.
	f := &fakeRT{}
	f.steps = []fakeStep{{status: 500, err: errors.New("broken transport")}}
	rt, _ := newTestRetry(f, 3)
	resp, err := doReq(t, rt, http.MethodGet, "")
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatal("expected nil resp when err != nil")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if len(f.closed) != 1 || !f.closed[0].closed.Load() {
		t.Fatal("non-nil response body returned with error was not closed")
	}

	// Retryable error that exhausts retries while returning non-nil responses.
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	f2 := &fakeRT{}
	f2.steps = []fakeStep{{status: 503, err: dialErr}}
	rt2, _ := newTestRetry(f2, 1)
	resp2, err := doReq(t, rt2, http.MethodGet, "")
	if resp2 != nil {
		_ = resp2.Body.Close()
		t.Fatal("expected nil resp when err != nil")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("err=%v", err)
	}
	if len(f2.closed) != 2 || !f2.closed[0].closed.Load() || !f2.closed[1].closed.Load() {
		t.Fatalf("expected all returned bodies closed, got %d", len(f2.closed))
	}
}

func TestIsConnectionError(t *testing.T) {
	if isConnectionError(nil) || isConnectionError(context.Canceled) || isConnectionError(errors.New("x")) {
		t.Fatal("false positives")
	}
	if !isConnectionError(syscall.EHOSTUNREACH) {
		t.Fatal("EHOSTUNREACH should be a connection error")
	}
}

func TestNewRetryTransportDisabledAndUnwrapsStacked(t *testing.T) {
	base := &fakeRT{}
	if got := NewRetryTransport(base, 0); got != base {
		t.Fatal("0 retries should return base unchanged")
	}
	if got := NewRetryTransport(nil, -1); got != http.DefaultTransport {
		t.Fatal("nil base should default to http.DefaultTransport")
	}
	wrapped, ok := NewRetryTransport(base, 2).(*RetryTransport)
	if !ok || wrapped.Base != base || wrapped.MaxRetries != 2 {
		t.Fatalf("unexpected wrapped transport: %#v", wrapped)
	}

	// Passing an already-wrapped *RetryTransport must unwrap instead of stacking.
	rewrapped, ok := NewRetryTransport(wrapped, 3).(*RetryTransport)
	if !ok || rewrapped.Base != base || rewrapped.MaxRetries != 3 {
		t.Fatalf("stacked *RetryTransport not unwrapped: %#v", rewrapped)
	}

	// Multi-level manually stacked *RetryTransport unwraps completely.
	nested := &RetryTransport{Base: &RetryTransport{Base: base, MaxRetries: 2}, MaxRetries: 2}
	flat, ok := NewRetryTransport(nested, 1).(*RetryTransport)
	if !ok || flat.Base != base || flat.MaxRetries != 1 {
		t.Fatalf("nested *RetryTransport not unwrapped: %#v", flat)
	}
	if got := NewRetryTransport(nested, 0); got != base {
		t.Fatalf("0 retries on nested *RetryTransport should return base, got %T", got)
	}

	// Typed-nil and nil-base *RetryTransport fall back to http.DefaultTransport.
	var typedNil *RetryTransport
	if got := NewRetryTransport(typedNil, 0); got != http.DefaultTransport {
		t.Fatalf("typed nil base should fall back to http.DefaultTransport, got %T", got)
	}
	if got, ok := NewRetryTransport(&RetryTransport{Base: nil, MaxRetries: 2}, 1).(*RetryTransport); !ok || got.Base != http.DefaultTransport {
		t.Fatalf("nil-base *RetryTransport should unwrap to http.DefaultTransport, got %#v", got)
	}
}

func TestRetryTransportEndToEnd(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"templateID":"base"}` {
			t.Errorf("body=%q", b)
		}
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":503,"message":"busy"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	hc := &http.Client{Transport: NewRetryTransport(server.Client().Transport, 3), Timeout: 10 * time.Second}
	resp, err := hc.Post(server.URL+"/sandboxes", "application/json", strings.NewReader(`{"templateID":"base"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || calls.Load() != 3 {
		t.Fatalf("status=%d calls=%d", resp.StatusCode, calls.Load())
	}
}
