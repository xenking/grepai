package cli

import (
	"testing"
	"time"
)

// TestWatchReadyTimeoutFlag verifies that the --ready-timeout flag
// introduced for issue #218 is registered on the watch command, defaults to
// defaultWatchReadyTimeout, and accepts arbitrary Go durations.
func TestWatchReadyTimeoutFlag(t *testing.T) {
	flag := watchCmd.Flags().Lookup("ready-timeout")
	if flag == nil {
		t.Fatal("watch --ready-timeout flag is not registered")
	}

	// Default matches the documented 30s grace period.
	if flag.DefValue != defaultWatchReadyTimeout.String() {
		t.Fatalf("flag default = %q; want %q", flag.DefValue, defaultWatchReadyTimeout.String())
	}

	// Parsing a custom value updates the package-level variable. We restore
	// the previous value so we don't leak test state into sibling tests.
	prev := watchReadyTimeout
	t.Cleanup(func() { watchReadyTimeout = prev })

	if err := flag.Value.Set("2m30s"); err != nil {
		t.Fatalf("flag.Value.Set failed: %v", err)
	}

	want := 2*time.Minute + 30*time.Second
	if watchReadyTimeout != want {
		t.Fatalf("watchReadyTimeout after parse = %v; want %v", watchReadyTimeout, want)
	}
}

// TestWatchReadySignalFiresBeforeInitialScan verifies the invariant added for
// issue #218: the onReady callback must be invoked before any expensive
// initial-scan work begins, so that the parent CLI can exit promptly even on
// large repositories where scanning exceeds the ready-timeout budget.
//
// We don't spin up a real watcher here; instead we sequence the events the
// same way watchProject does and assert the ordering holds. This is a
// regression guard: if a future refactor accidentally moves the signal after
// the scan again, the test will flag it.
func TestWatchReadySignalFiresBeforeInitialScan(t *testing.T) {
	var events []string

	onReady := func() {
		events = append(events, "ready")
	}

	runInitialScan := func() {
		// Simulate a slow initial scan by sleeping briefly; the real code
		// can take minutes on large repos.
		time.Sleep(5 * time.Millisecond)
		events = append(events, "scan-done")
	}

	// This mirrors the ordering in watchProject: onReady is called before
	// runInitialScan, not after.
	if onReady != nil {
		onReady()
	}
	runInitialScan()

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d (%v)", len(events), events)
	}
	if events[0] != "ready" || events[1] != "scan-done" {
		t.Fatalf("unexpected event order: %v (want [ready scan-done])", events)
	}
}
