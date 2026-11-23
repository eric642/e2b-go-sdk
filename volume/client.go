package volume

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// DefaultFileTransferTimeout bounds single volume file downloads and uploads
// when the caller's context carries no deadline (1 hour, matching the official
// JavaScript and Python SDKs).
const DefaultFileTransferTimeout = time.Hour

const maxVolumeErrorBody = 1 << 20

// Client manages persistent volumes on the E2B control plane and constructs
// Volume handles backed by shared pooled HTTP clients.
//
// Control-plane calls (/volumes) use ControlPlaneHTTPClient (retries +
// RequestTimeout), while volume-content calls (/volumecontent/{id}/*) use
// DataPlaneHTTPClient (no client-wide timeout cutoff and no control-plane
// retry wrapper on streaming uploads/downloads).
//
// A *Client is safe for concurrent use.
type Client struct {
	cfg     e2b.Config
	apiCli  *apiclient.Client
	dataCli *http.Client
}

// Info captures persistent volume metadata returned by the control plane.
// Token and Domain are populated by GetInfo and Create (which return
// VolumeAndToken) and empty on List.
type Info struct {
	ID     string
	Name   string
	Token  string
	Domain string
}

// Options configure a single package-level volume call.
type Options struct {
	Config e2b.Config
}

// NewClient resolves cfg (applying E2B_* environment-variable fallbacks and
// defaults) and constructs a shared volume Client. It returns
// *e2b.InvalidArgumentError if cfg specifies an invalid Proxy or HTTPVersion.
func NewClient(cfg e2b.Config) (*Client, error) {
	resolved := cfg.Resolve()
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	auth := transport.Auth{
		APIKey:      resolved.APIKey,
		AccessToken: resolved.AccessToken,
		Headers:     resolved.Headers,
	}
	apiCli, err := transport.NewAPIClient(resolved.APIURL, resolved.ControlPlaneHTTPClient(), auth)
	if err != nil {
		return nil, &e2b.VolumeError{Message: "init api client", Cause: err}
	}
	return &Client{
		cfg:     resolved,
		apiCli:  apiCli,
		dataCli: resolved.DataPlaneHTTPClient(),
	}, nil
}

// Config returns a deep copy of the resolved e2b.Config backing this Client.
func (c *Client) Config() e2b.Config {
	return c.cfg.Clone()
}

// Create provisions a new persistent volume and returns a tokenized Volume
// handle. When the server returns a BYOC cluster domain, content traffic is
// routed to https://api.<domain>; when domain is absent (non-BYOC E2B Cloud
// and self-hosted runtimes), it falls back to the resolved Config.Domain.
//
// Endpoint: POST /volumes
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (c *Client) Create(ctx context.Context, name string) (*Volume, error) {
	body := apiclient.PostVolumesJSONRequestBody{Name: name}
	resp, err := c.apiCli.PostVolumes(ctx, body)
	if err != nil {
		return nil, wrapErr("create volume", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeControlErr("create volume", resp)
	}
	var parsed apiclient.VolumeAndToken
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &e2b.VolumeError{Message: "decode create response", Cause: err}
	}
	return c.newVolume(parsed.VolumeID, parsed.Name, parsed.Token, derefString(parsed.Domain))
}

// Connect looks up an existing volume by ID, mints a fresh per-volume JWT and
// optional BYOC domain from the control plane, and returns a Volume handle.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (c *Client) Connect(ctx context.Context, volumeID string) (*Volume, error) {
	info, err := c.GetInfo(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	return c.newVolume(info.ID, info.Name, info.Token, info.Domain)
}

// ConnectWithToken constructs a Volume handle locally from a pre-minted JWT
// without making a control-plane HTTP round-trip. An optional BYOC domain
// overrides Config.Domain when routing volume-content requests.
func (c *Client) ConnectWithToken(volumeID, token string, domain ...string) (*Volume, error) {
	if err := validateVolumeID("connect volume", volumeID); err != nil {
		return nil, err
	}
	var volDomain string
	if len(domain) > 0 {
		volDomain = domain[0]
	}
	return c.newVolume(volumeID, "", token, volDomain)
}

// GetInfo retrieves volume metadata, a freshly minted content JWT, and the
// optional BYOC cluster domain for volumeID.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (c *Client) GetInfo(ctx context.Context, volumeID string) (*Info, error) {
	return getVolumeInfo(ctx, c.apiCli, volumeID)
}

