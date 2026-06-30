// Package embed is the embedding-provider seam. Implementations turn
// text into vectors; the OpenAI implementation lands in
// internal/embed/openai. The interface is intentionally small — Embed,
// Model, Dim — so that swapping providers (OpenAI → Gemini → local)
// is a config change at main.go and nothing else moves.
package embed

import "context"

type Provider interface {
	// Embed returns one vector per input text, in input order. All
	// vectors share the same dimensionality (Dim).
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Model returns the embedding model identifier (e.g.
	// "text-embedding-3-small"). It is recorded on
	// IndexResultResponse so a later model swap is auditable per row.
	Model() string

	// Dim returns the vector dimensionality the provider produces.
	// Backends use this when materialising their collection schema.
	Dim() int
}
