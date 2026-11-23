package e2b

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eric642/e2b-go-sdk/internal/transport"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"E2B_DOMAIN", "E2B_API_URL", "E2B_API_KEY", "E2B_ACCESS_TOKEN", "E2B_DEBUG", "E2B_SANDBOX_URL", "E2B_VOLUME_API_URL", "E2B_HTTP_VERSION"} {
		old := os.Getenv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
		t.Cleanup(func() {
			if old != "" {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func TestConfigResolveDefaults(t *testing.T) {
	clearEnv(t)

	cfg := Config{}.resolve()
	if cfg.Domain != DefaultDomain {
		t.Fatalf("want domain %s, got %s", DefaultDomain, cfg.Domain)
	}
	if cfg.APIURL != "https://api."+DefaultDomain {
		t.Fatalf("want default API URL, got %s", cfg.APIURL)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Fatalf("want default timeout, got %s", cfg.RequestTimeout)
	}
	if cfg.Headers["User-Agent"] == "" {
		t.Fatal("User-Agent should be populated")
	}
}

func TestConfigResolveDebug(t *testing.T) {
	clearEnv(t)
	cfg := Config{Debug: true}.resolve()
	if cfg.APIURL != "http://localhost:3000" {
		t.Fatalf("want debug URL, got %s", cfg.APIURL)
	}
	if got := cfg.sandboxURL("sbx-123", "custom.io"); got != "http://localhost:49983" {
		t.Fatalf("want localhost debug sandbox URL, got %s", got)
	}
}

func TestSandboxHostNonDebug(t *testing.T) {
	clearEnv(t)
	cfg := Config{Domain: "example.com"}.resolve()
	got := cfg.sandboxHost("sbx-abc", "example.com", 3000)
	want := "3000-sbx-abc.example.com"
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestConfigEnvFallbacks(t *testing.T) {
	clearEnv(t)
	t.Setenv("E2B_API_KEY", "k-from-env")
	t.Setenv("E2B_ACCESS_TOKEN", "at-from-env")
	t.Setenv("E2B_DOMAIN", "env.example.com")
	t.Setenv("E2B_SANDBOX_URL", "https://sbx.env.example.com")
	t.Setenv("E2B_API_URL", "https://api.env.example.com")

	cfg := Config{}.resolve()
	if cfg.APIKey != "k-from-env" {
		t.Fatalf("APIKey=%q", cfg.APIKey)
	}
	if cfg.AccessToken != "at-from-env" {
		t.Fatalf("AccessToken=%q", cfg.AccessToken)
	}
	if cfg.Domain != "env.example.com" {
		t.Fatalf("Domain=%q", cfg.Domain)
	}
	if cfg.SandboxURL != "https://sbx.env.example.com" {
		t.Fatalf("SandboxURL=%q", cfg.SandboxURL)
	}
	if cfg.APIURL != "https://api.env.example.com" {
		t.Fatalf("APIURL=%q", cfg.APIURL)
	}
}

func TestConfigExplicitFieldsWin(t *testing.T) {
	clearEnv(t)
	t.Setenv("E2B_API_KEY", "env-key")
	t.Setenv("E2B_DOMAIN", "env.example.com")
	t.Setenv("E2B_API_URL", "https://api.env.example.com")

	cfg := Config{
		APIKey: "explicit-key",
		Domain: "explicit.com",
		APIURL: "https://explicit.example.com",
	}.resolve()
	if cfg.APIKey != "explicit-key" {
		t.Fatalf("explicit APIKey lost: %q", cfg.APIKey)
	}
	if cfg.Domain != "explicit.com" {
		t.Fatalf("explicit Domain lost: %q", cfg.Domain)
	}
	if cfg.APIURL != "https://explicit.example.com" {
		t.Fatalf("explicit APIURL lost: %q", cfg.APIURL)
	}
}

func TestConfigDebugFromEnv(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "True"} {
		t.Run(v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("E2B_DEBUG", v)
			cfg := Config{}.resolve()
			if !cfg.Debug {
				t.Fatalf("Debug should be true for E2B_DEBUG=%q", v)
			}
		})
	}
	t.Run("false", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("E2B_DEBUG", "false")
		cfg := Config{}.resolve()
		if cfg.Debug {
			t.Fatal("Debug should be false for E2B_DEBUG=false")
		}
	})
}

func TestConfigHTTPClientOverride(t *testing.T) {
	clearEnv(t)
	custom := &http.Client{Timeout: 999 * time.Millisecond}
	cfg := Config{HTTPClient: custom}.resolve()
	if cfg.httpClient() != custom {
		t.Fatal("httpClient() should return the caller-supplied *http.Client verbatim")
	}
}

func TestConfigHTTPClientDefault(t *testing.T) {
	clearEnv(t)
	cfg := Config{RequestTimeout: 7 * time.Second}.resolve()
	hc := cfg.httpClient()
	if hc == nil {
		t.Fatal("default httpClient should not be nil")
	}
	if hc.Timeout != 7*time.Second {
		t.Fatalf("Timeout propagation broken: got %s", hc.Timeout)
	}
}

func TestConfigRequestTimeoutDisabled(t *testing.T) {
	clearEnv(t)
	cfg := Config{RequestTimeoutDisabled: true}.resolve()
	if cfg.RequestTimeout != 0 {
		t.Fatalf("RequestTimeout should stay 0 when disabled, got %s", cfg.RequestTimeout)
	}
}

func TestConfigUserAgentContainsPlatformAndVersion(t *testing.T) {
	clearEnv(t)
	cfg := Config{}.resolve()
	ua := cfg.Headers["User-Agent"]
	if !strings.Contains(ua, "e2b-go-sdk/") {
		t.Fatalf("User-Agent missing SDK name: %q", ua)
	}
	if !strings.Contains(ua, SDKVersion) {
		t.Fatalf("User-Agent missing SDKVersion %q: %q", SDKVersion, ua)
	}
	if !strings.Contains(ua, runtime.GOOS) {
		t.Fatalf("User-Agent missing GOOS %q: %q", runtime.GOOS, ua)
	}
}

func TestConfigUserAgentNotOverriddenIfCallerSet(t *testing.T) {
	clearEnv(t)
	cfg := Config{Headers: map[string]string{"User-Agent": "caller-app/1.0"}}.resolve()
	if cfg.Headers["User-Agent"] != "caller-app/1.0" {
		t.Fatalf("caller User-Agent overwritten: %q", cfg.Headers["User-Agent"])
	}
}

func TestSandboxURLExplicitWins(t *testing.T) {
	clearEnv(t)
	cfg := Config{SandboxURL: "https://tunnel.ngrok.dev"}.resolve()
	if got := cfg.sandboxURL("sbx-ignored", "ignored.com"); got != "https://tunnel.ngrok.dev" {
		t.Fatalf("explicit SandboxURL ignored: %s", got)
	}
}

func TestConfigResolveDoesNotAliasOrMutateCallerMaps(t *testing.T) {
	clearEnv(t)
	hdrs := map[string]string{"X-Custom": "1"}
	extra := map[string]string{"X-Sbx": "2"}
	retries := 5
	in := Config{Headers: hdrs, ExtraSandboxHeaders: extra, Retries: &retries}
	cfg := in.resolve()

	if _, ok := hdrs["User-Agent"]; ok {
		t.Fatal("resolve mutated the caller's Headers map (User-Agent injected)")
	}
	cfg.Headers["X-Custom"] = "tampered"
	cfg.ExtraSandboxHeaders["X-Sbx"] = "tampered"
	*cfg.Retries = 0
	if hdrs["X-Custom"] != "1" || extra["X-Sbx"] != "2" || retries != 5 {
		t.Fatal("resolved Config aliases caller-owned maps/pointers")
	}
	if in.Resolve().Headers["X-Custom"] != "1" {
		t.Fatal("Resolve should be repeatable on the original Config")
	}
}

func TestConfigResolveConcurrentSafe(t *testing.T) {
	clearEnv(t)
	shared := Config{Headers: map[string]string{"X-A": "1"}, ExtraSandboxHeaders: map[string]string{"X-B": "2"}}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			c := shared.resolve()
			c.Headers["X-Mut"] = "x"
			c.ExtraSandboxHeaders["X-Mut"] = "y"
		})
	}
	wg.Wait()
	if len(shared.Headers) != 1 || len(shared.ExtraSandboxHeaders) != 1 {
		t.Fatal("concurrent resolve mutated shared maps")
	}
}

func TestConfigNewFieldsFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("E2B_VOLUME_API_URL", "https://vol.example.com")
	t.Setenv("E2B_HTTP_VERSION", "1.1")
	cfg := Config{}.resolve()
	if cfg.VolumeAPIURL != "https://vol.example.com" {
		t.Fatalf("VolumeAPIURL=%q", cfg.VolumeAPIURL)
	}
	if cfg.HTTPVersion != HTTPVersion1 {
		t.Fatalf("HTTPVersion=%q", cfg.HTTPVersion)
	}

	explicit := Config{VolumeAPIURL: "https://explicit", HTTPVersion: "2"}.resolve()
	if explicit.VolumeAPIURL != "https://explicit" || explicit.HTTPVersion != "2" {
		t.Fatalf("explicit fields lost: %+v", explicit)
	}
}

func TestConfigVolumeAPIURLDefaults(t *testing.T) {
	clearEnv(t)
	if got := (Config{}).resolve().VolumeAPIURL; got != "" {
		t.Fatalf("non-debug VolumeAPIURL should be derived per volume (empty), got %q", got)
	}
	if got := (Config{Debug: true}).resolve().VolumeAPIURL; got != "http://localhost:8080" {
		t.Fatalf("debug VolumeAPIURL=%q", got)
	}
}

