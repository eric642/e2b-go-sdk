package template

import "testing"

func TestSkipCache_ForcesOnlyNextLayer(t *testing.T) {
	b := New().FromImage("alpine:3").SkipCache().Run("echo hi").Run("echo other")
	if len(b.instructions) != 2 {
		t.Fatalf("expected 2 instructions, got %d", len(b.instructions))
	}
	if !b.instructions[0].Force {
		t.Fatal("first instruction should be force-marked")
	}
	if b.instructions[1].Force {
		t.Fatal("second instruction should not be force-marked")
	}
}

func TestSkipCache_ResetsAfterCopy(t *testing.T) {
	b := New().FromImage("alpine:3").SkipCache().Copy("f", "/d").Run("echo other")
	if !b.instructions[0].Force {
		t.Fatal("COPY should be force-marked")
	}
	if b.instructions[1].Force {
		t.Fatal("RUN should not inherit force flag")
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
