package config

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/yoanbernabeu/grepai/git"
	"gopkg.in/yaml.v3"
)

const (
	ConfigDir           = ".grepai"
	ConfigFileName      = "config.yaml"
	IndexFileName       = "index.gob"
	SymbolIndexFileName = "symbols.gob"
	RPGIndexFileName    = "rpg.gob"

	DefaultEmbedderProvider         = "ollama"
	DefaultOllamaEmbeddingModel     = "nomic-embed-text"
	DefaultLMStudioEmbeddingModel   = "text-embedding-nomic-embed-text-v1.5"
	DefaultOpenAIEmbeddingModel     = "text-embedding-3-small"
	DefaultSyntheticEmbeddingModel  = "hf:nomic-ai/nomic-embed-text-v1.5"
	DefaultOpenRouterEmbeddingModel = "openai/text-embedding-3-small"
	OpenAIEmbeddingModelLarge       = "text-embedding-3-large"
	OpenRouterEmbeddingModelLarge   = "openai/text-embedding-3-large"
	OpenRouterEmbeddingModelQwen8B  = "qwen/qwen3-embedding-8b"
	DefaultVoyageAIEmbeddingModel   = "voyage-code-3"

	DefaultOllamaEndpoint     = "http://localhost:11434"
	DefaultLMStudioEndpoint   = "http://127.0.0.1:1234"
	DefaultOpenAIEndpoint     = "https://api.openai.com/v1"
	DefaultVoyageAIEndpoint   = "https://api.voyageai.com/v1"
	DefaultSyntheticEndpoint  = "https://api.synthetic.new/openai/v1"
	DefaultOpenRouterEndpoint = "https://openrouter.ai/api/v1"

	DefaultLocalEmbeddingDimensions = 768
	DefaultOpenAIDimensions         = 1536
	DefaultOpenAILargeDimensions    = 3072
	DefaultQwen8BDimensions         = 4096
	DefaultVoyageAIDimensions       = 1024
	DefaultOpenAIParallelism        = 4

	DefaultPostgresDSN    = "postgres://localhost:5432/grepai"
	DefaultQdrantEndpoint = "localhost"
	DefaultQdrantPort     = 6334

	// RPG default configuration values.
	DefaultRPGDriftThreshold       = 0.35
	DefaultRPGMaxTraversalDepth    = 3
	DefaultRPGLLMTimeoutMs         = 8000
	DefaultRPGFeatureMode          = "local"
	DefaultRPGFeatureGroupStrategy = "sample"

	// Watch defaults for RPG realtime updates.
	DefaultWatchRPGPersistIntervalMs      = 1000
	DefaultWatchRPGDerivedDebounceMs      = 300
	DefaultWatchRPGFullReconcileIntervalS = 300
	DefaultWatchRPGMaxDirtyFilesPerBatch  = 128
)

type Config struct {
	Version           int             `yaml:"version"`
	Embedder          EmbedderConfig  `yaml:"embedder"`
	Store             StoreConfig     `yaml:"store"`
	Chunking          ChunkingConfig  `yaml:"chunking"`
	Framework         FrameworkConfig `yaml:"framework_processing"`
	Watch             WatchConfig     `yaml:"watch"`
	Search            SearchConfig    `yaml:"search"`
	Trace             TraceConfig     `yaml:"trace"`
	RPG               RPGConfig       `yaml:"rpg"`
	Update            UpdateConfig    `yaml:"update"`
	Ignore            []string        `yaml:"ignore"`
	ExternalGitignore string          `yaml:"external_gitignore,omitempty"`

	// rawBytes holds the exact bytes of config.yaml as read from disk.
	// Save() writes them back verbatim when the in-memory config still
	// matches what Load observed, so that loading + saving a user-owned
	// file without edits produces a byte-identical result.
	//
	// Excluded from YAML (un)marshaling via the "-" tag.
	rawBytes []byte `yaml:"-"`
	// loadedSnapshot is a deep copy of the struct produced by Load,
	// taken after applyDefaults() has run. Save() uses it to detect
	// whether the user (or the daemon) changed anything that warrants
	// a rewrite. nil for configs that were never Load()-ed from disk.
	loadedSnapshot *Config `yaml:"-"`
}

// UpdateConfig holds auto-update settings
type UpdateConfig struct {
	CheckOnStartup bool `yaml:"check_on_startup"` // Check for updates when running commands
}

type SearchConfig struct {
	Boost  BoostConfig  `yaml:"boost"`
	Hybrid HybridConfig `yaml:"hybrid"`
	Dedup  DedupConfig  `yaml:"dedup"`
}

// DedupConfig controls file-level deduplication of search results.
type DedupConfig struct {
	Enabled bool `yaml:"enabled"`
}

type HybridConfig struct {
	Enabled bool    `yaml:"enabled"`
	K       float32 `yaml:"k"` // RRF constant (default: 60)
}

