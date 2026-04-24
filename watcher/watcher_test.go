package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yoanbernabeu/grepai/indexer"
)

func newTestWatcher(t *testing.T, root string) *Watcher {
	t.Helper()

	matcher, err := indexer.NewIgnoreMatcher(root, nil, "")
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}

	w, err := NewWatcher(root, matcher, 50)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// TestAtomicRewatch_RearmsAfterDeleteCreate verifies that a watched directory
// which emits a DELETE followed by a CREATE within the re-arm window is
// re-registered on the new inode (issue #225). The test doesn't rely on the
// real OS producing the event sequence - it drives the internal
// scheduleRewatch / rearmAfterCreate hooks directly so we can assert the
// bookkeeping without flaky filesystem timing.
func TestAtomicRewatch_RearmsAfterDeleteCreate(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "pkg")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := newTestWatcher(t, root)

	// Simulate the initial scan having registered the watch on 'pkg'.
	w.rememberDir(subdir)
	if !w.isWatchedDir(subdir) {
		t.Fatalf("rememberDir did not record %s", subdir)
	}

	// Simulate a DELETE event: the atomic-rewatch timer should be armed.
	w.scheduleRewatch(subdir)

	w.dirMu.Lock()
	_, pending := w.pendingRewatch[subdir]
	w.dirMu.Unlock()
	if !pending {
		t.Fatalf("scheduleRewatch did not arm a pending timer for %s", subdir)
	}

	// Simulate a CREATE event landing within the 500ms window.
	if !w.rearmAfterCreate(subdir) {
		t.Fatalf("rearmAfterCreate returned false; expected re-arm")
	}

	// After re-arm, the pending timer should be cleared and the directory
	// should still be tracked.
	w.dirMu.Lock()
	_, stillPending := w.pendingRewatch[subdir]
	_, stillWatched := w.watchedDirs[subdir]
	w.dirMu.Unlock()
	if stillPending {
		t.Fatal("pending timer was not cleared after successful re-arm")
	}
	if !stillWatched {
		t.Fatal("directory was forgotten after successful re-arm")
	}
}

// TestAtomicRewatch_ForgetsAfterWindow ensures that if the CREATE never
// arrives within the configured window, the directory is dropped from the
// watched set (no stale bookkeeping).
func TestAtomicRewatch_ForgetsAfterWindow(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "pkg")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := newTestWatcher(t, root)
	w.rewatchInterval = 20 * time.Millisecond // keep the test fast

	w.rememberDir(subdir)

	// Now remove the directory from disk so that any rearm attempt would
	// fail, and schedule the rewatch as if a DELETE had just arrived.
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatalf("remove subdir: %v", err)
	}
	w.scheduleRewatch(subdir)

	// Wait long enough for the timer to fire.
	time.Sleep(80 * time.Millisecond)

	w.dirMu.Lock()
	_, pending := w.pendingRewatch[subdir]
	_, stillWatched := w.watchedDirs[subdir]
	w.dirMu.Unlock()

	if pending {
		t.Fatal("pending timer was not cleared after window elapsed")
	}
	if stillWatched {
		t.Fatal("directory should have been forgotten after window elapsed")
	}
}

// TestHandleEventArmsAndClearsRewatch exercises handleEvent directly to prove
// that the DELETE/CREATE branches wire through to the atomic-rewatch state
// machine. We avoid the real fsnotify loop: the intention is purely to check
// our own dispatch logic, not fsnotify's.
func TestHandleEventArmsAndClearsRewatch(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "pkg")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := newTestWatcher(t, root)
	w.rewatchInterval = time.Second
	w.rememberDir(subdir)

	// Remove the directory from disk to mimic an atomic write's initial
	// unlink (fsnotify's OS layer has already emitted the Remove event by
	// the time we handle it).
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatalf("remove subdir: %v", err)
	}
	w.handleEvent(fsnotify.Event{Name: subdir, Op: fsnotify.Remove})

	w.dirMu.Lock()
	_, pending := w.pendingRewatch[subdir]
	w.dirMu.Unlock()
	if !pending {
		t.Fatal("handleEvent(Remove) did not arm the rewatch timer")
	}

	// Recreate the directory and dispatch a CREATE event, which should
	// re-arm the watch and clear the pending timer.
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("recreate subdir: %v", err)
	}
	w.handleEvent(fsnotify.Event{Name: subdir, Op: fsnotify.Create})

	w.dirMu.Lock()
	_, stillPending := w.pendingRewatch[subdir]
	_, stillWatched := w.watchedDirs[subdir]
	w.dirMu.Unlock()

	if stillPending {
		t.Fatal("CREATE event did not cancel the pending rewatch timer")
	}
	if !stillWatched {
		t.Fatal("CREATE event did not restore the watched-directory record")
	}
}
