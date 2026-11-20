package e2b

import (
	"context"
	"strconv"
	"time"

	"github.com/eric642/e2b-go-sdk/internal/transport"
)

// envd semver gates (through stable envd-v0.6.13). These mirror the upstream JS
// SDK's E2B/packages/js-sdk/src/envd/versions.ts and envd server handlers. The
// SDK compares the envd version reported by the control plane
// (Sandbox.EnvdVersion) against these gates to keep behaviour compatible across
// both legacy self-hosted and current E2B Cloud envd builds.
const (
	// envdRecursiveWatch is the first envd version supporting recursive
	// directory watching. Filesystem.Watch(recursive=true) errors early below it.
	envdRecursiveWatch = "0.1.4"

	// envdMetrics is the first envd version exposing sandbox resource usage
	// metrics via GET /metrics.
	envdMetrics = "0.1.5"

	// envdDefaultUser is the first envd version that infers the default user
	// server-side. At or above it the SDK omits the username (and the
	// Authorization Basic header) unless the caller supplies one; below it the
	// SDK injects DefaultUser to preserve the old behaviour.
	envdDefaultUser = "0.4.0"

	// envdEnvdClose is the first envd version supporting stdin close. Below it
	// Commands.CloseStdin errors early instead of failing at the RPC layer.
	envdEnvdClose = "0.5.2"

	// envdFileCompose is the first envd version supporting zero-copy file
	// concatenation via POST /files/compose.
	envdFileCompose = "0.5.5"

	// envdOctetStreamUpload is the first envd version accepting
	// application/octet-stream file uploads. Below it the SDK falls back to
	// multipart/form-data.
	envdOctetStreamUpload = "0.5.7"

	// envdFileMetadata is the first envd version supporting user-defined xattr
	// metadata via X-Metadata-* upload headers and EntryInfo.Metadata.
	envdFileMetadata = "0.6.2"

	// envdWatchIncludeEntry is the first envd version supporting
	// WatchDirRequest.include_entry to populate FilesystemEvent.Entry.
	envdWatchIncludeEntry = "0.6.3"

	// envdWatchNetworkMounts is the first envd version supporting
	// WatchDirRequest.allow_network_mounts for NFS/CIFS/SMB/FUSE mounts.
	envdWatchNetworkMounts = "0.6.4"

	envdDebugFallback = "99.99.99"
)

// DefaultUser is the username the SDK falls back to when none is supplied and
// the envd build predates server-side default-user inference (envd < 0.4.0).
const DefaultUser = defaultUser

func (s *Sandbox) envdVersionForGating() string {
	if s == nil {
		return ""
	}
	if s.cfg.Debug {
		return envdDebugFallback
	}
	s.mu.RLock()
	ver := s.EnvdVersion
	s.mu.RUnlock()
	return ver
}

func (s *Sandbox) envdVersionRaw() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	ver := s.EnvdVersion
	s.mu.RUnlock()
	return ver
}

func (s *Sandbox) envdClients() *transport.EnvdClients {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	cli := s.envd
	s.mu.RUnlock()
	return cli
}

func (s *Sandbox) envdSnapshot() (domain, accessToken, gatingVer string, cli *transport.EnvdClients) {
	if s == nil {
		return "", "", "", nil
	}
	s.mu.RLock()
	domain = s.Domain
	accessToken = s.EnvdAccessToken
	gatingVer = s.EnvdVersion
	cli = s.envd
	s.mu.RUnlock()
	if s.cfg.Debug {
		gatingVer = envdDebugFallback
	}
	return domain, accessToken, gatingVer, cli
}

func withEnvdCallOpts(ctx context.Context, sbx *Sandbox, requestedUser string, reqTimeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolvedUser := resolveEnvdUser(sbx.envdVersionForGating(), requestedUser)
	ctx = transport.WithEnvdUser(ctx, resolvedUser)
	if reqTimeout > 0 {
		return context.WithTimeout(ctx, reqTimeout)
	}
	return ctx, func() {}
}

func unaryEnvdTimeout(sbx *Sandbox, explicit time.Duration) time.Duration {
	if explicit > 0 {
		return explicit
	}
	if sbx != nil && !sbx.cfg.RequestTimeoutDisabled && sbx.cfg.RequestTimeout > 0 {
		return sbx.cfg.RequestTimeout
	}
	return 0
}

func resolveEnvdUser(envdVersion, requested string) string {
	if requested != "" {
		return requested
	}
	if compareEnvdVersions(envdVersion, envdDefaultUser) < 0 {
		return defaultUser
	}
	return ""
}

func compareEnvdVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	n := max(len(as), len(bs))
	for i := range n {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func splitVersion(v string) []int {
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '-' || v[i] == '+' {
			v = v[:i]
			break
		}
	}
	if v == "" {
		return nil
	}
	var out []int
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == '.' {
			seg := v[start:i]
			n, err := strconv.Atoi(seg)
			if err != nil {
				n = 0
			}
			out = append(out, n)
			start = i + 1
		}
	}
	return out
}
