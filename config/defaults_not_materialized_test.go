package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultsNotMaterialized verifies that loading a minimal config
// and re-saving it does NOT materialize default sections into the
// file. Specifically: framework_processing, search.dedup, and a blank
// embedder.endpoint must remain absent after a no-edit round-trip.
//
// Patch 5 invariant: the daemon and library code applies defaults
// in-memory only; the user's file is authoritative about what it
// contains on disk.
func TestDefaultsNotMaterialized(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ConfigDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := GetConfigPath(tmpDir)

	// Minimal config for each provider that has a provider-native
	// default endpoint. The blank "endpoint:" omission must not be
	// rewritten back into the file.
	minimal := `version: 1
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
`
	if err := os.WriteFile(cfgPath, []byte(minimal), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The in-memory view should still have the provider default
	// endpoint resolved, so the embedder factory keeps working.
	if cfg.Embedder.Endpoint == "" {
		t.Error("expected in-memory endpoint to resolve to provider default")
	}

	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(got)

	forbidden := []struct {
		name   string
		needle string
	}{
		{"framework_processing section", "framework_processing:"},
		{"search section", "search:"},
		{"search.dedup section", "dedup:"},
		{"provider-default endpoint", "endpoint: http://localhost:11434"},
	}
	for _, f := range forbidden {
		if strings.Contains(content, f.needle) {
			t.Errorf("%s was materialized into config.yaml after round-trip:\n%s", f.name, content)
		}
	}

	// Byte-identity: the round-trip must not change anything.
	if content != minimal {
		t.Errorf("round-trip drifted from original:\n--- want ---\n%s\n--- got ---\n%s", minimal, content)
	}
}
