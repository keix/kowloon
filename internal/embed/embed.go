// Package embed is the embedding-provider seam. Implementations turn
// text into vectors; the OpenAI implementation lands in
// internal/embed/openai. The interface is intentionally small — Embed,
// Model, Dim — so that swapping providers (OpenAI → Gemini → local)
// is a config change at main.go and nothing else moves.
package embed

import "context"

// Result is what Embed returns. Vectors carries one entry per input
// text in input order. CacheHits and CacheMisses are populated by a
// caching wrapper in front of the raw provider (see
// internal/embed/cache); raw providers return them as zero. The
// indexer's structured log reads them to distinguish freshly embedded
// records from cache-served ones.
type Result struct {
	Vectors     [][]float32
	CacheHits   int
	CacheMisses int
}

type Provider interface {
	// Embed returns Result.Vectors in input order (len(Vectors) ==
	// len(texts)).
	Embed(ctx context.Context, texts []string) (Result, error)

	// Model returns the embedding model identifier (e.g.
	// "text-embedding-3-small"). It is recorded on
	// IndexResultResponse so a later model swap is auditable per row.
	Model() string

	// Dim returns the vector dimensionality the provider produces.
	// Backends use this when materialising their collection schema.
	Dim() int
}