func TestConfigRetries(t *testing.T) {
	zero, neg, five := 0, -2, 5
	cases := []struct {
		in   *int
		want int
	}{{nil, DefaultRetries}, {&zero, 0}, {&neg, 0}, {&five, 5}}
	for _, c := range cases {
		if got := (Config{Retries: c.in}).retries(); got != c.want {
			t.Errorf("retries(%v)=%d want %d", c.in, got, c.want)
		}
	}
	if DefaultRetries != 3 {
		t.Fatalf("DefaultRetries=%d, want 3", DefaultRetries)
	}
}

func TestControlPlaneHTTPClientRetries(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{RequestTimeout: 5 * time.Second}.resolve()
	resp, err := cfg.ControlPlaneHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d", resp.StatusCode, calls.Load())
	}

	calls.Store(0)
	zero := 0
	cfg0 := Config{Retries: &zero}.resolve()
	resp, err = cfg0.ControlPlaneHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("Retries=0: status=%d calls=%d", resp.StatusCode, calls.Load())
	}

	calls.Store(0)
	resp, err = cfg.DataPlaneHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("data plane retried: calls=%d", calls.Load())
	}
}

func TestControlPlaneHTTPClientWrapsCustomClient(t *testing.T) {
	clearEnv(t)
	custom := &http.Client{Timeout: 3 * time.Second}
	cfg := Config{HTTPClient: custom}.resolve()
	cp := cfg.ControlPlaneHTTPClient()
	if cp == custom {
		t.Fatal("control-plane client must not mutate/return the caller's client")
	}
	if cp.Timeout != 3*time.Second {
		t.Fatalf("Timeout not preserved: %s", cp.Timeout)
	}
	if custom.Transport != nil {
		t.Fatal("caller's client Transport mutated")
	}
	if cfg.DataPlaneHTTPClient() != custom || cfg.ResolvedHTTPClient() != custom {
		t.Fatal("data-plane / resolved client should be the caller's client verbatim")
	}
}

