package e2b

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"unicode"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

// AllTraffic is the CIDR range representing all IPv4 traffic.
const AllTraffic = "0.0.0.0/0"

// Bool returns a pointer to v, useful for optional tri-state boolean fields
// such as NetworkOptions.AllowPublicTraffic (in Go 1.26+, built-in new(true)
// and new(false) work identically).
func Bool(v bool) *bool { return new(v) }

// NetworkTransform configures HTTP header injection or overrides applied to
// outbound requests matching a NetworkRule.
type NetworkTransform struct {
	// Headers maps HTTP header names to values injected or overridden before
	// forwarding matching egress requests.
	Headers map[string]string
}

// NetworkRule configures a per-domain transform applied to matching outbound
// HTTP/HTTPS requests. Registering a domain in Rules does not permit egress on
// its own — also include the domain in AllowOut.
type NetworkRule struct {
	Transform *NetworkTransform
}

// EgressProxyTLSOptions configures TLS on the connection to a BYOP SOCKS5
// egress proxy.
type EgressProxyTLSOptions struct {
	// Enabled turns on TLS wrapping for the SOCKS5 proxy connection.
	Enabled bool
	// ServerName overrides SNI and certificate hostname verification (defaults
	// to the host part of EgressProxyOptions.Address).
	ServerName string
	// CACert provides PEM-encoded root CA certificate(s) replacing the host
	// system trust store (never returned by GetInfo).
	CACert string
}

// EgressProxyOptions configures a SOCKS5 proxy ("bring your own proxy") through
// which outbound sandbox TCP connections are tunneled after allow/deny
// filtering.
type EgressProxyOptions struct {
	// Address is the SOCKS5 proxy address in host:port form (e.g.
	// "proxy.example.com:1080").
	Address string
	// Username is the optional SOCKS5 username (RFC 1929).
	Username string
	// Password is the optional SOCKS5 password (RFC 1929; never returned by GetInfo).
	Password string
	// TLS configures optional TLS wrapping on the hop to the SOCKS5 proxy.
	TLS *EgressProxyTLSOptions
}

// NetworkOptions configures sandbox egress and public-traffic policy. Mirrors
// SandboxNetworkConfig in the REST API.
type NetworkOptions struct {
	// AllowOut lists CIDR blocks, IPs, or domain names permitted for egress.
	AllowOut []string
	// DenyOut lists CIDR blocks or IPs blocked for egress.
	DenyOut []string
	// AllowPublicTraffic controls whether sandbox URLs accept traffic without
	// the traffic access token. Nil omits the field so the project/server
	// default (true unless overridden on the project) applies; pass Bool(false)
	// or Bool(true) (or Go 1.26 new(false)/new(true)) to set it explicitly.
	AllowPublicTraffic *bool
	// MaskRequestHost customizes the host template on sandbox URLs.
	MaskRequestHost string
	// Rules defines per-domain egress HTTP/HTTPS header transform rules
	// (requires envd >= v0.5.13).
	Rules map[string][]NetworkRule
	// EgressProxy tunnels outbound sandbox TCP through a SOCKS5 proxy.
	EgressProxy *EgressProxyOptions
	// DNSServers lists IPv4 DNS servers reachable when AllowOut contains
	// domains. Nil omits the field (allowing default 8.8.8.8); a non-nil empty
	// slice ([]string{}) allows none.
	DNSServers []string
	// HTTPSPorts lists in-sandbox ports that serve HTTPS rather than plaintext
	// HTTP.
	HTTPSPorts []int32
}

// SandboxNetworkInfo describes the network configuration reported by GetInfo.
// It aliases NetworkOptions so callers can inspect or reuse network settings
// uniformly.
type SandboxNetworkInfo = NetworkOptions

