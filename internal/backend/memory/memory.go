// Package memory is the in-memory implementation of backend.Index. It
// is the v0 default for application-logic dev (KOWLOON_BACKEND=memory)
// and the harness for unit tests above it; production work goes through
// internal/backend/milvus.
//
// Search is brute-force cosine similarity over every stored vector.
// That is fine for the dev loop (a handful of card statements) but
// scales O(N) per query — the milvus backend is what protects prod.
package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/keix/kowloon"
)

type entry struct {
	record kowloon.Record
	vector []float32
}

type Backend struct {
	mu      sync.RWMutex
	entries map[string]entry
}

func New() *Backend {
	return &Backend{entries: make(map[string]entry)}
}

func (b *Backend) Upsert(_ context.Context, records []kowloon.Record, vectors [][]float32) error {
	if len(records) != len(vectors) {
		return fmt.Errorf("memory backend: %d records, %d vectors", len(records), len(vectors))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, r := range records {
		b.entries[r.ID] = entry{record: r, vector: vectors[i]}
	}
	return nil
}

func (b *Backend) Search(_ context.Context, query kowloon.SearchRequest, vector []float32) ([]kowloon.Match, error) {
	topK := query.TopK
	if topK <= 0 {
		topK = 10
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	var matches []kowloon.Match
	for _, e := range b.entries {
		if query.RecordType != "" && e.record.RecordType != query.RecordType {
			continue
		}
		if !matchesFilters(e.record, query.Filters) {
			continue
		}
		matches = append(matches, kowloon.Match{
			Record: e.record,
			Score:  cosine(vector, e.vector),
		})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > topK {
		matches = matches[:topK]
	}
	return matches, nil
}

func (b *Backend) DeleteByJob(_ context.Context, jobID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	prefix := jobID + ":"
	for id, e := range b.entries {
		if strings.HasPrefix(id, prefix) || e.record.Metadata["job_id"] == jobID {
			delete(b.entries, id)
		}
	}
	return nil
}

func matchesFilters(r kowloon.Record, filters map[string]string) bool {
	for k, v := range filters {
		if r.Metadata[k] != v {
			return false
		}
	}
	return true
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
