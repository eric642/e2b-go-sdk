package template

import "testing"

func TestTypesCompileAndZeroValue(t *testing.T) {
	_ = BuildInfo{}
	_ = BuildEvent{}
	_ = BuildStatus{Status: BuildStatusReady}
	_ = BuildReason{}
	_ = TagInfo{}
	_ = TemplateTag{}

	if BuildStatusBuilding != "building" ||
		BuildStatusWaiting != "waiting" ||
		BuildStatusReady != "ready" ||
		BuildStatusError != "error" {
		t.Fatal("BuildStatusValue constants drifted")
	}
}