type BoostConfig struct {
	Enabled   bool        `yaml:"enabled"`
	Penalties []BoostRule `yaml:"penalties"`
	Bonuses   []BoostRule `yaml:"bonuses"`
}

type BoostRule struct {
	Pattern string  `yaml:"pattern"`
	Factor  float32 `yaml:"factor"`
}

type EmbedderConfig struct {
	Provider       string `yaml:"provider"` // ollama | lmstudio | openai | voyageai | synthetic | openrouter
	Model          string `yaml:"model"`
	Endpoint       string `yaml:"endpoint,omitempty"`
	APIKey         string `yaml:"api_key,omitempty"`
	Dimensions     *int   `yaml:"dimensions,omitempty"`
	Parallelism    int    `yaml:"parallelism,omitempty"`      // Number of parallel workers for batch embedding (default: 4)
	MaxBatchSize   int    `yaml:"max_batch_size,omitempty"`   // Max chunks per embedding API call (0 = per-provider default)
	MaxBatchTokens int    `yaml:"max_batch_tokens,omitempty"` // Max tokens per embedding API batch (0 = per-provider default)
}

// ProviderBatchDefault reports the baked-in batch limits for a provider.
// Returned values are used when the user does not override them via
// embedder.max_batch_size / embedder.max_batch_tokens in config.yaml.
//
// These defaults are hardened against observed API ceilings:
//   - voyageai: Voyage enforces 1000 inputs / 120k tokens per request
//   - openai:   OpenAI allows 2048 inputs / ~300k tokens
//   - others:   treated like OpenAI (generous, since most are OpenAI-compatible)
func ProviderBatchDefault(provider string) (maxBatchSize, maxBatchTokens int) {
	switch provider {
	case "voyageai":
		return 900, 80000
	default:
		return 2000, 280000
	}
}

// ResolveBatchLimits returns the effective batch limits for this embedder
// config: user-specified values win, otherwise fall back to the per-provider
// default from ProviderBatchDefault.
func (e *EmbedderConfig) ResolveBatchLimits() (maxBatchSize, maxBatchTokens int) {
	maxBatchSize, maxBatchTokens = ProviderBatchDefault(e.Provider)
	if e.MaxBatchSize > 0 {
		maxBatchSize = e.MaxBatchSize
	}
	if e.MaxBatchTokens > 0 {
		maxBatchTokens = e.MaxBatchTokens
	}
	return maxBatchSize, maxBatchTokens
}

// GetDimensions returns the configured dimensions or a default value.
// For OpenAI/OpenRouter, defaults to 1536 (text-embedding-3-small).
// For Voyage AI, defaults to 1024 (voyage-code-3).
// For Ollama/LMStudio/Synthetic, defaults to 768 (nomic-embed-text-v1.5).
func (e *EmbedderConfig) GetDimensions() int {
	if e.Dimensions != nil {
		return *e.Dimensions
	}
	switch e.Provider {
	case "openai", "openrouter":
		switch strings.TrimSpace(e.Model) {
		case OpenAIEmbeddingModelLarge, OpenRouterEmbeddingModelLarge:
			return DefaultOpenAILargeDimensions
		case OpenRouterEmbeddingModelQwen8B, "qwen3-embedding-8b":
			return DefaultQwen8BDimensions
		default:
			return DefaultOpenAIDimensions
		}
	case "voyageai":
		return DefaultVoyageAIDimensions
	default:
		return DefaultLocalEmbeddingDimensions
	}
}

func DefaultEmbedderForProvider(provider string) EmbedderConfig {
	switch provider {
	case "voyageai":
		return EmbedderConfig{
			Provider:   "voyageai",
			Model:      DefaultVoyageAIEmbeddingModel,
			Endpoint:   DefaultVoyageAIEndpoint,
			Dimensions: nil, // Voyage AI uses native dimensions (1024)
		}
	case "synthetic":
		dim := DefaultLocalEmbeddingDimensions
		return EmbedderConfig{
			Provider:   "synthetic",
			Model:      DefaultSyntheticEmbeddingModel,
			Endpoint:   DefaultSyntheticEndpoint,
			Dimensions: &dim,
		}
	case "openrouter":
		return EmbedderConfig{
			Provider:   "openrouter",
			Model:      DefaultOpenRouterEmbeddingModel,
			Endpoint:   DefaultOpenRouterEndpoint,
			Dimensions: nil,
		}
	case "lmstudio":
		dim := DefaultLocalEmbeddingDimensions
		return EmbedderConfig{
			Provider:   "lmstudio",
			Model:      DefaultLMStudioEmbeddingModel,
			Endpoint:   DefaultLMStudioEndpoint,
			Dimensions: &dim,
		}
	case "openai":
		return EmbedderConfig{
			Provider:    "openai",
			Model:       DefaultOpenAIEmbeddingModel,
			Endpoint:    DefaultOpenAIEndpoint,
			Dimensions:  nil,
			Parallelism: DefaultOpenAIParallelism,
		}
	case "ollama":
		fallthrough
	default:
		dim := DefaultLocalEmbeddingDimensions
		return EmbedderConfig{
			Provider:   providerOrDefault(provider),
			Model:      DefaultOllamaEmbeddingModel,
			Endpoint:   DefaultOllamaEndpoint,
			Dimensions: &dim,
		}
	}
}