// List returns all persistent volumes visible to the authenticated team.
//
// Endpoint: GET /volumes
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (c *Client) List(ctx context.Context) ([]Info, error) {
	resp, err := c.apiCli.GetVolumes(ctx)
	if err != nil {
		return nil, wrapErr("list volumes", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeControlErr("list volumes", resp)
	}
	var out []apiclient.Volume
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &e2b.VolumeError{Message: "decode list response", Cause: err}
	}
	info := make([]Info, 0, len(out))
	for _, v := range out {
		info = append(info, Info{ID: v.VolumeID, Name: v.Name})
	}
	return info, nil
}

// Destroy permanently deletes volumeID. It returns (false, nil) when the
// volume does not exist (HTTP 404).
//
// Endpoint: DELETE /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func (c *Client) Destroy(ctx context.Context, volumeID string) (bool, error) {
	return destroyVolume(ctx, c.apiCli, volumeID)
}

func getVolumeInfo(ctx context.Context, apiCli *apiclient.Client, volumeID string) (*Info, error) {
	if err := validateVolumeID("get volume", volumeID); err != nil {
		return nil, err
	}
	resp, err := apiCli.GetVolumesVolumeID(ctx, volumeID)
	if err != nil {
		return nil, wrapErr("get volume", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, volumeControlErr("get volume", resp)
	}
	var parsed apiclient.VolumeAndToken
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &e2b.VolumeError{Message: "decode volume response", Cause: err}
	}
	return &Info{
		ID:     parsed.VolumeID,
		Name:   parsed.Name,
		Token:  parsed.Token,
		Domain: derefString(parsed.Domain),
	}, nil
}

func destroyVolume(ctx context.Context, apiCli *apiclient.Client, volumeID string) (bool, error) {
	if err := validateVolumeID("destroy volume", volumeID); err != nil {
		return false, err
	}
	resp, err := apiCli.DeleteVolumesVolumeID(ctx, volumeID)
	if err != nil {
		return false, wrapErr("destroy volume", err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 300 {
		return false, volumeControlErr("destroy volume", resp)
	}
	return true, nil
}

// Create provisions a new volume using a one-shot Client built from opts.Config.
// Prefer NewClient when making multiple volume calls.
//
// Endpoint: POST /volumes
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func Create(ctx context.Context, name string, opts Options) (*Volume, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return nil, err
	}
	return c.Create(ctx, name)
}

// Connect validates that volumeID exists on the control plane and attaches to
// it using token (falling back to the server-minted token when token is empty).
// Prefer (*Client).Connect or ConnectByID when you do not already hold a token,
// or (*Client).ConnectWithToken for zero-RTT attachment.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func Connect(ctx context.Context, volumeID, token string, opts Options) (*Volume, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return nil, err
	}
	info, err := c.GetInfo(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	tok := token
	if tok == "" {
		tok = info.Token
	}
	return c.newVolume(info.ID, info.Name, tok, info.Domain)
}

// ConnectByID attaches to an existing volume by ID using the server-minted JWT
// and optional BYOC domain returned by GET /volumes/{volumeID}.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func ConnectByID(ctx context.Context, volumeID string, cfg e2b.Config) (*Volume, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return c.Connect(ctx, volumeID)
}

// ConnectWithToken constructs a Volume handle locally from a pre-minted JWT
// without making a control-plane HTTP round-trip. An optional BYOC domain
// overrides opts.Config.Domain when routing volume-content requests.
func ConnectWithToken(volumeID, token string, opts Options, domain ...string) (*Volume, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return nil, err
	}
	return c.ConnectWithToken(volumeID, token, domain...)
}

// GetInfo retrieves metadata and a minted content token for volumeID using a
// one-shot Client built from opts.Config.
//
// Endpoint: GET /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func GetInfo(ctx context.Context, volumeID string, opts Options) (*Info, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return nil, err
	}
	return c.GetInfo(ctx, volumeID)
}

// List returns all volumes visible to the authenticated caller using a
// one-shot Client built from opts.Config.
//
// Endpoint: GET /volumes
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func List(ctx context.Context, opts Options) ([]Info, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return nil, err
	}
	return c.List(ctx)
}

// Destroy deletes volumeID permanently using a one-shot Client built from
// opts.Config, returning (false, nil) on HTTP 404.
//
// Endpoint: DELETE /volumes/{volumeID}
// Compatibility: E2B Cloud; self-hosted runtime 2026.07..2026.30 (not in runtime < 2026.07).
func Destroy(ctx context.Context, volumeID string, opts Options) (bool, error) {
	c, err := NewClient(opts.Config)
	if err != nil {
		return false, err
	}
	return c.Destroy(ctx, volumeID)
}

func (c *Client) contentBaseURL(volumeDomain string) string {
	if c.cfg.VolumeAPIURL != "" {
		return c.cfg.VolumeAPIURL
	}
	d := volumeDomain
	if d == "" {
		d = c.cfg.Domain
	}
	return "https://api." + d
}

