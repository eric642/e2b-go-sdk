package e2b

import "strconv"

// envd semver gates. These mirror the upstream JS SDK's
// E2B/packages/js-sdk/src/envd/versions.ts. The SDK compares the envd version
// reported by the control plane (Sandbox.EnvdVersion) against these to keep
// behaviour compatible with both old and new envd builds.
const (
	// envdRecursiveWatch is the first envd version supporting recursive
	// directory watching. Filesystem.Watch(recursive=true) errors early below it.
	envdRecursiveWatch = "0.1.4"

	// Note: the upstream SDKs also define ENVD_COMMANDS_STDIN (0.3.0), which
	// errors when a caller *explicitly disables* stdin on older envd. The Go API
	// has no "disable stdin" flag — stdin is gated implicitly by whether
	// RunOptions.Stdin is set — so that gate has no faithful trigger here and is
	// intentionally omitted.

	// envdDefaultUser is the first envd version that infers the default user
	// server-side. At or above it the SDK omits the username (and the
	// Authorization Basic header) unless the caller supplies one; below it the
	// SDK injects DefaultUser to preserve the old behaviour.
	envdDefaultUser = "0.4.0"

	// envdEnvdClose is the first envd version supporting stdin close. Below it
	// Commands.CloseStdin errors early instead of failing at the RPC layer.
	envdEnvdClose = "0.5.2"

	// envdOctetStreamUpload is the first envd version accepting
	// application/octet-stream file uploads. Below it the SDK falls back to
	// multipart/form-data.
	envdOctetStreamUpload = "0.5.7"

	// envdDebugFallback is the synthetic version assumed in Debug mode, where no
	// real envd is reachable. It is deliberately huge so every gate treats the
	// (non-existent) envd as the newest possible build.
	envdDebugFallback = "99.99.99"
)

// DefaultUser is the username the SDK falls back to when none is supplied and
// the envd build predates server-side default-user inference (envd < 0.4.0).
const DefaultUser = defaultUser

// envdVersionForGating returns the envd version to compare gates against. In
// Debug mode no real envd is reachable, so it reports envdDebugFallback,
// matching the upstream SDKs' ENVD_DEBUG_FALLBACK behaviour.
func (s *Sandbox) envdVersionForGating() string {
	if s.cfg.Debug {
		return envdDebugFallback
	}
	return s.EnvdVersion
}

// resolveEnvdUser mirrors the upstream authenticationHeader/file-URL logic: if
// the caller supplied a username, use it; otherwise inject the fallback user
// only when the envd build predates server-side default-user inference
// (envd < 0.4.0). Returning "" means "let envd infer the default user" — no
// username query parameter and no Authorization header.
func resolveEnvdUser(envdVersion, requested string) string {
	if requested != "" {
		return requested
	}
	if compareEnvdVersions(envdVersion, envdDefaultUser) < 0 {
		return defaultUser
	}
	return ""
}

// compareEnvdVersions compares two dotted numeric versions, returning -1, 0, or
// +1 for a<b, a==b, a>b. Any pre-release / build suffix (e.g. "-rc1", "+meta")
// is ignored, and missing segments are treated as 0, so "0.4" == "0.4.0". A
// non-numeric or empty version sorts before any real version (treated as the
// oldest possible build) so behaviour stays conservative.
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

// splitVersion parses the leading dotted numeric core of a semver string into
// its integer segments, stopping at the first '-' or '+'. A leading "v"/"V"
// prefix (e.g. "v1.2.3") is stripped, matching the npm compare-versions package
// the reference SDKs use. A segment that fails to parse contributes 0.
func splitVersion(v string) []int {
	// Strip an optional leading "v" prefix (e.g. "v1.2.3").
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	// Trim any pre-release ("-") or build ("+") metadata.
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