// NetworkUpdateOptions configures an atomic replacement of a running sandbox's
// egress network policy via UpdateNetwork. Omitted fields are cleared or reset
// to server defaults.
type NetworkUpdateOptions struct {
	// AllowOut lists CIDR blocks, IPs, or domain names permitted for egress.
	AllowOut []string
	// DenyOut lists CIDR blocks or IPs blocked for egress.
	DenyOut []string
	// Rules defines per-domain egress HTTP/HTTPS header transform rules
	// (requires envd >= v0.5.13).
	Rules map[string][]NetworkRule
	// EgressProxy tunnels outbound sandbox TCP through a SOCKS5 proxy.
	EgressProxy *EgressProxyOptions
	// DNSServers lists IPv4 DNS servers reachable when AllowOut contains
	// domains. Nil resets to default 8.8.8.8; []string{} allows none.
	DNSServers []string
	// AllowInternetAccess explicitly enables or disables internet access
	// (Bool(false) behaves like DenyOut: []string{AllTraffic}).
	AllowInternetAccess *bool
}

// LifecycleOptions controls what happens when the sandbox timeout fires.
type LifecycleOptions struct {
	// OnTimeout is "pause" or "kill". Empty means server default (kill).
	OnTimeout string
	// Mode controls what an auto-pause snapshot persists when OnTimeout is
	// "pause": "full" (memory + filesystem, server default) or "filesystem"
	// (filesystem only; cold-boots on resume, incompatible with AutoResume).
	// Empty omits autoPauseMemory so the server default applies.
	Mode string
	// AutoResume configures auto-resume for paused sandboxes.
	AutoResume bool
}

// IamToken defines a workload identity token registered on a sandbox.
type IamToken struct {
	// Audience of the workload token, stored exactly as provided.
	Audience string
	// TokenType is the workload token type (e.g. "JWT-SVID").
	TokenType string
}

// IamOptions configures sandbox workload identity tokens on Create / CreateV1.
type IamOptions struct {
	// Tokens maps caller-chosen token names to token definitions. Reference a
	// registered token in NetworkRule header transforms via IamTokenPlaceholder(name).
	Tokens map[string]IamToken
}

// VolumeMount attaches a volume to a sandbox path.
type VolumeMount struct {
	Name string
	Path string
}

// UpdateNetwork atomically replaces the egress network configuration of a
// running sandbox by ID. Omitted fields are cleared or reset on the server.
//
// Endpoint: PUT /sandboxes/{sandboxID}/network
// Compatibility: E2B Cloud; self-hosted runtime 2026.12..2026.30 (not in runtime < 2026.12).
// Envd: >= v0.5.13 when Rules is set
func (c *Client) UpdateNetwork(ctx context.Context, sandboxID string, opts NetworkUpdateOptions) error {
	return putSandboxNetwork(ctx, c.apiCli, sandboxID, opts)
}

// UpdateNetwork atomically replaces the egress network configuration of this
// running sandbox. Omitted fields are cleared or reset on the server.
//
// Endpoint: PUT /sandboxes/{sandboxID}/network
// Compatibility: E2B Cloud; self-hosted runtime 2026.12..2026.30 (not in runtime < 2026.12).
// Envd: >= v0.5.13 when Rules is set
func (s *Sandbox) UpdateNetwork(ctx context.Context, opts NetworkUpdateOptions) error {
	return putSandboxNetwork(ctx, s.apiCli, s.ID, opts)
}

func putSandboxNetwork(ctx context.Context, apiCli *apiclient.Client, sandboxID string, opts NetworkUpdateOptions) error {
	body, err := buildNetworkUpdateConfig(opts)
	if err != nil {
		return err
	}
	resp, err := apiCli.PutSandboxesSandboxIDNetwork(ctx, sandboxID, body)
	if err != nil {
		return mapHTTPOrCtx(err)
	}
	defer resp.Body.Close()
	return mapHTTPErr(resp, sandboxID)
}

