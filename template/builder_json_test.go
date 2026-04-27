package template

import (
	"encoding/json"
	"testing"
)

func TestToJSON_IncludesFromImageAndSteps(t *testing.T) {
	b := New().FromImage("alpine:3").RunCmd("echo hi")
	raw, err := b.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if parsed["fromImage"] != "alpine:3" {
		t.Fatalf("fromImage: %v", parsed["fromImage"])
	}
	steps, ok := parsed["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps: %v", parsed["steps"])
	}
}

func TestFinalBuilder_ToJSON(t *testing.T) {
	f := New().FromImage("alpine:3").SetStartCmd("run", WaitForPort(3000))
	raw, err := f.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if parsed["startCmd"] != "run" {
		t.Fatalf("startCmd: %v", parsed["startCmd"])
	}
}