func TestHTTPClientsTimeouts(t *testing.T) {
	clearEnv(t)
	cfg := Config{RequestTimeout: 9 * time.Second}.resolve()
	if got := cfg.ControlPlaneHTTPClient().Timeout; got != 9*time.Second {
		t.Fatalf("control-plane Timeout=%s", got)
	}
	if got := cfg.DataPlaneHTTPClient().Timeout; got != 0 {
		t.Fatalf("data-plane client must not set a client-wide Timeout (streams), got %s", got)
	}
}

func TestBaseTransportSharedAndConfigured(t *testing.T) {
	clearEnv(t)
	a := Config{}.resolve().baseTransport()
	b := Config{}.resolve().baseTransport()
	if a != b {
		t.Fatal("equivalent configs should share one pooled transport")
	}

	h1 := Config{HTTPVersion: "1.1"}.resolve().baseTransport()
	if h1 == a || h1.Protocols == nil || h1.Protocols.HTTP2() || !h1.Protocols.HTTP1() {
		t.Fatalf("HTTPVersion 1.1 should disable HTTP/2: %+v", h1.Protocols)
	}
	h2 := Config{HTTPVersion: "2"}.resolve().baseTransport()
	if h2.Protocols == nil || !h2.Protocols.HTTP2() {
		t.Fatalf("HTTPVersion 2 should enable HTTP/2: %+v", h2.Protocols)
	}

	p := Config{Proxy: "http://proxy.local:3128"}.resolve().baseTransport()
	req, _ := http.NewRequest(http.MethodGet, "https://api.e2b.app/sandboxes", nil)
	u, err := p.Proxy(req)
	if err != nil || u == nil || u.Host != "proxy.local:3128" {
		t.Fatalf("proxy not applied: %v %v", u, err)
	}
}

func TestParseProxyURL(t *testing.T) {
	ok := map[string]string{
		"proxy.local:3128":          "http://proxy.local:3128",
		"127.0.0.1:3128":            "http://127.0.0.1:3128",
		"http://u:p@proxy:8080":     "http://u:p@proxy:8080",
		"https://proxy.example":     "https://proxy.example",
		"socks5://127.0.0.1:1080":   "socks5://127.0.0.1:1080",
		" socks5h://proxy:1080 ":    "socks5h://proxy:1080",
		"HTTP://Proxy.Example:3128": "http://Proxy.Example:3128",
	}
	for in, want := range ok {
		u, err := parseProxyURL(in)
		if err != nil || !strings.EqualFold(u.String(), want) {
			t.Errorf("parseProxyURL(%q)=(%v,%v), want %s", in, u, err, want)
		}
	}
	for _, in := range []string{"ftp://proxy:21", "http://", "://x", "http://[::1"} {
		if _, err := parseProxyURL(in); err == nil {
			t.Errorf("parseProxyURL(%q) should fail", in)
		} else if _, isIA := errors.AsType[*InvalidArgumentError](err); !isIA {
			t.Errorf("parseProxyURL(%q) err %T, want *InvalidArgumentError", in, err)
		}
	}
}

func TestBareHostPortProxyIsApplied(t *testing.T) {
	clearEnv(t)
	for _, p := range []string{"proxy.local:3128", "127.0.0.1:3128"} {
		tr := Config{Proxy: p}.resolve().baseTransport()
		req, _ := http.NewRequest(http.MethodGet, "https://api.e2b.app/sandboxes", nil)
		u, err := tr.Proxy(req)
		if err != nil || u == nil || u.Host != p {
			t.Fatalf("Proxy %q not applied: %v %v", p, u, err)
		}
	}
}

func TestInvalidProxyFailsClosed(t *testing.T) {
	clearEnv(t)
	tr := Config{Proxy: "ftp://proxy:21"}.resolve().baseTransport()
	req, _ := http.NewRequest(http.MethodGet, "https://api.e2b.app/sandboxes", nil)
	if u, err := tr.Proxy(req); err == nil {
		t.Fatalf("invalid proxy must fail closed, got %v", u)
	}
}

