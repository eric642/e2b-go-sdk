package e2b

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAllTrafficCIDR(t *testing.T) {
	if AllTraffic != "0.0.0.0/0" {
		t.Fatalf("AllTraffic should equal the IPv4 catch-all CIDR, got %q", AllTraffic)
	}
}

func TestNetworkOptionsOmitsUnsetAllowPublicTraffic(t *testing.T) {
	cfg, err := buildNetworkConfig(&NetworkOptions{
		DenyOut: []string{AllTraffic},
	})
	if err != nil {
		t.Fatalf("buildNetworkConfig: %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(raw), "allowPublicTraffic") {
		t.Fatalf("omitted AllowPublicTraffic must not serialize allowPublicTraffic, got %s", raw)
	}

	boolFn := Bool
	if got := boolFn(true); got == nil || !*got {
		t.Fatalf("Bool(true) = %v", got)
	}

	cfgExplicitFalse, err := buildNetworkConfig(&NetworkOptions{
		AllowPublicTraffic: new(false),
		DNSServers:         []string{},
	})
	if err != nil {
		t.Fatalf("buildNetworkConfig: %v", err)
	}
	rawFalse, err := json.Marshal(cfgExplicitFalse)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(rawFalse), `"allowPublicTraffic":false`) {
		t.Fatalf("explicit Bool(false) must serialize allowPublicTraffic:false, got %s", rawFalse)
	}
	if !strings.Contains(string(rawFalse), `"dnsServers":[]`) {
		t.Fatalf("explicit empty DNSServers must serialize dnsServers:[], got %s", rawFalse)
	}
}
