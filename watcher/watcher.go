package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yoanbernabeu/grepai/indexer"
)

type EventType int

const (
	EventCreate EventType = iota
	EventModify
	EventDelete
	EventRename
)

type FileEvent struct {
	Type EventType
	Path string
}

// atomicDirRewatchWindow is how long we wait for a CREATE after a DELETE on a
// watched directory before giving up and concluding the directory is really
// gone. Many editors (Claude Code, VSCode, JetBrains) do a write-rename that
// atomically replaces a directory inode; fsnotify drops the watch on the old
// inode, so we need a short grace period to re-arm it on the new one. See
// issue #225.
const atomicDirRewatchWindow = 500 * time.Millisecond

type Watcher struct {
	root       string
	watcher    *fsnotify.Watcher
	ignore     *indexer.IgnoreMatcher
	debounceMs int
	events     chan FileEvent
	done       chan struct{}

	// Debouncing state
	pending   map[string]FileEvent
	pendingMu sync.Mutex
	timer     *time.Timer

	// Atomic-write re-arm state (issue #225). watchedDirs tracks every
	// directory path we have successfully Add()ed; pendingRewatch records
	// directories that emitted a DELETE/RENAME and are awaiting a matching
	// CREATE within atomicDirRewatchWindow.
	dirMu           sync.Mutex
	watchedDirs     map[string]struct{}
	pendingRewatch  map[string]*time.Timer
	rewatchInterval time.Duration
}

func NewWatcher(root string, ignore *indexer.IgnoreMatcher, debounceMs int) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		root:            root,
		watcher:         fsw,
		ignore:          ignore,
		debounceMs:      debounceMs,
		events:          make(chan FileEvent, 100),
		done:            make(chan struct{}),
		pending:         make(map[string]FileEvent),
		watchedDirs:     make(map[string]struct{}),
		pendingRewatch:  make(map[string]*time.Timer),
		rewatchInterval: atomicDirRewatchWindow,
	}, nil
}

func (w *Watcher) Start(ctx context.Context) error {
	// Add root directory and all subdirectories
	if err := w.addRecursive(w.root); err != nil {
		return err
	}

	// Start event processing
	go w.processEvents(ctx)

	return nil
}

func (w *Watcher) Events() <-chan FileEvent {
	return w.events
}

func (w *Watcher) Close() error {
	close(w.done)
	// Cancel any in-flight rewatch timers so we don't leak goroutines.
	w.dirMu.Lock()
	for path, t := range w.pendingRewatch {
		t.Stop()
		delete(w.pendingRewatch, path)
	}
	w.dirMu.Unlock()
	return w.watcher.Close()
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		relPath, err := filepath.Rel(w.root, path)
		if err != nil {
			return nil
		}

		// Handle directories: use ShouldSkipDir to respect .grepaiignore negations
		if info.IsDir() {
			if w.ignore.ShouldSkipDir(relPath) {
				return filepath.SkipDir
			}
			// Directory is not skipped; watch it if not individually ignored
			if !w.ignore.ShouldIgnore(relPath) {
				if err := w.watcher.Add(path); err != nil {
					log.Printf("Failed to watch %s: %v", path, err)
				} else {
					w.rememberDir(path)
				}
			}
			return nil
		}

		// Skip ignored files
		if w.ignore.ShouldIgnore(relPath) {
			return nil
		}

		return nil
	})
}

// rememberDir records that we successfully registered a watch on path.
func (w *Watcher) rememberDir(path string) {
	w.dirMu.Lock()
	w.watchedDirs[path] = struct{}{}
	if t, ok := w.pendingRewatch[path]; ok {
		t.Stop()
		delete(w.pendingRewatch, path)
	}
	w.dirMu.Unlock()
}

// isWatchedDir reports whether path was previously registered via rememberDir.
func (w *Watcher) isWatchedDir(path string) bool {
	w.dirMu.Lock()
	_, ok := w.watchedDirs[path]
	w.dirMu.Unlock()
	return ok
}

// forgetDir is used when we've concluded a directory is truly gone (the
// re-arm window expired without a matching CREATE).
func (w *Watcher) forgetDir(path string) {
	w.dirMu.Lock()
	delete(w.watchedDirs, path)
	delete(w.pendingRewatch, path)
	w.dirMu.Unlock()
}

