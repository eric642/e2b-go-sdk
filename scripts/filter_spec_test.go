package main

import (
	"strings"
	"testing"
)

func TestFilterText_PrunesEmptyPathAfterStripNotImplemented(t *testing.T) {
	t.Parallel()

	t.Run("all operations stripped with path parameters", func(t *testing.T) {
		t.Parallel()

		input := `openapi: 3.0.0
paths:
  /future/{id}:
    parameters:
      - name: id
        in: path
        required: true
    get:
      x-not-implemented: true
      tags:
        - sandboxes
      responses:
        "200":
          description: ok
    post:
      x-not-implemented: true
      tags:
        - sandboxes
      responses:
        "200":
          description: ok
  /sandboxes:
    get:
      tags:
        - sandboxes
      responses:
        "200":
          description: ok
`
		got, removedOps, _, err := filterText("api", input)
		if err != nil {
			t.Fatalf("filterText returned unexpected error: %v", err)
		}
		if removedOps != 0 {
			t.Fatalf("removedOps = %d, want 0", removedOps)
		}
		if strings.Contains(got, "/future/{id}") {
			t.Fatalf("expected empty /future/{id} path item to be pruned, got:\n%s", got)
		}
		if strings.Contains(got, "name: id") {
			t.Fatalf("expected path-level parameters of pruned path to be removed, got:\n%s", got)
		}
		if !strings.Contains(got, "/sandboxes:") {
			t.Fatalf("expected /sandboxes to be preserved, got:\n%s", got)
		}
	})

	t.Run("partial strip keeps path and remaining operation", func(t *testing.T) {
		t.Parallel()

		input := `openapi: 3.0.0
paths:
  /sandboxes:
    post:
      x-not-implemented: true
      tags:
        - sandboxes
      responses:
        "201":
          description: created
    get:
      tags:
        - sandboxes
      responses:
        "200":
          description: ok
`
		got, _, _, err := filterText("api", input)
		if err != nil {
			t.Fatalf("filterText returned unexpected error: %v", err)
		}
		if strings.Contains(got, "post:") {
			t.Fatalf("expected x-not-implemented post operation to be removed, got:\n%s", got)
		}
		if !strings.Contains(got, "/sandboxes:") || !strings.Contains(got, "get:") {
			t.Fatalf("expected /sandboxes get operation to remain, got:\n%s", got)
		}
	})

	t.Run("malformed path without indent-4 operations still errors", func(t *testing.T) {
		t.Parallel()

		input := `openapi: 3.0.0
paths:
  /broken:
      get:
        tags:
          - sandboxes
`
		_, _, _, err := filterText("api", input)
		if err == nil {
			t.Fatal("expected error for malformed path indentation, got nil")
		}
		if !strings.Contains(err.Error(), "has no operations at indent 4") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestBlockEnd_ZeroIndentedSequence(t *testing.T) {
	t.Parallel()

	t.Run("tags sequence at same indent is kept by api filter", func(t *testing.T) {
		t.Parallel()

		input := `openapi: 3.0.0
paths:
  /templates:
    get:
      tags:
      - templates
      summary: List templates
      responses:
        "200":
          description: ok
  /admin:
    get:
      tags:
      - admin
      responses:
        "200":
          description: ok
`
		got, removedOps, _, err := filterText("api", input)
		if err != nil {
			t.Fatalf("filterText returned unexpected error: %v", err)
		}
		if removedOps != 1 {
			t.Fatalf("removedOps = %d, want 1", removedOps)
		}
		if !strings.Contains(got, "/templates:") || !strings.Contains(got, "- templates") {
			t.Fatalf("expected /templates with zero-indented tags sequence to be kept, got:\n%s", got)
		}
		if strings.Contains(got, "/admin:") {
			t.Fatalf("expected /admin operation to be dropped, got:\n%s", got)
		}
	})

	t.Run("blockEnd spans zero-indented sequence items and stops at sibling key", func(t *testing.T) {
		t.Parallel()

		lines := []string{
			"      parameters:",
			"      - name: id",
			"        in: path",
			"      - name: limit",
			"        in: query",
			"      responses:",
		}
		if got := blockEnd(lines, 0); got != 5 {
			t.Fatalf("blockEnd(parameters) = %d, want 5", got)
		}
		// Starting on a sequence item must not swallow the sibling sequence item.
		if got := blockEnd(lines, 1); got != 3 {
			t.Fatalf("blockEnd(- name: id) = %d, want 3", got)
		}
	})
}

func TestStripNotImplemented_IgnoresBlockScalarProse(t *testing.T) {
	t.Parallel()

	input := `openapi: 3.0.0
paths:
  /sandboxes:
    get:
      summary: List sandboxes
      description: |
        This prose mentions x-not-implemented in an example:
        x-not-implemented: true
        # markdown heading inside block scalar
        Still part of description.
      tags:
        - sandboxes
      responses:
        "200":
          description: >-
            Folded block scalar mentioning
            x-not-implemented: true
            which must not delete the response.
    delete:
      description: |2-
        Indented chomped scalar:
        x-not-implemented: true
      x-not-implemented: true
      tags:
        - sandboxes
      responses:
        "204":
          description: deleted
`
	got, _, _, err := filterText("api", input)
	if err != nil {
		t.Fatalf("filterText returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "summary: List sandboxes") {
		t.Fatalf("expected get operation with block scalar prose to survive, got:\n%s", got)
	}
	if !strings.Contains(got, "# markdown heading inside block scalar") {
		t.Fatalf("expected markdown heading inside block scalar to survive, got:\n%s", got)
	}
	if !strings.Contains(got, "Folded block scalar mentioning") {
		t.Fatalf("expected 200 response with folded scalar prose to survive, got:\n%s", got)
	}
	if strings.Contains(got, "delete:") {
		t.Fatalf("expected delete operation with real x-not-implemented: true to be stripped, got:\n%s", got)
	}
}
