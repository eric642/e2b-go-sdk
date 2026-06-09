package e2b

import "testing"

func TestCompareEnvdVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.4.0", "0.4.0", 0},
		{"0.4.0", "0.5.7", -1},
		{"0.5.7", "0.4.0", 1},
		{"0.4", "0.4.0", 0}, // missing segment == 0
		{"0.5", "0.4.0", 1},
		{"0.4.0", "0.4.1", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.5.7-rc1", "0.5.7", 0},  // pre-release suffix ignored
		{"0.5.7+meta", "0.5.7", 0}, // build metadata ignored
		{"99.99.99", "0.5.7", 1},   // debug fallback newest
		{"", "0.1.4", -1},          // empty sorts oldest
		{"", "", 0},
		{"v1.2.3", "1.2.3", 0}, // leading "v" stripped
		{"v1.0.0", "0.5.7", 1}, // v-prefixed compares numerically, not as 0
		{"V2", "1.9.9", 1},     // uppercase "V" stripped too
		{"v1.2.3", "0.5.7", 1},
	}
	for _, c := range cases {
		if got := compareEnvdVersions(c.a, c.b); got != c.want {
			t.Errorf("compareEnvdVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestEnvdGateThresholds(t *testing.T) {
	// Below the gate.
	if compareEnvdVersions("0.3.9", envdDefaultUser) >= 0 {
		t.Error("0.3.9 should be < envdDefaultUser (0.4.0)")
	}
	// At/above the gate.
	if compareEnvdVersions("0.4.0", envdDefaultUser) < 0 {
		t.Error("0.4.0 should be >= envdDefaultUser")
	}
	if compareEnvdVersions("0.5.6", envdOctetStreamUpload) >= 0 {
		t.Error("0.5.6 should be < envdOctetStreamUpload (0.5.7)")
	}
	if compareEnvdVersions("0.5.7", envdOctetStreamUpload) < 0 {
		t.Error("0.5.7 should be >= envdOctetStreamUpload")
	}
	if compareEnvdVersions("0.1.3", envdRecursiveWatch) >= 0 {
		t.Error("0.1.3 should be < envdRecursiveWatch (0.1.4)")
	}
	if compareEnvdVersions("0.5.1", envdEnvdClose) >= 0 {
		t.Error("0.5.1 should be < envdEnvdClose (0.5.2)")
	}
}

func TestEnvdVersionForGating(t *testing.T) {
	sbx := &Sandbox{EnvdVersion: "0.2.0"}
	sbx.cfg = Config{}
	if got := sbx.envdVersionForGating(); got != "0.2.0" {
		t.Errorf("non-debug gating version = %q want 0.2.0", got)
	}
	dbg := &Sandbox{EnvdVersion: "0.2.0"}
	dbg.cfg = Config{Debug: true}
	if got := dbg.envdVersionForGating(); got != envdDebugFallback {
		t.Errorf("debug gating version = %q want %q", got, envdDebugFallback)
	}
}
