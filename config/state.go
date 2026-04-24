package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// StateFileName is the filename for the runtime state file inside
// ConfigDir. It holds mtime/counter/diagnostic fields that the watcher
// and indexer persist between runs. These must never leak into the
// user-owned config.yaml because the file is sometimes committed to
// version control and should round-trip cleanly.
const StateFileName = "state.yaml"

// State represents runtime-only values that grepai persists between
// daemon runs. Fields here intentionally do NOT appear in config.yaml.
//
// All fields use omitempty so that a brand-new state.yaml starts small
// and grows only with state that the running daemon has actually
// observed.
type State struct {
	// LastIndexTime is the timestamp of the last successful full/reindex
	// pass. It is used by the incremental scanner to skip files whose
	// mtime predates this timestamp.
	LastIndexTime time.Time `yaml:"last_index_time,omitempty"`

	// LastActivityTime is the timestamp of the most recent observable
	// watcher event — index, remove, rename, or chunk-skip. Unlike
	// LastIndexTime, it advances on removal events so that `grepai status`
	// never reports a stale "Last activity" timestamp when the only
	// recent change was a file deletion.
	LastActivityTime time.Time `yaml:"last_activity_time,omitempty"`
}

// GetStatePath returns the path to the state.yaml file within a project's
// .grepai/ directory.
func GetStatePath(projectRoot string) string {
	return filepath.Join(GetConfigDir(projectRoot), StateFileName)
}

// LoadState reads the runtime state for projectRoot. A missing file is
// not an error; the caller gets a zero-valued State back. This matches
// the semantics callers expect when grepai is freshly initialized.
func LoadState(projectRoot string) (*State, error) {
	path := GetStatePath(projectRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var st State
	if err := yaml.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	return &st, nil
}

// SaveState writes the runtime state for projectRoot. The .grepai/
// directory is created if missing. Callers should not assume atomic
// writes across processes; concurrent daemons on the same worktree are
// not supported.
func (s *State) Save(projectRoot string) error {
	if s == nil {
		return nil
	}
	dir := GetConfigDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := GetStatePath(projectRoot)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

// IsZero reports whether the state has any runtime values recorded.
// Used by the migration path to decide if we still need to write a
// state file or can skip it entirely.
func (s *State) IsZero() bool {
	if s == nil {
		return true
	}
	return s.LastIndexTime.IsZero() && s.LastActivityTime.IsZero()
}
