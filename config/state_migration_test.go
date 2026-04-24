package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStateMigration_LegacyLastIndexTime verifies that legacy configs
// still carrying watch.last_index_time have the field migrated into
// state.yaml on Load, and that the next Save rewrites config.yaml
// without the migrated field. The migration must be idempotent —
// reloading after migration should be a no-op.
func TestStateMigration_LegacyLastIndexTime(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ConfigDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := GetConfigPath(tmpDir)

	legacy := `version: 1
embedder:
  provider: ollama
  model: nomic-embed-text
store:
  backend: gob
chunking:
  size: 512
  overlap: 50
watch:
  debounce_ms: 500
  last_index_time: 2026-04-18T17:35:45+10:00
`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// In-memory view should still carry the time so existing callers
	// that read cfg.Watch.LastIndexTime keep working.
	if cfg.Watch.LastIndexTime.IsZero() {
		t.Fatal("expected cfg.Watch.LastIndexTime to be populated from legacy field")
	}
	expected, _ := time.Parse(time.RFC3339, "2026-04-18T17:35:45+10:00")
	if !cfg.Watch.LastIndexTime.Equal(expected) {
		t.Errorf("LastIndexTime = %v, want %v", cfg.Watch.LastIndexTime, expected)
	}

	// state.yaml should now exist with the migrated value.
	statePath := GetStatePath(tmpDir)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state.yaml missing after migration: %v", err)
	}
	state, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !state.LastIndexTime.Equal(expected) {
		t.Errorf("state.LastIndexTime = %v, want %v", state.LastIndexTime, expected)
	}

	// Save must strip the legacy field from config.yaml.
	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rewritten, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read rewritten: %v", err)
	}
	if strings.Contains(string(rewritten), "last_index_time") {
		t.Errorf("config.yaml still contains last_index_time after migration:\n%s", string(rewritten))
	}

	// Idempotence: a fresh load+save of the migrated file must be a
	// byte-identical round-trip (no further drift).
	cfg2, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := cfg2.Save(tmpDir); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	final, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(final) != string(rewritten) {
		t.Errorf("post-migration save is not idempotent:\n--- after migration ---\n%s\n--- after re-save ---\n%s", string(rewritten), string(final))
	}
}
