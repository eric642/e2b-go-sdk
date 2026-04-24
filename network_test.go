package e2b

import "testing"

func TestAllTrafficCIDR(t *testing.T) {
	if AllTraffic != "0.0.0.0/0" {
		t.Fatalf("AllTraffic should equal the IPv4 catch-all CIDR, got %q", AllTraffic)
	}
}

func TestNetworkOptionsZeroValue(t *testing.T) {
	// A zero-valued NetworkOptions must be a usable (no-op) policy. If any
	// fields were accidentally required we'd know because constructing
	// NetworkOptions{} would fail a compilation.
	var opts NetworkOptions
	if len(opts.AllowOut)+len(opts.DenyOut) != 0 {
		t.Fatal("slices must default to empty")
	}
	if opts.AllowPublicTraffic {
		t.Fatal("AllowPublicTraffic must default to false")
	}
	if opts.MaskRequestHost != "" {
		t.Fatal("MaskRequestHost must default to empty")
	}
}

func TestLifecycleOptionsZeroValue(t *testing.T) {
	var l LifecycleOptions
	if l.OnTimeout != "" || l.AutoResume {
		t.Fatalf("LifecycleOptions must have zero defaults, got %+v", l)
	}
}

func TestVolumeMountZeroValue(t *testing.T) {
	var v VolumeMount
	if v.Name != "" || v.Path != "" {
		t.Fatalf("VolumeMount must have zero defaults, got %+v", v)
	}
}