func TestNormalizeHTTPVersion(t *testing.T) {
	for in, want := range map[string]string{"": "", "1.1": "1.1", "1": "1.1", "HTTP/1.1": "1.1", "2": "2", "2.0": "2", "h2": "2", "HTTP/2": "2"} {
		if got, err := normalizeHTTPVersion(in); err != nil || got != want {
			t.Errorf("normalizeHTTPVersion(%q)=(%q,%v), want %q", in, got, err, want)
		}
	}
	if _, err := normalizeHTTPVersion("3"); err == nil {
		t.Fatal("HTTPVersion 3 should be rejected")
	}
}

func TestNewClientValidatesTransportOptions(t *testing.T) {
	clearEnv(t)
	for _, cfg := range []Config{
		{APIKey: "k", Proxy: "ftp://proxy:21"},
		{APIKey: "k", Proxy: "http://"},
		{APIKey: "k", HTTPVersion: "3"},
	} {
		_, err := NewClient(cfg)
		if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Errorf("NewClient(%+v) err=%v, want *InvalidArgumentError", cfg, err)
		}
	}
	t.Setenv("E2B_HTTP_VERSION", "bogus")
	if _, err := NewClient(Config{APIKey: "k"}); err == nil {
		t.Error("invalid E2B_HTTP_VERSION should be rejected")
	}
	if _, err := NewClient(Config{APIKey: "k", HTTPVersion: "3", Proxy: "ftp://x", HTTPClient: &http.Client{}}); err != nil {
		t.Errorf("custom HTTPClient should skip transport validation: %v", err)
	}
	t.Setenv("E2B_HTTP_VERSION", "")
	if _, err := NewClient(Config{APIKey: "k", Proxy: "127.0.0.1:3128", HTTPVersion: "1.1"}); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
}

func TestClientConfigDoesNotAliasRetries(t *testing.T) {
	clearEnv(t)
	r := 4
	c, err := NewClient(Config{APIKey: "k", Retries: &r})
	if err != nil {
		t.Fatal(err)
	}
	got := c.Config()
	*got.Retries = 0
	if *c.cfg.Retries != 4 || r != 4 {
		t.Fatalf("Client.Config() aliases Retries: live=%d caller=%d", *c.cfg.Retries, r)
	}
}

func TestEnvdHeadersExcludeControlPlaneHeaders(t *testing.T) {
	clearEnv(t)
	cfg := Config{
		Headers:             map[string]string{"X-Control": "secret", "Authorization": "Bearer cp"},
		ExtraSandboxHeaders: map[string]string{"X-Sbx": "1"},
	}.resolve()
	h := cfg.envdHeaders()
	if _, ok := h["X-Control"]; ok {
		t.Fatal("control-plane Headers leaked to envd")
	}
	if _, ok := h["Authorization"]; ok {
		t.Fatal("control-plane Authorization leaked to envd")
	}
	if h["X-Sbx"] != "1" || h["User-Agent"] == "" {
		t.Fatalf("envd headers missing UA/extras: %v", h)
	}
	custom := Config{Headers: map[string]string{"User-Agent": "app/2"}}.resolve()
	if custom.envdHeaders()["User-Agent"] != "app/2" {
		t.Fatal("caller User-Agent should propagate to envd")
	}
}

