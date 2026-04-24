package e2b

// AllTraffic is the CIDR range representing all IPv4 traffic.
const AllTraffic = "0.0.0.0/0"

// NetworkOptions configures sandbox egress and public-traffic policy. Mirrors
// SandboxNetworkConfig in the REST API.
type NetworkOptions struct {
	// AllowOut is a list of CIDR blocks or IPs permitted for egress.
	AllowOut []string
	// DenyOut is a list of CIDR blocks or IPs blocked for egress.
	DenyOut []string
	// AllowPublicTraffic, when true, makes sandbox URLs reachable without
	// the traffic access token.
	AllowPublicTraffic bool
	// MaskRequestHost customizes the host template on sandbox URLs.
	MaskRequestHost string
}

// LifecycleOptions controls what happens when the sandbox timeout fires.
type LifecycleOptions struct {
	// OnTimeout is "pause" or "kill". Empty means server default (kill).
	OnTimeout string
	// AutoResume configures auto-resume for paused sandboxes.
	AutoResume bool
}

// VolumeMount attaches a volume to a sandbox path.
type VolumeMount struct {
	Name string
	Path string
}
