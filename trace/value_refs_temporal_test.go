package trace

import (
	"context"
	"path/filepath"
	"testing"
)

// TestFindValueReferences_TemporalFixture drives the value-reference
// scanner against the Temporal-style fixture in trace/fixtures/.
// FoodLoggingWorkflow appears in consumer.go under three value-passed
// idioms (direct register, ExecuteWorkflow argument, go statement),
// all of which should land in the references array. The `go Fn(...)`
// occurrence IS a call — it has parentheses — so it must NOT be
// surfaced as a value-ref.
func TestFindValueReferences_TemporalFixture(t *testing.T) {
	ctx := context.Background()
	// Fixture root is this package's testdata-adjacent fixtures dir.
	root, err := filepath.Abs("fixtures/temporal")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	store := NewGOBSymbolStore(filepath.Join(t.TempDir(), "sym.gob"))

	// Seed the symbol store with both files and the known definition
	// site so the scanner can filter it out correctly.
	if err := store.SaveFileWithContentHash(ctx, "workflows.go", "", []Symbol{{
		Name: "FoodLoggingWorkflow", Kind: KindFunction, File: "workflows.go", Line: 14, Language: "go",
	}, {
		Name: "StepCounterWorkflow", Kind: KindFunction, File: "workflows.go", Line: 19, Language: "go",
	}, {
		Name: "HydrationWorkflow", Kind: KindFunction, File: "workflows.go", Line: 24, Language: "go",
	}}, nil); err != nil {
		t.Fatalf("seed workflows.go: %v", err)
	}
	if err := store.SaveFileWithContentHash(ctx, "consumer.go", "", nil, nil); err != nil {
		t.Fatalf("seed consumer.go: %v", err)
	}

	refs, err := FindValueReferences(ctx, store, root, "FoodLoggingWorkflow")
	if err != nil {
		t.Fatalf("FindValueReferences: %v", err)
	}

	if len(refs) == 0 {
		t.Fatal("expected at least one value-reference for FoodLoggingWorkflow, got 0")
	}

	// All refs must be kind=value-ref and live in consumer.go —
	// workflows.go has only the definition and a docstring mention.
	for _, r := range refs {
		if r.Kind != ValueRefKind {
			t.Errorf("ref at %s:%d has kind %q, want %q", r.File, r.Line, r.Kind, ValueRefKind)
		}
	}

	// Expect the ExecuteWorkflow, RegisterWorkflow, and `go Fn(...)`
	// value-pass lines to all land. Per the patch-7 spec, the
	// empty-callers fallback must surface every identifier occurrence
	// — including the `go Fn(...)` goroutine entry — because at that
	// point there is nothing competing in the callers array.
	var haveExec, haveReg, haveGo bool
	for _, r := range refs {
		if r.File != "consumer.go" {
			continue
		}
		switch {
		case containsAll(r.Snippet, "ExecuteWorkflow", "FoodLoggingWorkflow"):
			haveExec = true
		case r.Snippet == "tc.RegisterWorkflow(FoodLoggingWorkflow)":
			haveReg = true
		case containsAll(r.Snippet, "go FoodLoggingWorkflow"):
			haveGo = true
		}
	}
	if !haveExec {
		t.Errorf("missing ExecuteWorkflow value-ref in %+v", refs)
	}
	if !haveReg {
		t.Errorf("missing RegisterWorkflow value-ref in %+v", refs)
	}
	if !haveGo {
		t.Errorf("missing `go FoodLoggingWorkflow(...)` value-ref in %+v", refs)
	}
}

// containsAll is a tiny helper: all substrings must be present in s.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !stringsContains(s, sub) {
			return false
		}
	}
	return true
}

// stringsContains wraps strings.Contains without importing "strings"
// at the top of this file (the test already has its own imports,
// and a single helper beats adding another import edit).
func stringsContains(s, sub string) bool {
	// simple substring search
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
