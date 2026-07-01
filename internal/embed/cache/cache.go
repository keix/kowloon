// Package cache wraps an embed.Provider so identical
// (model, dimensions, text) triples are not embedded twice. The
// wrap tracks its own hit/miss counts and reports them on
// embed.Result so the indexer's structured log can distinguish
// freshly embedded records from cache-served ones.
//
// The memory implementation is the v0 default; a persistent backend
// (DynamoDB) can slot in behind the same Cache interface later
// without touching the wrap or the indexer.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Key names the (model, dimensions, text) triple a cached vector
// belongs to. Model + Dimensions are load-bearing: switching
// text-embedding-3-large from 3072 to 1536 dimensions produces
// different vectors for the same text, so the cache MUST invalidate.
type Key struct {
	Model      string
	Dimensions int
	TextHash   string
}

// String returns the deterministic key form the cache stores under.
func (k Key) String() string {
	return k.Model + ":" + strconv.Itoa(k.Dimensions) + ":" + k.TextHash
}

// MakeKey hashes the text once and packs the identifying triple.
// SHA-256 is the hash because embedded texts are short enough that
// collision risk is negligible while the fixed-length hex is easy to
// key on across backends (memory, DynamoDB, S3).
func MakeKey(model string, dim int, text string) Key {
	h := sha256.Sum256([]byte(text))
	return Key{
		Model:      model,
		Dimensions: dim,
		TextHash:   hex.EncodeToString(h[:]),
	}
}

// Cache is the read-through cache the wrap consults. Implementations
// must be safe for concurrent use.
type Cache interface {
	Get(ctx context.Context, key Key) (vector []float32, ok bool, err error)
	Put(ctx context.Context, key Key, vector []float32) error
}