type StoreConfig struct {
	Backend  string         `yaml:"backend"` // gob | postgres | qdrant
	Postgres PostgresConfig `yaml:"postgres,omitempty"`
	Qdrant   QdrantConfig   `yaml:"qdrant,omitempty"`
}

type PostgresConfig struct {
	DSN string `yaml:"dsn"`
}

type QdrantConfig struct {
	Endpoint   string `yaml:"endpoint"`             // e.g., "http://localhost" or "localhost"
	Port       int    `yaml:"port,omitempty"`       // e.g., 6333
	Collection string `yaml:"collection,omitempty"` // Optional, defaults from project path
	APIKey     string `yaml:"api_key,omitempty"`    // Optional, for Qdrant Cloud
	UseTLS     bool   `yaml:"use_tls,omitempty"`    // Enable TLS (for Qdrant Cloud)
}

type ChunkingConfig struct {
	Size    int `yaml:"size"`
	Overlap int `yaml:"overlap"`
}

func DefaultStoreForBackend(backend string) StoreConfig {
	cfg := StoreConfig{Backend: backendOrDefault(backend)}
	switch cfg.Backend {
	case "postgres":
		cfg.Postgres = PostgresConfig{
			DSN: DefaultPostgresDSN,
		}
	case "qdrant":
		cfg.Qdrant = QdrantConfig{
			Endpoint: DefaultQdrantEndpoint,
			Port:     DefaultQdrantPort,
		}
	}
	return cfg
}

type FrameworkConfig struct {
	Enabled    bool                  `yaml:"enabled"`
	Mode       string                `yaml:"mode"` // auto | require | off
	NodePath   string                `yaml:"node_path,omitempty"`
	Frameworks FrameworkFeatureFlags `yaml:"frameworks"`
	isSet      bool                  `yaml:"-"`
	enabledSet bool                  `yaml:"-"`
}

type FrameworkFeatureFlags struct {
	Vue    FrameworkFeatureConfig `yaml:"vue"`
	Svelte FrameworkFeatureConfig `yaml:"svelte"`
	Astro  FrameworkFeatureConfig `yaml:"astro"`
	Solid  FrameworkFeatureConfig `yaml:"solid"`
}

type FrameworkFeatureConfig struct {
	Enabled    bool `yaml:"enabled"`
	isSet      bool `yaml:"-"`
	enabledSet bool `yaml:"-"`
}

func (c *FrameworkConfig) UnmarshalYAML(value *yaml.Node) error {
	type raw FrameworkConfig
	var aux raw
	if err := value.Decode(&aux); err != nil {
		return err
	}
	*c = FrameworkConfig(aux)
	c.isSet = true
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i].Value == "enabled" {
			c.enabledSet = true
			break
		}
	}
	return nil
}

func (c *FrameworkFeatureConfig) UnmarshalYAML(value *yaml.Node) error {
	type raw FrameworkFeatureConfig
	var aux raw
	if err := value.Decode(&aux); err != nil {
		return err
	}
	*c = FrameworkFeatureConfig(aux)
	c.isSet = true
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i].Value == "enabled" {
			c.enabledSet = true
			break
		}
	}
	return nil
}

type WatchConfig struct {
	DebounceMs int `yaml:"debounce_ms"`
	// LastIndexTime is populated at Load time from state.yaml so that
	// existing callers can continue to read cfg.Watch.LastIndexTime
	// directly. It is intentionally NOT serialized to config.yaml —
	// runtime state belongs in state.yaml (see state.go). Writes go
	// through (*State).Save() in the watch loop.
	LastIndexTime               time.Time `yaml:"-"`
	RPGPersistIntervalMs        int       `yaml:"rpg_persist_interval_ms,omitempty"`
	RPGDerivedDebounceMs        int       `yaml:"rpg_derived_debounce_ms,omitempty"`
	RPGFullReconcileIntervalSec int       `yaml:"rpg_full_reconcile_interval_sec,omitempty"`
	RPGMaxDirtyFilesPerBatch    int       `yaml:"rpg_max_dirty_files_per_batch,omitempty"`
}

type TraceConfig struct {
	Mode             string   `yaml:"mode"`              // fast or precise
	EnabledLanguages []string `yaml:"enabled_languages"` // File extensions to index
	ExcludePatterns  []string `yaml:"exclude_patterns"`  // Patterns to exclude
}

