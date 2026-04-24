// Package volume provides a client for E2B persistent volumes.
//
// A Volume is independent of a specific sandbox: it can be mounted via
// e2b.VolumeMount during Sandbox.Create. Content operations (read/write/
// list) talk to the volume-content REST API and authenticate with a
// per-volume JWT issued by the control plane.
package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/eric642/e2b-go-sdk"
	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
	"github.com/eric642/e2b-go-sdk/internal/transport"
	"github.com/eric642/e2b-go-sdk/internal/volumeapi"
)

// Volume is a handle for one persistent volume.
type Volume struct {
	ID    string
	Name  string
	token string

	cfg     e2b.Config
	apiCli  *apiclient.Client
	volCli  *volumeapi.Client
	httpCli *http.Client
}

// Info captures volume metadata.
type Info struct {
	ID    string
	Name  string
	Token string
}

// Options configure a single volume call.
type Options struct {
	Config e2b.Config
}

// Create provisions a new volume.
func Create(ctx context.Context, name string, opts Options) (*Volume, error) {
	cfg, apiCli, hc, err := bootstrap(opts.Config)
	if err != nil {
		return nil, err
	}
	body := apiclient.PostVolumesJSONRequestBody{Name: name}
	resp, err := apiCli.PostVolumes(ctx, body)
	if err != nil {
		return nil, wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, volumeHTTPErr(resp)
	}
	var parsed apiclient.VolumeAndToken
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &e2b.VolumeError{Message: "decode create response", Cause: err}
	}
	return newVolume(cfg, apiCli, hc, parsed.VolumeID, parsed.Name, parsed.Token)
}

// Connect attaches to an existing volume using a pre-issued token.
func Connect(ctx context.Context, volumeID, token string, opts Options) (*Volume, error) {
	cfg, apiCli, hc, err := bootstrap(opts.Config)
	if err != nil {
		return nil, err
	}
	// Validate the volume exists.
	resp, err := apiCli.GetVolumesVolumeID(ctx, volumeID)
	if err != nil {
		return nil, wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, volumeHTTPErr(resp)
	}
	var parsed apiclient.Volume
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &e2b.VolumeError{Message: "decode connect response", Cause: err}
	}
	return newVolume(cfg, apiCli, hc, parsed.VolumeID, parsed.Name, token)
}

// List returns all volumes visible to the authenticated caller.
func List(ctx context.Context, opts Options) ([]Info, error) {
	_, apiCli, _, err := bootstrap(opts.Config)
	if err != nil {
		return nil, err
	}
	resp, err := apiCli.GetVolumes(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, volumeHTTPErr(resp)
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

// Delete removes this volume permanently.
func (v *Volume) Delete(ctx context.Context) error {
	resp, err := v.apiCli.DeleteVolumesVolumeID(ctx, v.ID)
	if err != nil {
		return wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return volumeHTTPErr(resp)
	}
	return nil
}

// ReadFile downloads the file at path into memory.
func (v *Volume) ReadFile(ctx context.Context, path string) ([]byte, error) {
	resp, err := v.volCli.GetVolumecontentVolumeIDFile(ctx, v.ID,
		&volumeapi.GetVolumecontentVolumeIDFileParams{Path: path})
	if err != nil {
		return nil, wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, volumeHTTPErr(resp)
	}
	return io.ReadAll(resp.Body)
}

// WriteFile uploads content to path inside the volume.
func (v *Volume) WriteFile(ctx context.Context, path string, content []byte) error {
	resp, err := v.volCli.PutVolumecontentVolumeIDFileWithBody(
		ctx, v.ID,
		&volumeapi.PutVolumecontentVolumeIDFileParams{Path: path},
		"application/octet-stream",
		bytes.NewReader(content),
	)
	if err != nil {
		return wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return volumeHTTPErr(resp)
	}
	return nil
}

// Remove deletes a file or directory from the volume.
func (v *Volume) Remove(ctx context.Context, path string) error {
	resp, err := v.volCli.DeleteVolumecontentVolumeIDPath(
		ctx, v.ID,
		&volumeapi.DeleteVolumecontentVolumeIDPathParams{Path: path},
	)
	if err != nil {
		return wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return volumeHTTPErr(resp)
	}
	return nil
}

// MakeDir creates a directory (including parents) at path.
func (v *Volume) MakeDir(ctx context.Context, path string) error {
	resp, err := v.volCli.PostVolumecontentVolumeIDDir(
		ctx, v.ID,
		&volumeapi.PostVolumecontentVolumeIDDirParams{Path: path},
	)
	if err != nil {
		return wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return volumeHTTPErr(resp)
	}
	return nil
}

// Token returns the auth token for this volume. Callers can persist it and
// later pass it to Connect.
func (v *Volume) Token() string { return v.token }

// bootstrap resolves config and builds an API client for volume-level ops
// (create/connect/list/delete). Returns the resolved config, api client,
// and http client.
func bootstrap(cfg e2b.Config) (e2b.Config, *apiclient.Client, *http.Client, error) {
	// Use the same REST surface as the core SDK.
	cfgOut := cfg
	// Call the sandbox.Config resolve via indirect path: use default
	// behaviour via a throwaway client.
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	apiCli, err := transport.NewAPIClient(apiURL(cfg), hc,
		transport.Auth{APIKey: apiKey(cfg), AccessToken: accessToken(cfg), Headers: cfg.Headers})
	if err != nil {
		return cfgOut, nil, nil, &e2b.VolumeError{Message: "init api client", Cause: err}
	}
	return cfgOut, apiCli, hc, nil
}

// newVolume builds the volume content client using the returned token.
func newVolume(cfg e2b.Config, apiCli *apiclient.Client, hc *http.Client, id, name, token string) (*Volume, error) {
	volCli, err := transport.NewVolumeAPIClient(apiURL(cfg), hc, token, cfg.Headers)
	if err != nil {
		return nil, &e2b.VolumeError{Message: "init volume content client", Cause: err}
	}
	return &Volume{
		ID:      id,
		Name:    name,
		token:   token,
		cfg:     cfg,
		apiCli:  apiCli,
		volCli:  volCli,
		httpCli: hc,
	}, nil
}

// wrap converts a transport error into an e2b.VolumeError unless it's
// already an SDK error.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var se e2b.Error
	if errors.As(err, &se) {
		return err
	}
	return &e2b.VolumeError{Message: "volume request failed", Cause: err}
}

// volumeHTTPErr decodes an HTTP error body as VolumeError.
func volumeHTTPErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return &e2b.VolumeError{Message: fmt.Sprintf("http %d: %s", resp.StatusCode, string(body))}
}

// Reach into e2b.Config for fields not re-exported publicly. Since
// e2b.Config fields are capitalized they are accessible directly.
func apiURL(cfg e2b.Config) string {
	if cfg.APIURL != "" {
		return cfg.APIURL
	}
	d := cfg.Domain
	if d == "" {
		d = e2b.DefaultDomain
	}
	if cfg.Debug {
		return "http://localhost:3000"
	}
	return "https://api." + d
}
func apiKey(cfg e2b.Config) string      { return cfg.APIKey }
func accessToken(cfg e2b.Config) string { return cfg.AccessToken }
