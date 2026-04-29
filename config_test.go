package e2b

import (
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"E2B_DOMAIN", "E2B_API_URL", "E2B_API_KEY", "E2B_ACCESS_TOKEN", "E2B_DEBUG", "E2B_SANDBOX_URL"} {
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