func buildNetworkConfig(n *NetworkOptions) (*apiclient.SandboxNetworkConfig, error) {
	if n == nil {
		return nil, nil
	}
	ep, err := buildEgressProxyConfig(n.EgressProxy)
	if err != nil {
		return nil, err
	}
	var httpsPorts *[]uint32
	if n.HTTPSPorts != nil {
		ports := make([]uint32, 0, len(n.HTTPSPorts))
		for _, p := range n.HTTPSPorts {
			if p < 1 || p > 65535 {
				return nil, &InvalidArgumentError{Message: fmt.Sprintf("network.HTTPSPorts port %d out of range 1..65535", p)}
			}
			ports = append(ports, uint32(p))
		}
		httpsPorts = &ports
	}
	var allowPublic *bool
	if n.AllowPublicTraffic != nil {
		v := *n.AllowPublicTraffic
		allowPublic = &v
	}
	var dnsServers *[]string
	if n.DNSServers != nil {
		s := slices.Clone(n.DNSServers)
		dnsServers = &s
	}
	return &apiclient.SandboxNetworkConfig{
		AllowOut:           cloneSlicePtr(n.AllowOut),
		DenyOut:            cloneSlicePtr(n.DenyOut),
		AllowPublicTraffic: allowPublic,
		MaskRequestHost:    nilIfEmpty(n.MaskRequestHost),
		Rules:              buildNetworkRules(n.Rules),
		EgressProxy:        ep,
		DnsServers:         dnsServers,
		HttpsPorts:         httpsPorts,
	}, nil
}

func buildNetworkUpdateConfig(opts NetworkUpdateOptions) (apiclient.SandboxNetworkUpdateConfig, error) {
	ep, err := buildEgressProxyConfig(opts.EgressProxy)
	if err != nil {
		return apiclient.SandboxNetworkUpdateConfig{}, err
	}
	var allowInternet *bool
	if opts.AllowInternetAccess != nil {
		v := *opts.AllowInternetAccess
		allowInternet = &v
	}
	var dnsServers *[]string
	if opts.DNSServers != nil {
		s := slices.Clone(opts.DNSServers)
		dnsServers = &s
	}
	return apiclient.SandboxNetworkUpdateConfig{
		AllowOut:            cloneSlicePtr(opts.AllowOut),
		DenyOut:             cloneSlicePtr(opts.DenyOut),
		Rules:               buildNetworkRules(opts.Rules),
		EgressProxy:         ep,
		DnsServers:          dnsServers,
		AllowInternetAccess: allowInternet,
	}, nil
}

func buildEgressProxyConfig(ep *EgressProxyOptions) (*apiclient.SandboxEgressProxyConfig, error) {
	if ep == nil {
		return nil, nil
	}
	if ep.Address == "" {
		return nil, &InvalidArgumentError{Message: "network.EgressProxy.Address must not be empty"}
	}
	out := &apiclient.SandboxEgressProxyConfig{
		Address:  ep.Address,
		Username: nilIfEmpty(ep.Username),
		Password: nilIfEmpty(ep.Password),
	}
	if ep.TLS != nil && (ep.TLS.Enabled || ep.TLS.ServerName != "" || ep.TLS.CACert != "") {
		out.Tls = &apiclient.SandboxEgressProxyTLSConfig{
			Enabled:    true,
			ServerName: nilIfEmpty(ep.TLS.ServerName),
			CaCert:     nilIfEmpty(ep.TLS.CACert),
		}
	}
	return out, nil
}

func buildNetworkRules(rules map[string][]NetworkRule) *map[string][]apiclient.SandboxNetworkRule {
	if rules == nil {
		return nil
	}
	out := make(map[string][]apiclient.SandboxNetworkRule, len(rules))
	for host, list := range rules {
		converted := make([]apiclient.SandboxNetworkRule, 0, len(list))
		for _, r := range list {
			var ar apiclient.SandboxNetworkRule
			if r.Transform != nil {
				var hdrs *map[string]string
				if r.Transform.Headers != nil {
					cp := maps.Clone(r.Transform.Headers)
					hdrs = &cp
				}
				ar.Transform = &apiclient.SandboxNetworkTransform{Headers: hdrs}
			}
			converted = append(converted, ar)
		}
		out[host] = converted
	}
	return &out
}