func volumeContentHeaders(cfgHeaders map[string]string) map[string]string {
	var ua string
	for k, v := range cfgHeaders {
		if !strings.EqualFold(k, "User-Agent") || v == "" {
			continue
		}
		if k == "User-Agent" {
			ua = v
			break
		}
		if ua == "" {
			ua = v
		}
	}
	if ua == "" {
		return nil
	}
	return map[string]string{"User-Agent": ua}
}

func validateVolumeID(op, volumeID string) error {
	switch volumeID {
	case "", ".", "..":
		return &e2b.VolumeError{
			Message: op,
			Cause:   &e2b.InvalidArgumentError{Message: `volumeID must not be empty, ".", or ".."`},
		}
	}
	return nil
}

func drainBody(body io.Reader) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
}

func (c *Client) newVolume(id, name, token, volumeDomain string) (*Volume, error) {
	effDomain := volumeDomain
	if effDomain == "" {
		effDomain = c.cfg.Domain
	}
	baseURL := c.contentBaseURL(volumeDomain)
	volCli, err := transport.NewVolumeAPIClient(baseURL, c.dataCli, token, volumeContentHeaders(c.cfg.Headers))
	if err != nil {
		return nil, &e2b.VolumeError{Message: "init volume content client", Cause: err}
	}
	return &Volume{
		ID:     id,
		Name:   name,
		Domain: effDomain,
		token:  token,
		cfg:    c.cfg,
		apiCli: c.apiCli,
		volCli: volCli,
	}, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type statusCoder interface {
	error
	HTTPStatus() int
	ErrorCode() string
}

func extractBaseError(err error, fallbackStatus int) e2b.BaseError {
	if sc, ok := errors.AsType[statusCoder](err); ok {
		status := sc.HTTPStatus()
		if status == 0 {
			status = fallbackStatus
		}
		return e2b.BaseError{StatusCode: status, Code: sc.ErrorCode()}
	}
	return e2b.BaseError{StatusCode: fallbackStatus}
}

func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		te := &e2b.TimeoutError{Message: err.Error(), Cause: err}
		return &e2b.VolumeError{Message: op, Cause: te}
	}
	if _, ok := errors.AsType[*e2b.VolumeError](err); ok {
		return err
	}
	if se, ok := errors.AsType[e2b.Error](err); ok {
		return &e2b.VolumeError{
			BaseError: extractBaseError(se, 0),
			Message:   op,
			Cause:     err,
		}
	}
	return &e2b.VolumeError{Message: op, Cause: err}
}

func volumeControlErr(op string, resp *http.Response) error {
	decoded := e2b.DecodeAPIError(resp, "")
	if decoded == nil {
		return nil
	}
	return &e2b.VolumeError{
		BaseError: extractBaseError(decoded, resp.StatusCode),
		Message:   op,
		Cause:     decoded,
	}
}

// beltErrorPayload handles both the belt string-code schema
// ({"code": "NOT_FOUND", "message": "..."}) and the control-plane schema
// ({"code": 404, "error_code": "...", "message": "..."}).
type beltErrorPayload struct {
	Code      any    `json:"code"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func volumeContentErr(op string, resp *http.Response, path string) error {
	if resp == nil {
		return &e2b.VolumeError{Message: op + ": empty response"}
	}
	var raw []byte
	if resp.Body != nil {
		raw, _ = io.ReadAll(io.LimitReader(resp.Body, maxVolumeErrorBody))
	}
	var codeStr, msg string
	var parsed beltErrorPayload
	if len(raw) > 0 && json.Unmarshal(raw, &parsed) == nil && (parsed.Message != "" || parsed.ErrorCode != "" || parsed.Code != nil) {
		msg = parsed.Message
		if parsed.ErrorCode != "" {
			codeStr = parsed.ErrorCode
		} else if s, ok := parsed.Code.(string); ok {
			codeStr = s
		}
	} else if len(raw) > 0 {
		msg = string(raw)
	}

	if resp.StatusCode == http.StatusNotFound {
		if msg == "" {
			msg = "path not found"
		}
		base := e2b.BaseError{StatusCode: http.StatusNotFound, Code: codeStr}
		fnf := &e2b.FileNotFoundError{
			BaseError: base,
			Path:      path,
			Message:   msg,
		}
		return &e2b.VolumeError{
			BaseError: base,
			Message:   op,
			Cause:     fnf,
		}
	}

	decoded := e2b.ClassifyAPIError(resp.StatusCode, codeStr, msg, "")
	if decoded == nil {
		return nil
	}
	return &e2b.VolumeError{
		BaseError: extractBaseError(decoded, resp.StatusCode),
		Message:   op,
		Cause:     decoded,
	}
}
