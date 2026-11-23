package e2b

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SignatureOperation is either "read" or "write".
type SignatureOperation string

const (
	SignatureRead  SignatureOperation = "read"
	SignatureWrite SignatureOperation = "write"
)

// Signature is an opaque v1 signature usable as a query parameter when
// hitting envd's /files endpoints.
type Signature struct {
	Value      string
	Expiration *int64 // Unix seconds; nil -> no expiration
}

// SignatureOptions configures GetSignature, Sandbox.UploadURL, and
// Sandbox.DownloadURL.
type SignatureOptions struct {
	User                string
	ExpirationInSeconds int
}

// GetSignature produces a v1 SHA-256 signature for an envd /files request. It
// matches the Python and JS SDK implementations byte-for-byte.
//
//	raw = path:operation:user:envdAccessToken[:expiration]
//	signature = "v1_" + base64std_nopad(sha256(raw))
func GetSignature(filePath string, operation SignatureOperation, envdAccessToken string, opts SignatureOptions) (Signature, error) {
	if operation != SignatureRead && operation != SignatureWrite {
		return Signature{}, &InvalidArgumentError{
			Message: fmt.Sprintf("signature operation must be %q or %q, got %q", SignatureRead, SignatureWrite, operation),
		}
	}
	if envdAccessToken == "" {
		return Signature{}, &InvalidArgumentError{Message: "envd access token is not set; cannot generate signature"}
	}
	if opts.ExpirationInSeconds < 0 {
		return Signature{}, &InvalidArgumentError{Message: "signature expiration must be non-negative"}
	}
	var expPtr *int64
	parts := []string{filePath, string(operation), opts.User, envdAccessToken}
	if opts.ExpirationInSeconds > 0 {
		exp := time.Now().Unix() + int64(opts.ExpirationInSeconds)
		expPtr = &exp
		parts = append(parts, strconv.FormatInt(exp, 10))
	}
	raw := strings.Join(parts, ":")
	sum := sha256.Sum256([]byte(raw))
	encoded := base64.RawStdEncoding.EncodeToString(sum[:])
	return Signature{Value: "v1_" + encoded, Expiration: expPtr}, nil
}

// UploadURL builds a local HTTP URL the caller can POST a file to (/files).
// When filePath is empty, the ?path= query parameter is omitted (for multipart
// uploads that supply filenames in part headers). For envd >= v0.4.0, the
// default username parameter is omitted unless opts.User is set. Returns
// *InvalidArgumentError if opts.ExpirationInSeconds > 0 when the sandbox has no
// EnvdAccessToken.
func (s *Sandbox) UploadURL(filePath string, opts SignatureOptions) (string, error) {
	return s.buildFileURL(filePath, SignatureWrite, opts, true)
}

// DownloadURL builds a local HTTP URL the caller can GET a file from (/files).
// For envd >= v0.4.0, the default username parameter is omitted unless
// opts.User is set. Returns *InvalidArgumentError if
// opts.ExpirationInSeconds > 0 when the sandbox has no EnvdAccessToken.
func (s *Sandbox) DownloadURL(filePath string, opts SignatureOptions) (string, error) {
	return s.buildFileURL(filePath, SignatureRead, opts, false)
}

// SignedUploadURL is an explicit alias for UploadURL returning a pre-signed
// (when EnvdAccessToken is set) upload URL for POST /files.
func (s *Sandbox) SignedUploadURL(filePath string, opts SignatureOptions) (string, error) {
	return s.UploadURL(filePath, opts)
}

// SignedDownloadURL is an explicit alias for DownloadURL returning a pre-signed
// (when EnvdAccessToken is set) download URL for GET /files.
func (s *Sandbox) SignedDownloadURL(filePath string, opts SignatureOptions) (string, error) {
	return s.DownloadURL(filePath, opts)
}

type fileURLParams struct {
	domain              string
	accessToken         string
	resolvedUser        string
	filePath            string
	op                  SignatureOperation
	expirationInSeconds int
	upload              bool
}

func (s *Sandbox) buildFileURL(filePath string, op SignatureOperation, opts SignatureOptions, upload bool) (string, error) {
	if opts.ExpirationInSeconds < 0 {
		return "", &InvalidArgumentError{Message: "signature expiration must be non-negative"}
	}
	domain, accessToken, gatingVer, _ := s.envdSnapshot()
	return s.buildFileURLFromSnapshot(fileURLParams{
		domain:              domain,
		accessToken:         accessToken,
		resolvedUser:        resolveEnvdUser(gatingVer, opts.User),
		filePath:            filePath,
		op:                  op,
		expirationInSeconds: opts.ExpirationInSeconds,
		upload:              upload,
	})
}

func (s *Sandbox) buildFileURLFromSnapshot(p fileURLParams) (string, error) {
	if p.expirationInSeconds < 0 {
		return "", &InvalidArgumentError{Message: "signature expiration must be non-negative"}
	}
	if p.expirationInSeconds > 0 && p.accessToken == "" {
		return "", &InvalidArgumentError{
			Message: "signature expiration can only be used when the sandbox has an envd access token (Secure enabled)",
		}
	}

	base := strings.TrimRight(s.cfg.sandboxURL(s.ID, p.domain), "/")
	params := make([]string, 0, 4)
	if !p.upload || p.filePath != "" {
		params = append(params, "path="+url.QueryEscape(p.filePath))
	}
	if p.resolvedUser != "" {
		params = append(params, "username="+url.QueryEscape(p.resolvedUser))
	}
	if p.accessToken != "" {
		sig, err := GetSignature(p.filePath, p.op, p.accessToken, SignatureOptions{
			User:                p.resolvedUser,
			ExpirationInSeconds: p.expirationInSeconds,
		})
		if err != nil {
			return "", err
		}
		params = append(params, "signature="+url.QueryEscape(sig.Value))
		if sig.Expiration != nil {
			params = append(params, "signature_expiration="+strconv.FormatInt(*sig.Expiration, 10))
		}
	}
	if len(params) == 0 {
		return base + "/files", nil
	}
	return base + "/files?" + strings.Join(params, "&"), nil
}