type RPGConfig struct {
	Enabled              bool    `yaml:"enabled"`
	StorePath            string  `yaml:"store_path,omitempty"`
	FeatureMode          string  `yaml:"feature_mode"` // local | hybrid | llm
	DriftThreshold       float64 `yaml:"drift_threshold"`
	MaxTraversalDepth    int     `yaml:"max_traversal_depth"`
	LLMProvider          string  `yaml:"llm_provider,omitempty"`
	LLMModel             string  `yaml:"llm_model,omitempty"`
	LLMEndpoint          string  `yaml:"llm_endpoint,omitempty"`
	LLMAPIKey            string  `yaml:"llm_api_key,omitempty"`
	LLMTimeoutMs         int     `yaml:"llm_timeout_ms,omitempty"`
	FeatureGroupStrategy string  `yaml:"feature_group_strategy,omitempty"`
}

// ValidateRPGConfig checks RPG configuration values for validity.
func ValidateRPGConfig(cfg RPGConfig) error {
	if cfg.DriftThreshold < 0.0 || cfg.DriftThreshold > 1.0 {
		return fmt.Errorf("rpg.drift_threshold must be between 0.0 and 1.0, got %.2f", cfg.DriftThreshold)
	}
	if cfg.MaxTraversalDepth < 1 || cfg.MaxTraversalDepth > 10 {
		return fmt.Errorf("rpg.max_traversal_depth must be between 1 and 10, got %d", cfg.MaxTraversalDepth)
	}
	switch cfg.FeatureMode {
	case "local", "hybrid", "llm":
		// valid
	default:
		return fmt.Errorf("rpg.feature_mode must be one of: local, hybrid, llm; got %q", cfg.FeatureMode)
	}
	switch cfg.FeatureGroupStrategy {
	case "sample", "split":
		// valid
	default:
		return fmt.Errorf("rpg.feature_group_strategy must be one of: sample, split; got %q", cfg.FeatureGroupStrategy)
	}
	return nil
}

// ValidateWatchConfig checks watch configuration values for validity.
func ValidateWatchConfig(cfg WatchConfig) error {
	if cfg.RPGPersistIntervalMs < 200 {
		return fmt.Errorf("watch.rpg_persist_interval_ms must be >= 200, got %d", cfg.RPGPersistIntervalMs)
	}
	if cfg.RPGDerivedDebounceMs < 100 {
		return fmt.Errorf("watch.rpg_derived_debounce_ms must be >= 100, got %d", cfg.RPGDerivedDebounceMs)
	}
	if cfg.RPGFullReconcileIntervalSec < 30 {
		return fmt.Errorf("watch.rpg_full_reconcile_interval_sec must be >= 30, got %d", cfg.RPGFullReconcileIntervalSec)
	}
	if cfg.RPGMaxDirtyFilesPerBatch < 1 {
		return fmt.Errorf("watch.rpg_max_dirty_files_per_batch must be >= 1, got %d", cfg.RPGMaxDirtyFilesPerBatch)
	}
	return nil
}

