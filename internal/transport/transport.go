package transport

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"strconv"

	"connectrpc.com/connect"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/envd/filesystem/filesystemconnect"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
	"github.com/eric642/e2b-go-sdk/internal/envdapi"
	"github.com/eric642/e2b-go-sdk/internal/volumeapi"
)

// Wire constants shared by every envd request. They mirror the headers the
// upstream JS/Python SDKs attach so self-hosted proxies (e.g. E2B Embed on
// localhost:3002 or a sandbox.<domain> gateway) can route envd traffic.
const (
	EnvdPort = 49983

	HeaderAccessToken   = "X-Access-Token"
	HeaderAuthorization = "Authorization"
	HeaderSandboxID     = "E2b-Sandbox-Id"
	HeaderSandboxPort   = "E2b-Sandbox-Port"
	HeaderAPIKey        = "X-API-Key"

	// HeaderKeepalivePing asks envd to emit keepalive messages on server
	// streams so idle proxies do not cut long-running streams.
	HeaderKeepalivePing = "Keepalive-Ping-Interval"
	// KeepalivePingIntervalSec is the keepalive interval requested on every
	// streaming envd RPC (matches upstream KEEPALIVE_PING_INTERVAL_SEC).
	KeepalivePingIntervalSec = 50
)

func basicUserAuth(user string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":"))
}

type Auth struct {
	APIKey      string
	AccessToken string
	Headers     map[string]string
}

func (a Auth) AsRequestEditor() apiclient.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if a.APIKey != "" {
			req.Header.Set(HeaderAPIKey, a.APIKey)
		} else if a.AccessToken != "" {
			req.Header.Set(HeaderAuthorization, "Bearer "+a.AccessToken)
		}
		for k, v := range a.Headers {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}
		return nil
	}
}

func NewAPIClient(baseURL string, hc *http.Client, auth Auth) (*apiclient.Client, error) {
	return apiclient.NewClient(
		baseURL,
		apiclient.WithHTTPClient(hc),
		apiclient.WithRequestEditorFn(auth.AsRequestEditor()),
	)
}

type EnvdAuth struct {
	Token     string
	User      string
	SandboxID string
	Headers   map[string]string
}

type envdUserKey struct{}

func WithEnvdUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, envdUserKey{}, user)
}

func envdUserFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	u, ok := ctx.Value(envdUserKey{}).(string)
	return u, ok
}

func (a EnvdAuth) userFor(ctx context.Context) string {
	if u, ok := envdUserFromContext(ctx); ok {
		return u
	}
	return a.User
}

func ApplyEnvdHeaders(req *http.Request, auth EnvdAuth, resolvedUser string) {
	applyEnvdHeader(req.Header, auth, resolvedUser)
}

// applyEnvdHeader mirrors the upstream SDKs: the Basic Authorization header is
// governed solely by the resolved user. Extra headers must not be able to
// inject their own Authorization (that would defeat default-user inference on
// envd >= 0.4.0 and could forward a control-plane bearer to envd), nor
// overwrite the access token or routing headers.
func applyEnvdHeader(h http.Header, a EnvdAuth, user string) {
	if a.Token != "" {
		h.Set(HeaderAccessToken, a.Token)
	}
	if a.SandboxID != "" {
		h.Set(HeaderSandboxID, a.SandboxID)
		h.Set(HeaderSandboxPort, strconv.Itoa(EnvdPort))
	}
	if user != "" {
		h.Set(HeaderAuthorization, "Basic "+basicUserAuth(user))
	} else {
		h.Del(HeaderAuthorization)
	}
	for k, v := range a.Headers {
		ck := http.CanonicalHeaderKey(k)
		if ck == HeaderAuthorization {
			continue
		}
		// The SDK User-Agent replaces transport defaults such as
		// "connect-go/x.y"; other extras never overwrite a set header.
		if ck == "User-Agent" || h.Get(ck) == "" {
			h.Set(ck, v)
		}
	}
}

type envdHeaderInterceptor struct{ a EnvdAuth }

func (i envdHeaderInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		applyEnvdHeader(req.Header(), i.a, i.a.userFor(ctx))
		return next(ctx, req)
	}
}

func (i envdHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		h := conn.RequestHeader()
		applyEnvdHeader(h, i.a, i.a.userFor(ctx))
		h.Set(HeaderKeepalivePing, strconv.Itoa(KeepalivePingIntervalSec))
		return conn
	}
}

func (i envdHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type EnvdClients struct {
	Process    processconnect.ProcessClient
	Filesystem filesystemconnect.FilesystemClient
	API        *envdapi.Client
	Auth       EnvdAuth
}

func NewEnvdClients(baseURL string, hc *http.Client, auth EnvdAuth) (*EnvdClients, error) {
	auth.Headers = maps.Clone(auth.Headers)
	interceptor := envdHeaderInterceptor{a: auth}
	// Request JSON codec to match Python/JS (envd is a Connect-RPC server
	// that speaks both proto and JSON; JSON gives us cleaner debugging).
	opts := []connect.ClientOption{
		connect.WithInterceptors(interceptor),
		connect.WithProtoJSON(),
	}
	proc := processconnect.NewProcessClient(hc, baseURL, opts...)
	fs := filesystemconnect.NewFilesystemClient(hc, baseURL, opts...)

	envdEditor := func(ctx context.Context, req *http.Request) error {
		applyEnvdHeader(req.Header, auth, auth.userFor(ctx))
		return nil
	}
	apiCli, err := envdapi.NewClient(baseURL,
		envdapi.WithHTTPClient(hc),
		envdapi.WithRequestEditorFn(envdEditor),
	)
	if err != nil {
		return nil, err
	}
	return &EnvdClients{Process: proc, Filesystem: fs, API: apiCli, Auth: auth}, nil
}

func NewVolumeAPIClient(baseURL string, hc *http.Client, token string, extraHeaders map[string]string) (*volumeapi.Client, error) {
	var hdrs map[string]string
	if extraHeaders != nil {
		hdrs = make(map[string]string, len(extraHeaders))
		for k, v := range extraHeaders {
			if http.CanonicalHeaderKey(k) == HeaderAuthorization {
				continue
			}
			hdrs[k] = v
		}
	}
	editor := func(_ context.Context, req *http.Request) error {
		if token != "" {
			req.Header.Set(HeaderAuthorization, "Bearer "+token)
		} else {
			req.Header.Del(HeaderAuthorization)
		}
		for k, v := range hdrs {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}
		return nil
	}
	return volumeapi.NewClient(baseURL,
		volumeapi.WithHTTPClient(hc),
		volumeapi.WithRequestEditorFn(editor),
	)
}
