package template

import (
	"path/filepath"
	"testing"
)

func TestInstructionsWithHashes_ComputesCopyHash(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "f.txt", "abc")

	b := New().FromImage("alpine:3").WithContext(ctx).Copy("f.txt", "/x/")
	steps, err := b.instructionsWithHashes()
	if err != nil {
		t.Fatal(err)
	}
	var copyStep *instruction
	for i := range steps {
		if steps[i].Type == instTypeCopy {
			copyStep = &steps[i]
			break
		}
	}
	if copyStep == nil || copyStep.FilesHash == "" {
		t.Fatalf("no COPY step with hash: %#v", steps)
	}
}

func TestInstructionsWithHashes_CopyWithoutContextErrors(t *testing.T) {
	b := New().FromImage("alpine:3").Copy("f.txt", "/x/")
	_, err := b.instructionsWithHashes()
	if err == nil {
		t.Fatal("expected error for COPY without WithContext")
	}
}

func TestSerialize_ShapesAPIBody(t *testing.T) {
	b := New().FromImage("alpine:3").Run("echo hi")
	body, err := b.serialize(false)
	if err != nil {
		t.Fatal(err)
	}
	if body.FromImage == nil || *body.FromImage != "alpine:3" {
		t.Fatalf("fromImage: %v", body.FromImage)
	}
	if body.Steps == nil || len(*body.Steps) != 1 || (*body.Steps)[0].Type != "RUN" {
		t.Fatalf("steps: %#v", body.Steps)
	}
}

func TestSerialize_IncludesCopyHash(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx, "app.txt", "hi")
	b := New().FromImage("alpine:3").WithContext(ctx).Copy("app.txt", "/app/")
	body, err := b.serialize(false)
	if err != nil {
		t.Fatal(err)
	}
	if body.Steps == nil || len(*body.Steps) != 1 {
		t.Fatalf("steps: %+v", body.Steps)
	}
	step := (*body.Steps)[0]
	if step.Type != "COPY" || step.FilesHash == nil || *step.FilesHash == "" {
		t.Fatalf("copy step missing hash: %#v", step)
	}
	_ = filepath.Clean // silence import
}
