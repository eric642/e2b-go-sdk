package template

import "testing"

func TestSkipCache_ForcesAllSubsequentLayers(t *testing.T) {
	b := New().FromImage("alpine:3").Run("cached").SkipCache().Run("echo hi").Run("echo other")
	if len(b.instructions) != 3 {
		t.Fatalf("expected 3 instructions, got %d", len(b.instructions))
	}
	if b.instructions[0].Force {
		t.Fatal("instruction before SkipCache should not be force-marked")
	}
	if !b.instructions[1].Force || !b.instructions[2].Force {
		t.Fatalf("all instructions after SkipCache should be force-marked: %+v", b.instructions)
	}
}

func TestSkipCache_StickyAcrossCopyAndRun(t *testing.T) {
	b := New().FromImage("alpine:3").SkipCache().Copy("f", "/d").Run("echo other")
	if !b.instructions[0].Force {
		t.Fatal("COPY should be force-marked")
	}
	if !b.instructions[1].Force {
		t.Fatal("subsequent RUN should inherit sticky force flag")
	}
}

func TestWithContextAndIgnore(t *testing.T) {
	b := New().WithContext("/tmp/x").WithIgnore("*.log", "build/")
	if b.contextDir != "/tmp/x" {
		t.Fatalf("context: %s", b.contextDir)
	}
	if len(b.ignorePatterns) != 2 {
		t.Fatalf("ignore: %v", b.ignorePatterns)
	}
	if b.ignorePatterns[0] != "*.log" || b.ignorePatterns[1] != "build/" {
		t.Fatalf("ignore order: %v", b.ignorePatterns)
	}
}