func DefaultConfig() *Config {
	return &Config{
		Version:  1,
		Embedder: DefaultEmbedderForProvider(DefaultEmbedderProvider),
		Store:    DefaultStoreForBackend("gob"),
		Chunking: ChunkingConfig{
			Size:    512,
			Overlap: 50,
		},
		Framework: FrameworkConfig{
			Enabled:  true,
			Mode:     "auto",
			NodePath: "node",
			Frameworks: FrameworkFeatureFlags{
				Vue:    FrameworkFeatureConfig{Enabled: true},
				Svelte: FrameworkFeatureConfig{Enabled: false},
				Astro:  FrameworkFeatureConfig{Enabled: false},
				Solid:  FrameworkFeatureConfig{Enabled: false},
			},
		},
		Watch: WatchConfig{
			DebounceMs:                  500,
			RPGPersistIntervalMs:        DefaultWatchRPGPersistIntervalMs,
			RPGDerivedDebounceMs:        DefaultWatchRPGDerivedDebounceMs,
			RPGFullReconcileIntervalSec: DefaultWatchRPGFullReconcileIntervalS,
			RPGMaxDirtyFilesPerBatch:    DefaultWatchRPGMaxDirtyFilesPerBatch,
		},
		Search: SearchConfig{
			Dedup: DedupConfig{
				Enabled: true,
			},
			Hybrid: HybridConfig{
				Enabled: false,
				K:       60,
			},
			Boost: BoostConfig{
				Enabled: true,
				Penalties: []BoostRule{
					// Test files (multi-language)
					{Pattern: "/tests/", Factor: 0.5},
					{Pattern: "/test/", Factor: 0.5},
					{Pattern: "__tests__", Factor: 0.5},
					{Pattern: "_test.", Factor: 0.5},
					{Pattern: ".test.", Factor: 0.5},
					{Pattern: ".spec.", Factor: 0.5},
					{Pattern: "test_", Factor: 0.5},
					// Mocks
					{Pattern: "/mocks/", Factor: 0.4},
					{Pattern: "/mock/", Factor: 0.4},
					{Pattern: ".mock.", Factor: 0.4},
					// Fixtures & test data
					{Pattern: "/fixtures/", Factor: 0.4},
					{Pattern: "/testdata/", Factor: 0.4},
					// Generated code
					{Pattern: "/generated/", Factor: 0.4},
					{Pattern: ".generated.", Factor: 0.4},
					{Pattern: ".gen.", Factor: 0.4},
					// Documentation
					{Pattern: ".md", Factor: 0.6},
					{Pattern: "/docs/", Factor: 0.6},
				},
				Bonuses: []BoostRule{
					// Entry points (multi-language)
					{Pattern: "/src/", Factor: 1.1},
					{Pattern: "/lib/", Factor: 1.1},
					{Pattern: "/app/", Factor: 1.1},
				},
			},
		},
		Trace: TraceConfig{
			Mode: "fast",
			EnabledLanguages: []string{
				".go", ".js", ".ts", ".jsx", ".tsx", ".vue", ".py", ".php",
				".lua",
				".c", ".h", ".cpp", ".hpp", ".cc", ".cxx",
				".rs", ".zig", ".cs", ".java",
				".fs", ".fsx", ".fsi", // F#
				".pas", ".dpr", // Pascal/Delphi
			},
			ExcludePatterns: []string{
				"*_test.go",
				"*.spec.ts",
				"*.spec.js",
				"*.test.ts",
				"*.test.js",
				"__tests__/*",
			},
		},
		RPG: RPGConfig{
			Enabled:              false,
			FeatureMode:          DefaultRPGFeatureMode,
			DriftThreshold:       DefaultRPGDriftThreshold,
			MaxTraversalDepth:    DefaultRPGMaxTraversalDepth,
			LLMProvider:          "ollama",
			LLMModel:             "",
			LLMEndpoint:          "http://localhost:11434/v1",
			LLMTimeoutMs:         DefaultRPGLLMTimeoutMs,
			FeatureGroupStrategy: DefaultRPGFeatureGroupStrategy,
		},
		Update: UpdateConfig{
			CheckOnStartup: false, // Opt-in by default for privacy
		},
		Ignore: []string{
			".git",
			".grepai",
			"node_modules",
			"vendor",
			"bin",
			"dist",
			"__pycache__",
			".venv",
			"venv",
			".idea",
			".vscode",
			"target",
			".zig-cache",
			"zig-out",
			"qdrant_storage",
		},
	}
}

func GetConfigDir(projectRoot string) string {
	return filepath.Join(projectRoot, ConfigDir)
}

func GetConfigPath(projectRoot string) string {
	return filepath.Join(GetConfigDir(projectRoot), ConfigFileName)
}

func GetIndexPath(projectRoot string) string {
	return filepath.Join(GetConfigDir(projectRoot), IndexFileName)
}

func GetSymbolIndexPath(projectRoot string) string {
	return filepath.Join(GetConfigDir(projectRoot), SymbolIndexFileName)
}

func GetRPGIndexPath(projectRoot string) string {
	return filepath.Join(GetConfigDir(projectRoot), RPGIndexFileName)
}

