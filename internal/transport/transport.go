// Package transport wires the REST (oapi-codegen) and Connect-RPC clients
// used by the public e2b package. Everything here is internal: the exported
// helpers are thin constructors consumed by sandbox.go, volume/, etc.
package transport

import (
	"context"
	"encoding/base64"
	"net/http"

	"connectrpc.com/connect"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/envd/filesystem/filesystemconnect"
	"github.com/eric642/e2b-go-sdk/internal/envd/process/processconnect"
	"github.com/eric642/e2b-go-sdk/internal/envdapi"
	"github.com/eric642/e2b-go-sdk/internal/volumeapi"
)

func basicUserAuth(user string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":"))
}

// Auth describes how to authenticate against the control-plane REST API.
// Exactly one of APIKey or AccessToken should be set (APIKey wins if both).
type Auth struct {
	APIKey      string
	AccessToken string
	Headers     map[string]string
}

// AsRequestEditor builds a RequestEditorFn that injects the auth header and
// any extra headers the caller supplied.
func (a Auth) AsRequestEditor() apiclient.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if a.APIKey != "" {
			req.Header.Set("X-API-Key", a.APIKey)
		}
		if a.AccessToken != "" {
			req.Header.Set("Authorization", "Bearer "+a.AccessToken)
		}
		for k, v := range a.Headers {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}
		return nil
	}
}

// NewAPIClient builds the control-plane REST client (https://api.<domain>).
func NewAPIClient(baseURL string, hc *http.Client, auth Auth) (*apiclient.Client, error) {
	return apiclient.NewClient(
		baseURL,
		apiclient.WithHTTPClient(hc),
		apiclient.WithRequestEditorFn(auth.AsRequestEditor()),
	)
}

// EnvdAuth is the envd (in-sandbox) access token. It is returned by the
// control plane when the sandbox is created. For Connect-RPC the token is
// carried as HTTP Basic auth with the token placed in the *username* slot.
// For plain HTTP endpoints (/files, /metrics, /envs) the token goes in an
// X-Access-Token header.
type EnvdAuth struct {
	Token    string
	User     string
	Headers  map[string]string
}

// envdStreamHeaderInterceptor sets the envd auth/header set on every unary
// and streaming call initiated by the client.
type envdStreamHeaderInterceptor struct{ a EnvdAuth }

func (i envdStreamHeaderInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		i.a.applyHeader(req.Header())
		return next(ctx, req)
	}
}

func (i envdStreamHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		i.a.applyHeader(conn.RequestHeader())
		return conn
	}
}

func (i envdStreamHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (a EnvdAuth) applyHeader(h http.Header) {
	if a.Token != "" {
		h.Set("X-Access-Token", a.Token)
	}
	user := a.User
	if user == "" {
		user = "user"
	}
	h.Set("Authorization", "Basic "+basicUserAuth(user))
	for k, v := range a.Headers {
		if h.Get(k) == "" {
			h.Set(k, v)
		}
	}
}

// NewEnvdClients builds the two Connect-RPC clients used by the sandbox
// (Process + Filesystem) plus the plain-HTTP client for /files, /metrics,
// /envs. baseURL is the envd root URL (https://49983-<id>.<domain>).
type EnvdClients struct {
	Process    processconnect.ProcessClient
	Filesystem filesystemconnect.FilesystemClient
	API        *envdapi.Client
}

// NewEnvdClients constructs Connect and REST clients for envd traffic.
func NewEnvdClients(baseURL string, hc *http.Client, auth EnvdAuth) (*EnvdClients, error) {
	interceptor := envdStreamHeaderInterceptor{a: auth}
	// Request JSON codec to match Python/JS (envd is a Connect-RPC server
	// that speaks both proto and JSON; JSON gives us cleaner debugging).
	opts := []connect.ClientOption{
		connect.WithInterceptors(interceptor),
	}
	proc := processconnect.NewProcessClient(hc, baseURL, opts...)
	fs := filesystemconnect.NewFilesystemClient(hc, baseURL, opts...)

	envdEditor := func(_ context.Context, req *http.Request) error {
		if auth.Token != "" {
			req.Header.Set("X-Access-Token", auth.Token)
		}
		user := auth.User
		if user == "" {
			user = "user"
		}
		req.Header.Set("Authorization", "Basic "+basicUserAuth(user))
		for k, v := range auth.Headers {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}
		return nil
	}
	apiCli, err := envdapi.NewClient(baseURL,
		envdapi.WithHTTPClient(hc),
		envdapi.WithRequestEditorFn(envdEditor),
	)
	if err != nil {
		return nil, err
	}
	return &EnvdClients{Process: proc, Filesystem: fs, API: apiCli}, nil
}

// NewVolumeAPIClient builds the volume-content REST client using a JWT
// bearer token scoped to a single volume.
func NewVolumeAPIClient(baseURL string, hc *http.Client, token string, extraHeaders map[string]string) (*volumeapi.Client, error) {
	editor := func(_ context.Context, req *http.Request) error {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range extraHeaders {
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
