package config

import "testing"

func TestProviderBatchDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   string
		wantSize   int
		wantTokens int
	}{
		{name: "voyageai uses conservative Voyage limits", provider: "voyageai", wantSize: 900, wantTokens: 80000},
		{name: "openai keeps legacy generous limits", provider: "openai", wantSize: 2000, wantTokens: 280000},
		{name: "openrouter treated like openai", provider: "openrouter", wantSize: 2000, wantTokens: 280000},
		{name: "unknown provider falls back to openai defaults", provider: "mystery", wantSize: 2000, wantTokens: 280000},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSize, gotTokens := ProviderBatchDefault(tc.provider)
			if gotSize != tc.wantSize || gotTokens != tc.wantTokens {
				t.Fatalf("ProviderBatchDefault(%q) = (%d, %d); want (%d, %d)",
					tc.provider, gotSize, gotTokens, tc.wantSize, tc.wantTokens)
			}
		})
	}
}

func TestEmbedderConfig_ResolveBatchLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        EmbedderConfig
		wantSize   int
		wantTokens int
	}{
		{
			name:       "voyageai falls back to provider default when unset",
			cfg:        EmbedderConfig{Provider: "voyageai"},
			wantSize:   900,
			wantTokens: 80000,
		},
		{
			name:       "openai falls back to provider default when unset",
			cfg:        EmbedderConfig{Provider: "openai"},
			wantSize:   2000,
			wantTokens: 280000,
		},
		{
			name:       "user overrides win for both fields",
			cfg:        EmbedderConfig{Provider: "voyageai", MaxBatchSize: 128, MaxBatchTokens: 32000},
			wantSize:   128,
			wantTokens: 32000,
		},
		{
			name:       "partial override: size only keeps token default",
			cfg:        EmbedderConfig{Provider: "voyageai", MaxBatchSize: 42},
			wantSize:   42,
			wantTokens: 80000,
		},
		{
			name:       "partial override: tokens only keeps size default",
			cfg:        EmbedderConfig{Provider: "openai", MaxBatchTokens: 1234},
			wantSize:   2000,
			wantTokens: 1234,
		},
		{
			name:       "zero or negative overrides are ignored",
			cfg:        EmbedderConfig{Provider: "voyageai", MaxBatchSize: 0, MaxBatchTokens: -1},
			wantSize:   900,
			wantTokens: 80000,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSize, gotTokens := tc.cfg.ResolveBatchLimits()
			if gotSize != tc.wantSize || gotTokens != tc.wantTokens {
				t.Fatalf("ResolveBatchLimits() = (%d, %d); want (%d, %d)",
					gotSize, gotTokens, tc.wantSize, tc.wantTokens)
			}
		})
	}
}