func Load(projectRoot string) (*Config, error) {
	configPath := GetConfigPath(projectRoot)

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Detect legacy configs that still carry runtime state inline
	// (watch.last_index_time). We migrate to state.yaml and strip the
	// field from the in-memory raw bytes so the next Save rewrites a
	// clean file. One-shot INFO log per migration event.
	migrated, migratedTime, strippedBytes, err := detectAndStripLegacyLastIndexTime(data)
	if err != nil {
		return nil, fmt.Errorf("migrate legacy state: %w", err)
	}

	// Load state.yaml (runtime-only fields). Back-compat: if state is
	// empty but the legacy config had watch.last_index_time, seed it.
	state, err := LoadState(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if migrated {
		if state.LastIndexTime.IsZero() || migratedTime.After(state.LastIndexTime) {
			state.LastIndexTime = migratedTime
		}
		if err := state.Save(projectRoot); err != nil {
			return nil, fmt.Errorf("write migrated state: %w", err)
		}
		logMigrationOnce(projectRoot)
		data = strippedBytes
	}

	// Populate in-memory fields backed by state.yaml so existing
	// callers that read cfg.Watch.LastIndexTime keep working.
	cfg.Watch.LastIndexTime = state.LastIndexTime

	// Apply defaults for missing values (backward compatibility)
	cfg.applyDefaults()

	// Validate watch timing configuration
	if err := ValidateWatchConfig(cfg.Watch); err != nil {
		return nil, fmt.Errorf("invalid watch configuration: %w", err)
	}

	// Validate RPG config when enabled
	if cfg.RPG.Enabled {
		if err := ValidateRPGConfig(cfg.RPG); err != nil {
			return nil, fmt.Errorf("invalid RPG configuration: %w", err)
		}
	}

	// Capture the post-default snapshot + raw bytes so that Save() can
	// recognize a "no edits since Load" round-trip and write back the
	// original bytes untouched.
	cfg.rawBytes = append([]byte(nil), data...)
	cfg.loadedSnapshot = deepCopyForSnapshot(&cfg)

	return &cfg, nil
}

// deepCopyForSnapshot returns a copy of cfg with private tracking
// fields cleared. We compare by reflect.DeepEqual, so the copy must
// not reference the original's rawBytes/loadedSnapshot.
func deepCopyForSnapshot(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	// Independent slice so later mutation to the original does not
	// leak into the snapshot.
	if cfg.Ignore != nil {
		clone.Ignore = append([]string(nil), cfg.Ignore...)
	}
	clone.rawBytes = nil
	clone.loadedSnapshot = nil
	return &clone
}

// detectAndStripLegacyLastIndexTime scans raw config bytes for a
// `watch.last_index_time` entry under the top-level `watch:` block and
// removes it if found. Returns the extracted time (if any), the edited
// bytes, and whether a migration was performed. This is intentionally
// a conservative string-level edit (matching the upstream yaml.v3
// formatting) to preserve byte-identity for the rest of the file.
func detectAndStripLegacyLastIndexTime(data []byte) (migrated bool, when time.Time, stripped []byte, err error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return false, time.Time{}, nil, fmt.Errorf("parse yaml for migration: %w", err)
	}
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return false, time.Time{}, data, nil
	}

	root := node.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		val := root.Content[i+1]
		if key.Value != "watch" || val.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			wk := val.Content[j]
			wv := val.Content[j+1]
			if wk.Value != "last_index_time" {
				continue
			}
			parsed, perr := time.Parse(time.RFC3339Nano, wv.Value)
			if perr != nil {
				// Accept other time formats yaml.v3 might emit.
				parsed, _ = time.Parse(time.RFC3339, wv.Value)
			}
			edited := stripYAMLLineByKey(data, "last_index_time")
			return true, parsed, edited, nil
		}
	}
	return false, time.Time{}, data, nil
}

// stripYAMLLineByKey removes lines of the form `  <indent>key: value`
// from data. It is scoped to the specific key name and tolerates
// variable leading whitespace. Lines that do not match are preserved
// verbatim, protecting user formatting.
func stripYAMLLineByKey(data []byte, key string) []byte {
	needle := []byte(key + ":")
	lines := bytes.Split(data, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmed, needle) {
			// Only strip if the rest of the line is either empty, a
			// value, or a comment — NOT a nested map where the key is
			// followed by more content on the same visual level.
			rest := trimmed[len(needle):]
			if len(rest) == 0 || rest[0] == ' ' || rest[0] == '\t' {
				continue
			}
		}
		out = append(out, line)
	}
	return bytes.Join(out, []byte("\n"))
}

// logMigrationOnce emits a single INFO line per process per project
// when the legacy watch.last_index_time is migrated to state.yaml. We
// deduplicate by projectRoot to avoid noisy logs when multiple code
// paths reload the same config.
var (
	migrationLogOnce sync.Map // map[string]struct{}
)

func logMigrationOnce(projectRoot string) {
	if _, loaded := migrationLogOnce.LoadOrStore(projectRoot, struct{}{}); loaded {
		return
	}
	log.Printf("config: migrated watch.last_index_time from config.yaml to state.yaml for %s", projectRoot)
}