func buildSandboxIam(iam *IamOptions) (*apiclient.SandboxIam, error) {
	if iam == nil || len(iam.Tokens) == 0 {
		return nil, nil
	}
	tokens := make(apiclient.SandboxIamTokens, len(iam.Tokens))
	for name, tok := range iam.Tokens {
		if err := validatePlaceholderName("iam token", name); err != nil {
			return nil, err
		}
		tokens[name] = apiclient.SandboxIamToken{
			Audience:  tok.Audience,
			TokenType: tok.TokenType,
		}
	}
	return &apiclient.SandboxIam{Tokens: &tokens}, nil
}

func networkInfoFromAPI(d *apiclient.SandboxNetworkConfig) *SandboxNetworkInfo {
	if d == nil {
		return nil
	}
	info := &SandboxNetworkInfo{}
	if d.AllowOut != nil {
		info.AllowOut = slices.Clone(*d.AllowOut)
	}
	if d.DenyOut != nil {
		info.DenyOut = slices.Clone(*d.DenyOut)
	}
	if d.AllowPublicTraffic != nil {
		v := *d.AllowPublicTraffic
		info.AllowPublicTraffic = &v
	}
	if d.MaskRequestHost != nil {
		info.MaskRequestHost = *d.MaskRequestHost
	}
	if d.DnsServers != nil {
		info.DNSServers = slices.Clone(*d.DnsServers)
	}
	if d.HttpsPorts != nil {
		ports := make([]int32, 0, len(*d.HttpsPorts))
		for _, p := range *d.HttpsPorts {
			ports = append(ports, int32(p))
		}
		info.HTTPSPorts = ports
	}
	if d.EgressProxy != nil {
		ep := &EgressProxyOptions{Address: d.EgressProxy.Address}
		if d.EgressProxy.Username != nil {
			ep.Username = *d.EgressProxy.Username
		}
		if d.EgressProxy.Tls != nil && d.EgressProxy.Tls.Enabled {
			tlsCfg := &EgressProxyTLSOptions{Enabled: true}
			if d.EgressProxy.Tls.ServerName != nil {
				tlsCfg.ServerName = *d.EgressProxy.Tls.ServerName
			}
			ep.TLS = tlsCfg
		}
		info.EgressProxy = ep
	}
	info.Rules = networkRulesFromAPI(d.Rules)
	return info
}

func networkRulesFromAPI(raw *map[string][]apiclient.SandboxNetworkRule) map[string][]NetworkRule {
	if raw == nil {
		return nil
	}
	rules := make(map[string][]NetworkRule, len(*raw))
	for host, list := range *raw {
		converted := make([]NetworkRule, 0, len(list))
		for _, r := range list {
			var nr NetworkRule
			if r.Transform != nil {
				nr.Transform = &NetworkTransform{}
				if r.Transform.Headers != nil {
					nr.Transform.Headers = maps.Clone(*r.Transform.Headers)
				}
			}
			converted = append(converted, nr)
		}
		rules[host] = converted
	}
	return rules
}

func validatePlaceholderName(kind, name string) error {
	if name == "" {
		return &InvalidArgumentError{Message: kind + " name must not be empty"}
	}
	for _, r := range name {
		if r == '{' || r == '}' || unicode.IsControl(r) {
			return &InvalidArgumentError{Message: fmt.Sprintf("%s name %q cannot contain '{', '}', or control characters", kind, name)}
		}
	}
	return nil
}

func cloneSlicePtr(s []string) *[]string {
	if len(s) == 0 {
		return nil
	}
	cp := slices.Clone(s)
	return &cp
}
