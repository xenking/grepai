package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigRoundtrip_ByteIdentical verifies that loading a minimal
// user-authored config.yaml and saving it without any in-memory edits
// produces a byte-identical file. This is the core invariant behind
// patch 5: the user's file is NEVER rewritten unless they (or the
// caller) actually change a user-facing field. Runtime state belongs
// in state.yaml.
func TestConfigRoundtrip_ByteIdentical(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "minimal ollama config",
			yaml: `version: 1
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
`,
		},
		{
			name: "config with explicit endpoint keeps it",
			yaml: `version: 1
embedder:
  provider: openai
  model: text-embedding-3-small
  endpoint: https://my-proxy.example.com/v1
  api_key: sk-test
store:
  backend: gob
chunking:
  size: 512
  overlap: 50
watch:
  debounce_ms: 500
`,
		},
		{
			name: "config with no framework_processing block",
			yaml: `version: 1
embedder:
  provider: voyageai
  model: voyage-code-3
  api_key: vak-test
store:
  backend: gob
chunking:
  size: 512
  overlap: 50
watch:
  debounce_ms: 500
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(tmpDir, ConfigDir), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := GetConfigPath(tmpDir)
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			cfg, err := Load(tmpDir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := cfg.Save(tmpDir); err != nil {
				t.Fatalf("Save: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(got) != tt.yaml {
				t.Errorf("round-trip mismatch:\n--- want ---\n%s\n--- got ---\n%s", tt.yaml, string(got))
			}
		})
	}
}