// applyDefaults fills in missing configuration values with sensible defaults.
// This ensures backward compatibility with older config files that may not
// have newer fields like dimensions or endpoint.
func (c *Config) applyDefaults() {
	defaults := DefaultConfig()

	// Embedder defaults
	if c.Embedder.Endpoint == "" {
		c.Embedder.Endpoint = DefaultEmbedderForProvider(c.Embedder.Provider).Endpoint
	}

	// Only set default dimensions for local embedders.
	// For OpenAI/OpenRouter, leave nil to let the API use the model's native dimensions.
	if c.Embedder.Dimensions == nil {
		switch cfg := DefaultEmbedderForProvider(c.Embedder.Provider); {
		case cfg.Dimensions != nil:
			dim := *cfg.Dimensions
			c.Embedder.Dimensions = &dim
		}
	}

	// Parallelism default (used by OpenAI and Voyage AI embedders)
	if c.Embedder.Parallelism <= 0 {
		c.Embedder.Parallelism = 4
	}

	// Chunking defaults
	if c.Chunking.Size == 0 {
		c.Chunking.Size = defaults.Chunking.Size
	}
	if c.Chunking.Overlap == 0 {
		c.Chunking.Overlap = defaults.Chunking.Overlap
	}

	// Framework processing defaults
	hasFrameworkConfig := c.Framework.isSet
	if !hasFrameworkConfig {
		c.Framework = defaults.Framework
	} else {
		if !c.Framework.enabledSet {
			c.Framework.Enabled = defaults.Framework.Enabled
		}
		if c.Framework.Mode == "" {
			c.Framework.Mode = defaults.Framework.Mode
		}
		if c.Framework.NodePath == "" {
			c.Framework.NodePath = defaults.Framework.NodePath
		}
		if !c.Framework.Frameworks.Vue.enabledSet {
			c.Framework.Frameworks.Vue.Enabled = defaults.Framework.Frameworks.Vue.Enabled
		}
		if !c.Framework.Frameworks.Svelte.enabledSet {
			c.Framework.Frameworks.Svelte.Enabled = defaults.Framework.Frameworks.Svelte.Enabled
		}
		if !c.Framework.Frameworks.Astro.enabledSet {
			c.Framework.Frameworks.Astro.Enabled = defaults.Framework.Frameworks.Astro.Enabled
		}
		if !c.Framework.Frameworks.Solid.enabledSet {
			c.Framework.Frameworks.Solid.Enabled = defaults.Framework.Frameworks.Solid.Enabled
		}
	}

	// Watch defaults
	if c.Watch.DebounceMs == 0 {
		c.Watch.DebounceMs = defaults.Watch.DebounceMs
	}
	if c.Watch.RPGPersistIntervalMs == 0 {
		c.Watch.RPGPersistIntervalMs = defaults.Watch.RPGPersistIntervalMs
	}
	if c.Watch.RPGDerivedDebounceMs == 0 {
		c.Watch.RPGDerivedDebounceMs = defaults.Watch.RPGDerivedDebounceMs
	}
	if c.Watch.RPGFullReconcileIntervalSec == 0 {
		c.Watch.RPGFullReconcileIntervalSec = defaults.Watch.RPGFullReconcileIntervalSec
	}
	if c.Watch.RPGMaxDirtyFilesPerBatch == 0 {
		c.Watch.RPGMaxDirtyFilesPerBatch = defaults.Watch.RPGMaxDirtyFilesPerBatch
	}

	// Qdrant defaults
	if c.Store.Backend == "qdrant" && c.Store.Qdrant.Port <= 0 {
		c.Store.Qdrant.Port = DefaultStoreForBackend("qdrant").Qdrant.Port
	}

	// RPG defaults
	if c.RPG.FeatureMode == "" {
		c.RPG.FeatureMode = DefaultRPGFeatureMode
	}
	if c.RPG.DriftThreshold == 0 {
		c.RPG.DriftThreshold = DefaultRPGDriftThreshold
	}
	if c.RPG.MaxTraversalDepth <= 0 {
		c.RPG.MaxTraversalDepth = DefaultRPGMaxTraversalDepth
	}
	if c.RPG.LLMProvider == "" {
		c.RPG.LLMProvider = "ollama"
	}
	// LLMModel intentionally left empty when unset — user must configure
	// explicitly. The watch/mcp code falls back to the local extractor when
	// LLMModel is empty.
	if c.RPG.LLMEndpoint == "" {
		c.RPG.LLMEndpoint = "http://localhost:11434/v1"
	}
	if c.RPG.LLMTimeoutMs <= 0 {
		c.RPG.LLMTimeoutMs = DefaultRPGLLMTimeoutMs
	}
	if c.RPG.FeatureGroupStrategy == "" {
		c.RPG.FeatureGroupStrategy = DefaultRPGFeatureGroupStrategy
	}
}

func providerOrDefault(provider string) string {
	if provider == "" {
		return DefaultEmbedderProvider
	}
	return provider
}

func backendOrDefault(backend string) string {
	if backend == "" {
		return "gob"
	}
	return backend
}

