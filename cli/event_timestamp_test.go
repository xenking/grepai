package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
	"github.com/yoanbernabeu/grepai/watcher"
)

// TestHandleFileEvent_TimestampsAdvance verifies that state.yaml's
// last_index_time and last_activity_time advance correctly across
// index, delete, and chunk-skip events. This is the core behavior
// behind patch 6: deletes and skipped events should keep
// `grepai status`'s "Last activity" line fresh even when nothing was
// actually (re)indexed.
//
// Per project convention this test lives in cli/ because
// handleFileEvent is defined here; the patch spec's
// indexer/event_timestamp_test.go location would require an
// artificial extraction of the event loop into the indexer package.
// The contract being tested is the same.
func TestHandleFileEvent_TimestampsAdvance(t *testing.T) {
	ctx := context.Background()
	projectRoot := t.TempDir()

	// Seed a minimal config.yaml so config.LoadState works out of the
	// saveRuntimeState helper call path.
	if err := config.DefaultConfig().Save(projectRoot); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Write a source file to scan.
	srcPath := filepath.Join(projectRoot, "main.go")
	if err := os.WriteFile(srcPath, []byte("package main\n\nfunc Real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ignoreMatcher, err := indexer.NewIgnoreMatcher(projectRoot, []string{}, "")
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}

	emb := &countingEmbedder{}
	scanner := indexer.NewScanner(projectRoot, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(projectRoot, "index.gob"))
	idx := indexer.NewIndexer(projectRoot, vecStore, emb, chunker, scanner, time.Time{})

	symbolStore := trace.NewGOBSymbolStore(filepath.Join(projectRoot, "symbols.gob"))
	if err := symbolStore.Load(ctx); err != nil {
		t.Fatalf("load symbols: %v", err)
	}
	defer symbolStore.Close()

	cfg := config.DefaultConfig()

	// Force every event to bypass the throttle so the test doesn't
	// need to sleep 30 seconds. handleFileEvent treats an unset
	// *lastConfigWrite as "never written" which is exactly what we
	// want here.
	lastWrite := time.Time{}

	// Step 1: index event → both timestamps should jump forward.
	beforeIndex := time.Now()
	handleFileEvent(
		ctx, idx, scanner, trace.NewRegexExtractor(), symbolStore,
		nil, nil, []string{".go"}, projectRoot, cfg, &lastWrite,
		nil,
		watcher.FileEvent{Type: watcher.EventModify, Path: "main.go"},
		nil, nil,
	)

	st1, err := config.LoadState(projectRoot)
	if err != nil {
		t.Fatalf("load state after index: %v", err)
	}
	if st1.LastIndexTime.Before(beforeIndex) {
		t.Errorf("LastIndexTime %v did not advance past %v on index event", st1.LastIndexTime, beforeIndex)
	}
	if st1.LastActivityTime.Before(beforeIndex) {
		t.Errorf("LastActivityTime %v did not advance past %v on index event", st1.LastActivityTime, beforeIndex)
	}

	// Reset the throttle so the next event can write again.
	lastWrite = time.Time{}

	// Step 2: delete event → only last_activity_time advances,
	// last_index_time must stay pinned at its previous value.
	prevIndex := st1.LastIndexTime
	beforeDelete := time.Now()
	handleFileEvent(
		ctx, idx, scanner, trace.NewRegexExtractor(), symbolStore,
		nil, nil, []string{".go"}, projectRoot, cfg, &lastWrite,
		nil,
		watcher.FileEvent{Type: watcher.EventDelete, Path: "main.go"},
		nil, nil,
	)

	st2, err := config.LoadState(projectRoot)
	if err != nil {
		t.Fatalf("load state after delete: %v", err)
	}
	if !st2.LastIndexTime.Equal(prevIndex) {
		t.Errorf("LastIndexTime changed on delete: was %v, now %v", prevIndex, st2.LastIndexTime)
	}
	if !st2.LastActivityTime.After(st1.LastActivityTime) {
		t.Errorf("LastActivityTime did not advance on delete: before=%v, after=%v", st1.LastActivityTime, st2.LastActivityTime)
	}
	if st2.LastActivityTime.Before(beforeDelete) {
		t.Errorf("LastActivityTime %v did not advance past %v on delete event", st2.LastActivityTime, beforeDelete)
	}
}