func TestConfigHeaderCanonicalization(t *testing.T) {
	clearEnv(t)
	cfg := Config{
		Headers:             map[string]string{"user-agent": "lower-ua/1", "x-custom": "v"},
		ExtraSandboxHeaders: map[string]string{"user-agent": "sbx-ua/2", "x-sbx": "s"},
	}.resolve()
	if len(cfg.Headers) != 2 || cfg.Headers["User-Agent"] != "lower-ua/1" || cfg.Headers["X-Custom"] != "v" {
		t.Fatalf("Headers not canonicalized: %#v", cfg.Headers)
	}
	if _, dup := cfg.Headers["user-agent"]; dup {
		t.Fatalf("duplicate lowercase user-agent in Headers: %#v", cfg.Headers)
	}

	envd := cfg.envdHeaders()
	if len(envd) != 2 || envd["User-Agent"] != "sbx-ua/2" || envd["X-Sbx"] != "s" {
		t.Fatalf("envdHeaders not canonicalized: %#v", envd)
	}
	if _, dup := envd["user-agent"]; dup {
		t.Fatalf("duplicate lowercase user-agent in envdHeaders: %#v", envd)
	}

	// Also verify envdHeaders handles non-canonical keys on an unresolved Config.
	unresolved := Config{
		Headers:             map[string]string{"user-agent": "raw-ua/1"},
		ExtraSandboxHeaders: map[string]string{"x-extra": "e"},
	}
	envd2 := unresolved.envdHeaders()
	if envd2["User-Agent"] != "raw-ua/1" || envd2["X-Extra"] != "e" {
		t.Fatalf("unresolved envdHeaders=%#v", envd2)
	}
}

func TestSandboxURLTrimsTrailingSlash(t *testing.T) {
	clearEnv(t)
	for _, raw := range []string{"https://tunnel.ngrok.dev/", "https://tunnel.ngrok.dev///"} {
		cfg := Config{SandboxURL: raw}.resolve()
		if got := cfg.sandboxURL("sbx-1", ""); got != "https://tunnel.ngrok.dev" {
			t.Fatalf("sandboxURL(%q)=%q, want without trailing slash", raw, got)
		}
	}
}

func TestParseProxyURLRejectsMissingHostname(t *testing.T) {
	for _, in := range []string{"http://:8080", ":8080", "https://:443", "   "} {
		if _, err := parseProxyURL(in); err == nil {
			t.Errorf("parseProxyURL(%q) should fail", in)
		} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Errorf("parseProxyURL(%q) err=%T, want *InvalidArgumentError", in, err)
		}
	}
}

func TestBaseTransportNormalizesKeyAndSkipsCachingInvalidProxy(t *testing.T) {
	clearEnv(t)
	a := Config{Proxy: "proxy.norm.local:3128", HTTPVersion: "1"}.resolve().baseTransport()
	b := Config{Proxy: " http://proxy.norm.local:3128 ", HTTPVersion: "HTTP/1.1"}.resolve().baseTransport()
	c := Config{Proxy: "HTTP://proxy.norm.local:3128", HTTPVersion: "1.1"}.resolve().baseTransport()
	if a != b || a != c {
		t.Fatal("equivalent Proxy / HTTPVersion configs should share one pooled transport")
	}

	transportsMu.Lock()
	before := len(transports)
	transportsMu.Unlock()

	bad := Config{Proxy: "ftp://never-cache.invalid:21"}.resolve().baseTransport()
	req, _ := http.NewRequest(http.MethodGet, "https://api.e2b.app/sandboxes", nil)
	if _, err := bad.Proxy(req); err == nil {
		t.Fatal("invalid proxy must fail closed")
	}

	transportsMu.Lock()
	after := len(transports)
	transportsMu.Unlock()
	if after != before {
		t.Fatalf("invalid proxy transport cached in global pool: before=%d after=%d", before, after)
	}
}

func TestConfigValidateRejectsNegativeRequestTimeout(t *testing.T) {
	clearEnv(t)
	for _, cfg := range []Config{
		{APIKey: "k", RequestTimeout: -time.Second},
		{APIKey: "k", RequestTimeout: -time.Nanosecond, HTTPClient: &http.Client{}},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(%+v) should fail", cfg)
		} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Errorf("Validate(%+v) err=%T, want *InvalidArgumentError", cfg, err)
		}
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("NewClient(%+v) should fail", cfg)
		} else if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
			t.Errorf("NewClient(%+v) err=%T, want *InvalidArgumentError", cfg, err)
		}
	}
}

