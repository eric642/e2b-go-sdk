package template

import "testing"

func TestBuildStatusConstants(t *testing.T) {
	if BuildStatusBuilding != "building" ||
		BuildStatusWaiting != "waiting" ||
		BuildStatusReady != "ready" ||
		BuildStatusError != "error" {
		t.Fatal("BuildStatusValue constants drifted")
	}
}
