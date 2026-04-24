package trace

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestScanFileForValueRefs exercises the per-file scanner directly so
// we avoid disk I/O in the fast tests. Covers the three patterns
// called out in patch 7:
//   - `go Fn()` — bare identifier used as goroutine entry
//   - `tc.Execute(ctx, Fn)` — Temporal-style value argument
//   - `handlers["x"] = Fn` — dispatch-map registration
//
// The scanner runs only when direct callers yielded zero, so it must
// surface all three — including the `go Fn()` entry whose parens
// would otherwise mark it as a direct call. This is the empty-callers
// fallback path; spec requires all three land in references.
func TestScanFileForValueRefs(t *testing.T) {
	content := `package worker

import "context"

// Fn is the target symbol.
func Fn(ctx context.Context) error { return nil }

var handlers = map[string]func(ctx context.Context) error{}

func init() {
	go Fn()
	handlers["x"] = Fn
}

func Register(tc TemporalClient, ctx context.Context) {
	tc.Execute(ctx, Fn)
}
`
	needle := regexp.MustCompile(`\bFn\b`)
	defLines := map[int]struct{}{6: {}} // func Fn(...) definition
	// Pass nil callLines to simulate the zero-direct-callers
	// fallback path — every occurrence should be surfaced.
	refs := scanFileForValueRefs("worker/fn.go", content, needle, defLines, nil)

	lineKinds := make(map[int]string)
	for _, r := range refs {
		if r.Kind != ValueRefKind {
			t.Errorf("unexpected kind %q at %s:%d", r.Kind, r.File, r.Line)
		}
		lineKinds[r.Line] = r.Enclosing
	}

	// All three patterns must appear in references.
	for _, want := range []int{11, 12, 16} {
		if _, ok := lineKinds[want]; !ok {
			t.Errorf("line %d not detected as value-ref: %+v", want, refs)
		}
	}

	// Enclosing function resolution sanity check.
	if got := lineKinds[12]; got != "init" {
		t.Errorf("enclosing for line 12 = %q, want init", got)
	}
	if got := lineKinds[16]; got != "Register" {
		t.Errorf("enclosing for line 16 = %q, want Register", got)
	}
}

// TestScanFileForValueRefs_WithCallLines verifies that when the
// scanner DOES know about direct call-sites (callLines populated), it
// filters them out to avoid duplicate/confusing entries. This exercise
// path is not used today by trace callers (the scanner only runs on
// empty callers) but the contract is here for future callers that
// might want a true "only value-refs" view.
func TestScanFileForValueRefs_WithCallLines(t *testing.T) {
	content := `package worker

func Fn() {}

func use() {
    Fn()          // line 6: direct call (should filter)
    _ = Fn        // line 7: value-ref (should keep)
}
`
	needle := regexp.MustCompile(`\bFn\b`)
	defLines := map[int]struct{}{3: {}}
	callLines := map[int]struct{}{6: {}}
	refs := scanFileForValueRefs("a.go", content, needle, defLines, callLines)

	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (line 7), got %d: %+v", len(refs), refs)
	}
	if refs[0].Line != 7 {
		t.Errorf("expected value-ref on line 7, got %d", refs[0].Line)
	}
}

// TestScanFileForValueRefs_SkipsComments guards comment-only and
// whole-line-comment false positives. Call-site filtering is
// covered in TestScanFileForValueRefs_WithCallLines instead, because
// the empty-callers fallback intentionally surfaces direct calls too
// (there is nothing in result.Callers competing with them).
func TestScanFileForValueRefs_SkipsComments(t *testing.T) {
	content := strings.Join([]string{
		"// MyWorkflow docs",
		"func MyWorkflow() {}",
		"// inline MyWorkflow comment",
		"var _ = MyWorkflow",
	}, "\n")
	needle := regexp.MustCompile(`\bMyWorkflow\b`)
	defLines := map[int]struct{}{2: {}}
	refs := scanFileForValueRefs("a.go", content, needle, defLines, nil)

	// Lines 1 and 3 are whole-line comments — they must not appear.
	// Line 2 is the definition (filtered via defLines). Only line 4
	// (`var _ = MyWorkflow`) is a real value-ref.
	if len(refs) != 1 {
		t.Fatalf("expected exactly 1 value-ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].Line != 4 {
		t.Errorf("expected ref on line 4, got %d", refs[0].Line)
	}
}

// TestFindValueReferences_Integration verifies the disk-backed path:
// seed a symbol store, drop two fixture files on disk, run
// FindValueReferences, and confirm the dispatch-map registration is
// surfaced while the direct call is not.
func TestFindValueReferences_Integration(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	// Fixture 1: defines the target.
	defFile := filepath.Join(tmp, "wf.go")
	if err := os.WriteFile(defFile, []byte(strings.Join([]string{
		"package workflows",
		"",
		"// FoodLoggingWorkflow is the target.",
		"func FoodLoggingWorkflow() error { return nil }",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}

	// Fixture 2: registers it as a value.
	regFile := filepath.Join(tmp, "register.go")
	if err := os.WriteFile(regFile, []byte(strings.Join([]string{
		"package main",
		"",
		"func register(tc TemporalClient) {",
		"    tc.RegisterWorkflow(FoodLoggingWorkflow)",
		"}",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write reg: %v", err)
	}

	store := NewGOBSymbolStore(filepath.Join(tmp, "sym.gob"))
	if err := store.SaveFileWithContentHash(ctx, "wf.go", "hash1", []Symbol{{
		Name: "FoodLoggingWorkflow", Kind: KindFunction, File: "wf.go", Line: 4, Language: "go",
	}}, nil); err != nil {
		t.Fatalf("save def: %v", err)
	}
	if err := store.SaveFileWithContentHash(ctx, "register.go", "hash2", nil, nil); err != nil {
		t.Fatalf("save reg: %v", err)
	}

	refs, err := FindValueReferences(ctx, store, tmp, "FoodLoggingWorkflow")
	if err != nil {
		t.Fatalf("FindValueReferences: %v", err)
	}

	// Expect exactly one ref from register.go line 4. The definition
	// line in wf.go must not appear.
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].File != "register.go" || refs[0].Line != 4 {
		t.Errorf("unexpected location: %+v", refs[0])
	}
	if refs[0].Kind != ValueRefKind {
		t.Errorf("unexpected kind: %q", refs[0].Kind)
	}
	if refs[0].Enclosing != "register" {
		t.Errorf("enclosing = %q, want register", refs[0].Enclosing)
	}
}