func TestPassingClientHTTPClientDoesNotStackRetriesOrRetryDataPlane(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	baseHC := *srv.Client()
	baseHC.Timeout = 5 * time.Second
	retries := 2
	orig, err := NewClient(Config{
		APIKey:     "k",
		APIURL:     srv.URL,
		Retries:    &retries,
		HTTPClient: &baseHC,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pass orig.HTTPClient() (which carries a *transport.RetryTransport and Timeout)
	// back into a new Config.
	derived := Config{
		APIKey:     "k",
		APIURL:     srv.URL,
		Retries:    &retries,
		HTTPClient: orig.HTTPClient(),
	}.resolve()

	// Control plane must unwrap and apply retries once (1 + 2 = 3 attempts, not 3*3 = 9).
	cp := derived.ControlPlaneHTTPClient()
	cpRT, ok := cp.Transport.(*transport.RetryTransport)
	if !ok {
		t.Fatalf("ControlPlaneHTTPClient transport=%T, want *transport.RetryTransport", cp.Transport)
	}
	if _, nested := cpRT.Base.(*transport.RetryTransport); nested {
		t.Fatal("ControlPlaneHTTPClient stacked *transport.RetryTransport")
	}
	calls.Store(0)
	resp, err := cp.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Fatalf("control-plane attempts=%d, want 3 (no stacked retries)", got)
	}

	// Data plane must strip *transport.RetryTransport and clear Timeout.
	dp := derived.DataPlaneHTTPClient()
	if dp == derived.HTTPClient {
		t.Fatal("DataPlaneHTTPClient must clone client when unwrapping *RetryTransport")
	}
	if dp.Timeout != 0 {
		t.Fatalf("DataPlaneHTTPClient Timeout=%v, want 0", dp.Timeout)
	}
	if _, stillRetrying := dp.Transport.(*transport.RetryTransport); stillRetrying {
		t.Fatal("DataPlaneHTTPClient kept *transport.RetryTransport")
	}
	calls.Store(0)
	resp, err = dp.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Fatalf("data-plane attempts=%d, want 1 (no retries)", got)
	}
	// Original HTTPClient must not have been mutated.
	if orig.HTTPClient().Timeout == 0 {
		t.Fatal("orig.HTTPClient().Timeout was mutated")
	}
	if _, ok := orig.HTTPClient().Transport.(*transport.RetryTransport); !ok {
		t.Fatal("orig.HTTPClient().Transport was mutated")
	}
}

func TestCeilSecondsInt32(t *testing.T) {
	cases := []struct {
		name    string
		d       time.Duration
		want    int32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"1ns", time.Nanosecond, 1, false},
		{"500ms", 500 * time.Millisecond, 1, false},
		{"1s", time.Second, 1, false},
		{"1s+1ns", time.Second + time.Nanosecond, 2, false},
		{"1500ms", 1500 * time.Millisecond, 2, false},
		{"60s", 60 * time.Second, 60, false},
		{"maxInt32", time.Duration(math.MaxInt32) * time.Second, math.MaxInt32, false},
		{"maxInt32+1ns", time.Duration(math.MaxInt32)*time.Second + time.Nanosecond, 0, true},
		{"maxInt64", time.Duration(math.MaxInt64), 0, true},
		{"neg1ns", -time.Nanosecond, 0, true},
		{"neg1s", -time.Second, 0, true},
		{"minInt64", time.Duration(math.MinInt64), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ceilSecondsInt32("timeout", tc.d)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ceilSecondsInt32(%v)=%d, want error", tc.d, got)
				}
				if _, ok := errors.AsType[*InvalidArgumentError](err); !ok {
					t.Fatalf("ceilSecondsInt32(%v) err=%T, want *InvalidArgumentError", tc.d, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ceilSecondsInt32(%v)=(%d,%v), want (%d,nil)", tc.d, got, err, tc.want)
			}
		})
	}
}
