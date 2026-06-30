// Package backend is the vector store seam. The memory implementation
// (internal/backend/memory) is the dev default; the milvus implementation
// (internal/backend/milvus) is the production target. Both satisfy the
// same Index contract so the indexer's search and resolve flows behave
// identically across them — only durability, scale, and ANN quality
// differ.
package backend

import (
	"context"

	"github.com/keix/kowloon"
)

type Index interface {
	// Upsert writes records and their vectors. len(records) ==
	// len(vectors); each vector's dimensionality matches the
	// provider's Dim().
	Upsert(ctx context.Context, records []kowloon.Record, vectors [][]float32) error

	// Search returns the top matches for the query vector, after
	// applying SearchRequest.Filters as exact-match scalar filters
	// on Record.Metadata. SearchRequest.RecordType narrows to one
	// record kind; empty means "all".
	Search(ctx context.Context, query kowloon.SearchRequest, vector []float32) ([]kowloon.Match, error)

	// DeleteByJob drops every record whose JobID-derived ID prefix
	// matches. Used both for re-index recovery (drop a batch, re-add
	// with a new embedding model) and the explicit
	// DELETE /v1/jobs/{job_id} endpoint.
	DeleteByJob(ctx context.Context, jobID string) error
}
