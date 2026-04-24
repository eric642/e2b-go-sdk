package e2b

import (
	"crypto/sha256"
	"encoding/base64"
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
// hitting envd's file endpoints.
type Signature struct {
	Value      string
	Expiration *int64 // Unix seconds; nil → no expiration
}

// SignatureOptions configures GetSignature.
type SignatureOptions struct {
	User                string
	ExpirationInSeconds int
}

// GetSignature produces a v1 signature for an envd /files request. It matches
// the Python and JS implementations byte-for-byte.
//
// raw = path:operation:user:envdAccessToken[:expiration]
// signature = "v1_" + base64url(sha256(raw))  // '=' padding stripped
func GetSignature(path string, operation SignatureOperation, envdAccessToken string, opts SignatureOptions) (Signature, error) {
	if envdAccessToken == "" {
		return Signature{}, &InvalidArgumentError{Message: "envd access token is not set; cannot generate signature"}
	}
	var expPtr *int64
	parts := []string{path, string(operation), opts.User, envdAccessToken}
	if opts.ExpirationInSeconds > 0 {
		exp := time.Now().Unix() + int64(opts.ExpirationInSeconds)
		expPtr = &exp
		parts = append(parts, strconv.FormatInt(exp, 10))
	}
	raw := strings.Join(parts, ":")
	sum := sha256.Sum256([]byte(raw))
	encoded := strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	return Signature{Value: "v1_" + encoded, Expiration: expPtr}, nil
}
