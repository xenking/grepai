package embedder

import (
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestFactoryRespectsConfiguredBatchLimits asserts that the factory threads
// user-provided max_batch_size / max_batch_tokens into the concrete embedders
// (issue #92). When the user leaves them unset, the per-provider default from
// config.ProviderBatchDefault must be honored.
func TestFactoryRespectsConfiguredBatchLimits(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("VOYAGE_API_KEY", "test-key")

	tests := []struct {
		name       string
		provider   string
		overrideSz int
		overrideTk int
		wantSize   int
		wantTokens int
	}{
		{name: "voyageai default", provider: "voyageai", wantSize: 900, wantTokens: 80000},
		{name: "voyageai custom", provider: "voyageai", overrideSz: 250, overrideTk: 40000, wantSize: 250, wantTokens: 40000},
		{name: "openai default", provider: "openai", wantSize: 2000, wantTokens: 280000},
		{name: "openai custom", provider: "openai", overrideSz: 512, overrideTk: 150000, wantSize: 512, wantTokens: 150000},
		{name: "voyageai partial override", provider: "voyageai", overrideSz: 123, wantSize: 123, wantTokens: 80000},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Embedder: config.EmbedderConfig{
					Provider:       tc.provider,
					Model:          "test-model",
					Endpoint:       "https://example.invalid/v1",
					Parallelism:    1,
					MaxBatchSize:   tc.overrideSz,
					MaxBatchTokens: tc.overrideTk,
				},
			}

			emb, err := NewFromConfig(cfg)
			if err != nil {
				t.Fatalf("NewFromConfig: %v", err)
			}
			defer emb.Close()

			be, ok := emb.(BatchEmbedder)
			if !ok {
				t.Fatalf("%s embedder does not implement BatchEmbedder", tc.provider)
			}

			got := be.BatchConfig()
			if got.MaxBatchSize != tc.wantSize || got.MaxBatchTokens != tc.wantTokens {
				t.Fatalf("%s BatchConfig = (%d, %d); want (%d, %d)",
					tc.provider, got.MaxBatchSize, got.MaxBatchTokens, tc.wantSize, tc.wantTokens)
			}
		})
	}
}
