package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveEmbedderAPIKey_prefers_project_key(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := ResolveEmbedderAPIKey("voyageai", "project-key"); got != "project-key" {
		t.Fatalf("expected project key, got %q", got)
	}
}

func TestResolveEmbedderAPIKey_reads_global_config(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".grepai")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.yaml"), []byte("api_keys:\n  voyageai: global-key\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	if got := ResolveEmbedderAPIKey("voyageai", ""); got != "global-key" {
		t.Fatalf("expected global key, got %q", got)
	}
}

func TestResolveEmbedderAPIKey_expandsEnvironmentReferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VOYAGE_API_KEY", "env-key")
	globalDir := filepath.Join(home, ".grepai")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.yaml"), []byte("api_keys:\n  voyageai: ${VOYAGE_API_KEY}\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	if got := ResolveEmbedderAPIKey("voyageai", ""); got != "env-key" {
		t.Fatalf("expected expanded env key, got %q", got)
	}
}
