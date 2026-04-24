package trace

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ValueRefKind is the Kind emitted for non-call identifier references
// (value-pass callsites, handler registrations, dispatch maps, etc.).
const ValueRefKind = "value-ref"

// FindValueReferences scans files tracked by the given symbol store
// for bare occurrences of symbolName that are NOT call sites. It is
// intended as a fallback surface when LookupCallers returns zero
// results — common for value-passed callbacks such as
// `ExecuteWorkflow(ctx, MyWorkflow, ...)`, `go Fn()`, or handler maps.
//
// The scanner:
//   - re-reads each file from disk relative to projectRoot;
//   - skips lines matching definitions of the target symbol
//     (so the target's own def doesn't show up as a reference);
//   - skips occurrences immediately followed by `(` (those are
//     already direct calls and would have been caught upstream);
//   - skips occurrences inside single-line comments (best-effort);
//   - resolves a best-effort enclosing-function label by scanning
//     backwards for the closest function-def line in the file.
//
// Results are returned in stable order: by file path, then line.
func FindValueReferences(ctx context.Context, store *GOBSymbolStore, projectRoot, symbolName string) ([]ReferenceInfo, error) {
	if store == nil || symbolName == "" {
		return nil, nil
	}

	// Build a quick set of definition sites for the target symbol so
	// we can filter them out. A value-ref on the same line as its
	// definition is nonsensical.
	defSites := make(map[string]map[int]struct{})
	defs, err := store.LookupSymbol(ctx, symbolName)
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		byLine, ok := defSites[def.File]
		if !ok {
			byLine = make(map[int]struct{})
			defSites[def.File] = byLine
		}
		byLine[def.Line] = struct{}{}
	}

	// Similarly, track which file:line pairs already appear as
	// direct call references. We never want a call-site to masquerade
	// as a value-ref in the output.
	callSites := make(map[string]map[int]struct{})
	callRefs, err := store.LookupCallers(ctx, symbolName)
	if err != nil {
		return nil, err
	}
	for _, r := range callRefs {
		byLine, ok := callSites[r.File]
		if !ok {
			byLine = make(map[int]struct{})
			callSites[r.File] = byLine
		}
		byLine[r.Line] = struct{}{}
	}

	needle := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbolName) + `\b`)

	files := store.ListIndexedFiles()
	var out []ReferenceInfo
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		abs := filepath.Join(projectRoot, rel)
		content, err := os.ReadFile(abs)
		if err != nil {
			// Missing or unreadable files are non-fatal. The index
			// may be stale and we should degrade gracefully.
			continue
		}
		out = append(out, scanFileForValueRefs(rel, string(content), needle, defSites[rel], callSites[rel])...)
	}

	sortReferences(out)
	return out, nil
}

// scanFileForValueRefs is the file-level workhorse for
// FindValueReferences. It is exposed (package-private) so that unit
// tests can drive it directly without disk I/O.
func scanFileForValueRefs(file, content string, needle *regexp.Regexp, defLines, callLines map[int]struct{}) []ReferenceInfo {
	if needle == nil {
		return nil
	}
	lines := strings.Split(content, "\n")
	var out []ReferenceInfo
	for i, raw := range lines {
		// Strip any //... trailing comment and also skip whole-line
		// comments. We intentionally over-approximate — a value-ref
		// inside an inline comment is not a real call site and is
		// better left out than surfaced as noise.
		line := stripLineCommentsAndWhole(raw)
		if line == "" {
			continue
		}
		matches := needle.FindAllStringIndex(line, -1)
		if len(matches) == 0 {
			continue
		}
		lineNum := i + 1
		if _, ok := defLines[lineNum]; ok {
			continue
		}
		if _, ok := callLines[lineNum]; ok {
			continue
		}
		// When the caller already has direct call-sites to show, we
		// filter those out so we never conflate kinds. In the
		// empty-callers fallback path (callLines nil/empty), we
		// surface every occurrence instead — this is what the spec
		// asks for so that `go Fn()` and friends appear as refs when
		// direct callers yielded zero.
		for _, m := range matches {
			if len(callLines) > 0 && isDirectCallOccurrence(line, m[1]) {
				continue
			}
			out = append(out, ReferenceInfo{
				File:      file,
				Line:      lineNum,
				Enclosing: findEnclosingName(lines, i),
				Snippet:   strings.TrimSpace(raw),
				Kind:      ValueRefKind,
			})
			break // one entry per line is plenty; avoid noise
		}
	}
	return out
}

// isDirectCallOccurrence reports whether position `end` in line is
// immediately followed (possibly past whitespace) by an opening
// parenthesis — i.e. the occurrence is a real function call and
// should NOT be reported as a value-ref.
func isDirectCallOccurrence(line string, end int) bool {
	for i := end; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t':
			continue
		case '(':
			return true
		default:
			return false
		}
	}
	return false
}

// stripLineCommentsAndWhole drops the line entirely when the first
// non-whitespace characters are a `//` or `#` comment marker, and
// strips the trailing `//…` portion otherwise (outside string
// literals — cheap heuristic). Kept intentionally simple; the
// value-ref scanner is a hint for humans/LLMs, not a compiler.
func stripLineCommentsAndWhole(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	if idx := strings.Index(line, "//"); idx >= 0 && strings.Count(line[:idx], `"`)%2 == 0 {
		line = line[:idx]
	}
	return line
}

// findEnclosingName walks backwards from `line` looking for the
// closest `func <Name>`, `def <Name>`, or `fn <Name>` style line
// header. Empty string when nothing matches — callers treat it as
// "top-level".
var enclosingRe = regexp.MustCompile(
	`^\s*(?:func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)` +
		`|def\s+([A-Za-z_][A-Za-z0-9_]*)` +
		`|fn\s+([A-Za-z_][A-Za-z0-9_]*)` +
		`|function\s+([A-Za-z_][A-Za-z0-9_]*))`,
)

func findEnclosingName(lines []string, at int) string {
	for i := at; i >= 0; i-- {
		m := enclosingRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		for _, g := range m[1:] {
			if g != "" {
				return g
			}
		}
	}
	return ""
}

// sortReferences orders refs by file then line for stable output.
// stdlib sort import kept local to this file.
func sortReferences(refs []ReferenceInfo) {
	if len(refs) < 2 {
		return
	}
	// tiny insertion sort — value-ref output sets are small (tens),
	// and avoiding the "sort" package keeps the trace module lean.
	for i := 1; i < len(refs); i++ {
		cur := refs[i]
		j := i - 1
		for j >= 0 && lessReference(cur, refs[j]) {
			refs[j+1] = refs[j]
			j--
		}
		refs[j+1] = cur
	}
}

func lessReference(a, b ReferenceInfo) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}