// scheduleRewatch arms a timer: if a matching CREATE for path does not arrive
// within rewatchInterval, we treat the directory as permanently gone and stop
// tracking it. If CREATE does arrive (via rearmAfterCreate), the timer is
// cancelled and the watch is re-registered on the new inode.
func (w *Watcher) scheduleRewatch(path string) {
	w.dirMu.Lock()
	if existing, ok := w.pendingRewatch[path]; ok {
		existing.Stop()
	}
	w.pendingRewatch[path] = time.AfterFunc(w.rewatchInterval, func() {
		w.forgetDir(path)
	})
	w.dirMu.Unlock()
}

// rearmAfterCreate re-registers a watch on path if it was previously watched
// and is currently in the atomic-rewatch window. Returns true when a rewatch
// was performed.
func (w *Watcher) rearmAfterCreate(path string) bool {
	w.dirMu.Lock()
	_, pending := w.pendingRewatch[path]
	_, wasWatched := w.watchedDirs[path]
	w.dirMu.Unlock()

	if !pending && !wasWatched {
		return false
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}

	if err := w.addRecursive(path); err != nil {
		log.Printf("Failed to re-arm watch on %s after atomic write: %v", path, err)
		return false
	}
	log.Printf("Re-armed watch on %s after atomic-write cycle", path)
	return true
}

func (w *Watcher) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	// First, handle directory re-arm semantics (issue #225). We intentionally
	// do this before the hidden/ignored filtering below, because some editors
	// use atomic-write on directories whose basename starts with "." (for
	// example, rename-to-target with a ".tmp" dotdir).
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		if w.isWatchedDir(event.Name) {
			w.scheduleRewatch(event.Name)
		}
	}
	if event.Has(fsnotify.Create) {
		if w.rearmAfterCreate(event.Name) {
			// Note: we still fall through so the CREATE is emitted as a
			// normal FileEvent for any file within the directory's walk.
		}
	}

	relPath, err := filepath.Rel(w.root, event.Name)
	if err != nil {
		return
	}

	// Ignore hidden files and ignored paths
	if strings.HasPrefix(filepath.Base(relPath), ".") {
		return
	}
	if w.ignore.ShouldIgnore(relPath) {
		return
	}

	// Check if it's a supported file
	ext := strings.ToLower(filepath.Ext(event.Name))
	if !indexer.SupportedExtensions[ext] {
		// Check if it's a directory (for watching new directories)
		info, err := os.Stat(event.Name)
		if err != nil || !info.IsDir() {
			return
		}

		// New directory created, add to watcher
		if event.Has(fsnotify.Create) {
			if err := w.addRecursive(event.Name); err != nil {
				log.Printf("Failed to add new directory %s: %v", event.Name, err)
			}
		}
		return
	}

	var evType EventType
	switch {
	case event.Has(fsnotify.Create):
		evType = EventCreate
	case event.Has(fsnotify.Write):
		evType = EventModify
	case event.Has(fsnotify.Remove):
		evType = EventDelete
	case event.Has(fsnotify.Rename):
		evType = EventRename
	default:
		return
	}

	w.debounceEvent(FileEvent{
		Type: evType,
		Path: relPath,
	})
}

func (w *Watcher) debounceEvent(event FileEvent) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()

	// Merge events: delete > create/modify
	existing, exists := w.pending[event.Path]
	if exists && existing.Type == EventDelete && event.Type != EventDelete {
		// Keep delete if file was deleted then recreated quickly
		// This will be handled as delete + create
	} else {
		w.pending[event.Path] = event
	}

	// Reset timer
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(time.Duration(w.debounceMs)*time.Millisecond, w.flush)
}

func (w *Watcher) flush() {
	w.pendingMu.Lock()
	events := make([]FileEvent, 0, len(w.pending))
	for _, event := range w.pending {
		events = append(events, event)
	}
	w.pending = make(map[string]FileEvent)
	w.pendingMu.Unlock()

	for _, event := range events {
		select {
		case w.events <- event:
		default:
			log.Printf("Event channel full, dropping event for %s", event.Path)
		}
	}
}

func (e EventType) String() string {
	switch e {
	case EventCreate:
		return "CREATE"
	case EventModify:
		return "MODIFY"
	case EventDelete:
		return "DELETE"
	case EventRename:
		return "RENAME"
	default:
		return "UNKNOWN"
	}
}