func (c *Config) Save(projectRoot string) error {
	configDir := GetConfigDir(projectRoot)

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := GetConfigPath(projectRoot)

	// Round-trip preservation: if this Config was Load()-ed from disk
	// and nothing observable has changed since, write the original
	// bytes back verbatim. This keeps user formatting, comments, field
	// ordering, and absent-section defaults intact — the key property
	// required by the no-pollution invariant.
	if c.rawBytes != nil && c.loadedSnapshot != nil {
		current := deepCopyForSnapshot(c)
		if reflect.DeepEqual(current, c.loadedSnapshot) {
			if err := os.WriteFile(configPath, c.rawBytes, 0600); err != nil {
				return fmt.Errorf("failed to write config file: %w", err)
			}
			return nil
		}
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

func Exists(projectRoot string) bool {
	configPath := GetConfigPath(projectRoot)
	_, err := os.Stat(configPath)
	return err == nil
}

func FindProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	// Resolve symlinks to handle symlinked directories
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	dir := cwd
	for {
		if Exists(dir) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Git worktree fallback: if we're in a linked worktree and the main
	// worktree has .grepai/, auto-initialize a local copy for isolation.
	// Each worktree gets its own config + index so search/watch operate
	// on the worktree's own files.
	gitInfo, gitErr := git.Detect(cwd)
	if gitErr == nil && gitInfo.IsWorktree && Exists(gitInfo.MainWorktree) {
		if err := autoInitFromMainWorktree(gitInfo.GitRoot, gitInfo.MainWorktree); err == nil {
			return gitInfo.GitRoot, nil
		}
	}

	return "", fmt.Errorf("no grepai project found (run 'grepai init' first)")
}

// AutoInitWorktree creates a local .grepai/ in worktreeRoot by copying config and
// index files from mainWorktree. This is used by watch to auto-init linked worktrees.
func AutoInitWorktree(worktreeRoot, mainWorktree string) error {
	return autoInitFromMainWorktree(worktreeRoot, mainWorktree)
}

// autoInitFromMainWorktree creates a local .grepai/ in the worktree by copying
// config and index files from the main worktree. This enables zero-config usage:
// search and trace work immediately with the main worktree's index as a seed,
// and watch will incrementally update for worktree-specific changes.
func autoInitFromMainWorktree(worktreeRoot, mainWorktree string) error {
	localGrepai := filepath.Join(worktreeRoot, ".grepai")
	if err := os.MkdirAll(localGrepai, 0755); err != nil {
		return err
	}

	mainGrepai := filepath.Join(mainWorktree, ".grepai")

	// Copy config.yaml (required)
	srcConfig := filepath.Join(mainGrepai, "config.yaml")
	dstConfig := filepath.Join(localGrepai, "config.yaml")
	if err := copyFileIfExists(srcConfig, dstConfig); err != nil {
		os.RemoveAll(localGrepai)
		return err
	}
	// Verify config.yaml was actually copied (it's required)
	if _, err := os.Stat(dstConfig); os.IsNotExist(err) {
		os.RemoveAll(localGrepai)
		return fmt.Errorf("config.yaml not found in main worktree: %s", srcConfig)
	}

	// Copy index.gob as seed (search works immediately)
	_ = copyFileIfExists(
		filepath.Join(mainGrepai, "index.gob"),
		filepath.Join(localGrepai, "index.gob"),
	)

	// Copy symbols.gob as seed (trace works immediately)
	_ = copyFileIfExists(
		filepath.Join(mainGrepai, "symbols.gob"),
		filepath.Join(localGrepai, "symbols.gob"),
	)

	// Ensure .grepai/ is in .gitignore
	ensureGitignoreEntry(worktreeRoot, ".grepai/")

	return nil
}

// copyFileIfExists copies src to dst if src exists. Returns error only if src
// exists but copy fails. Returns nil if src doesn't exist.
func copyFileIfExists(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

// ensureGitignoreEntry adds an entry to .gitignore if not already present.
func ensureGitignoreEntry(dir, entry string) {
	gitignorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err == nil {
		// Check if entry already exists
		for _, line := range strings.Split(string(content), "\n") {
			if strings.TrimSpace(line) == entry || strings.TrimSpace(line) == strings.TrimSuffix(entry, "/") {
				return
			}
		}
	}
	// Append entry
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return
		}
	}
	if _, err := f.WriteString(entry + "\n"); err != nil {
		return
	}
}

// FindProjectRootWithGit extends FindProjectRoot with git worktree awareness.
// It first tries the standard .grepai/ directory walk. If found, it also returns
// git worktree info if available. If .grepai/ is not found locally but we're in
// a git worktree, it checks the main worktree for .grepai/config.yaml.
//
// Returns:
//   - projectRoot: the directory containing .grepai/
//   - gitInfo: git worktree detection info (nil if not in a git repo)
//   - err: error if neither local nor main worktree has .grepai/
func FindProjectRootWithGit() (string, *git.DetectInfo, error) {
	// Try standard FindProjectRoot first
	projectRoot, findErr := FindProjectRoot()

	// Get current directory for git detection
	cwd, err := os.Getwd()
	if err != nil {
		if findErr == nil {
			return projectRoot, nil, nil
		}
		return "", nil, findErr
	}

	// Resolve symlinks (same as FindProjectRoot does)
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		if findErr == nil {
			return projectRoot, nil, nil
		}
		return "", nil, findErr
	}

	// Try to detect git info
	gitInfo, gitErr := git.Detect(cwd)
	if gitErr != nil {
		// Not in a git repo - return whatever FindProjectRoot returned
		if findErr == nil {
			return projectRoot, nil, nil
		}
		return "", nil, findErr
	}

	// If we found .grepai/ locally, return it with git info
	if findErr == nil {
		return projectRoot, gitInfo, nil
	}

	// .grepai/ not found locally, but we're in a git repo
	// If we're in a linked worktree, check if main worktree has .grepai/
	if gitInfo.IsWorktree && Exists(gitInfo.MainWorktree) {
		return gitInfo.MainWorktree, gitInfo, nil
	}

	// No .grepai/ found anywhere
	return "", gitInfo, findErr
}
