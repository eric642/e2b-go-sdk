package transport

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultRetries = 3

	retryBackoffBase            = 100 * time.Millisecond
	retryBackoffMax             = 10 * time.Second
	retryJitterMin              = 0.5
	maxRetryWaitWithoutDeadline = 60 * time.Second
	maxRetryAfter               = 2_147_483 * time.Second
	// drainLimit caps how much of a discarded response body is drained so
	// the underlying connection can be reused.
	drainLimit = 64 << 10

	HeaderShouldRetry = "Should-Retry"
	HeaderRetryAfter  = "Retry-After"
)

// RetryTransport is an http.RoundTripper for control-plane requests that
// retries:
//
//   - responses carrying `Should-Retry: true` (the header, when present, is
//     authoritative: `Should-Retry: false` is never retried);
//   - otherwise 429, 502, 503 and 504 responses;
//   - failures to establish the connection (dial / DNS / connection refused /
//     host unreachable). Failures after the request may have been written
//     (e.g. connection reset mid-request) are not retried because the server
//     may already have acted on a non-idempotent request — Go's
//     http.Transport already transparently retries the safe cases.
//
// A retry happens only while the request context is live and the body is nil
// or replayable via Request.GetBody (streaming bodies get a single attempt).
// The wait honors Retry-After (delta-seconds or HTTP-date), else jittered
// exponential backoff from 100ms up to 10s; a retry whose wait would cross
// the request deadline (or 60s total when there is none) is skipped and the
// last response returned. Discarded response bodies are drained and closed.
type RetryTransport struct {
	Base       http.RoundTripper
	MaxRetries int

	sleep  func(ctx context.Context, d time.Duration) error
	random func() float64
	now    func() time.Time
}

func NewRetryTransport(base http.RoundTripper, maxRetries int) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if maxRetries <= 0 {
		return base
	}
	return &RetryTransport{Base: base, MaxRetries: maxRetries}
}

func (t *RetryTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *RetryTransport) timeNow() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *RetryTransport) rand() float64 {
	if t.random != nil {
		return t.random()
	}
	return rand.Float64()
}

func (t *RetryTransport) wait(ctx context.Context, d time.Duration) error {
	if t.sleep != nil {
		return t.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.MaxRetries <= 0 || !replayable(req) {
		return t.base().RoundTrip(req)
	}
	ctx := req.Context()
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = t.timeNow().Add(maxRetryWaitWithoutDeadline)
	}

	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 {
			var err error
			if r, err = rewind(req); err != nil {
				return nil, err
			}
		}
		resp, err := t.base().RoundTrip(r)
		if attempt >= t.MaxRetries || ctx.Err() != nil {
			return resp, err
		}

		var delay time.Duration
		if err != nil {
			if !isConnectionError(err) {
				return nil, err
			}
			delay = t.backoff(attempt)
		} else {
			d, ok := t.retryDelay(resp, attempt)
			if !ok {
				return resp, nil
			}
			delay = d
		}
		if !t.timeNow().Add(delay).Before(deadline) {
			return resp, err
		}
		if resp != nil {
			drainAndClose(resp.Body)
		}
		if werr := t.wait(ctx, delay); werr != nil {
			return nil, werr
		}
	}
}

func (t *RetryTransport) retryDelay(resp *http.Response, attempt int) (time.Duration, bool) {
	var retryable bool
	if v, present := headerValue(resp.Header, HeaderShouldRetry); present {
		retryable = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			retryable = true
		}
	}
	if !retryable {
		return 0, false
	}
	if d, ok := ParseRetryAfter(resp.Header.Get(HeaderRetryAfter), t.timeNow()); ok {
		return d, true
	}
	return t.backoff(attempt), true
}

func (t *RetryTransport) backoff(attempt int) time.Duration {
	d := retryBackoffMax
	if attempt < 30 {
		if b := retryBackoffBase << uint(attempt); b < retryBackoffMax {
			d = b
		}
	}
	return time.Duration(float64(d) * (retryJitterMin + t.rand()*(1-retryJitterMin)))
}

func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if isDigits(v) {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || time.Duration(secs)*time.Second > maxRetryAfter {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := max(when.Sub(now), 0)
	if d > maxRetryAfter {
		return 0, false
	}
	return d, true
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func headerValue(h http.Header, key string) (string, bool) {
	vs, ok := h[http.CanonicalHeaderKey(key)]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func rewind(req *http.Request) (*http.Request, error) {
	r := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return r, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	r.Body = body
	return r, nil
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, body, drainLimit)
	_ = body.Close()
}

func isConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return true
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH,
		syscall.ENETDOWN, syscall.EHOSTDOWN,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
