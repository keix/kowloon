package cache

import (
	"context"
	"fmt"

	"github.com/keix/kowloon/internal/embed"
)

// Wrap returns an embed.Provider that consults the given Cache before
// calling the underlying provider. Missed texts hit the underlying
// provider in one batch; hits skip it entirely. The returned
// embed.Result reports CacheHits / CacheMisses so the indexer can log
// them.
func Wrap(inner embed.Provider, c Cache) embed.Provider {
	return &cachedProvider{inner: inner, cache: c}
}

type cachedProvider struct {
	inner embed.Provider
	cache Cache
}

func (p *cachedProvider) Model() string { return p.inner.Model() }
func (p *cachedProvider) Dim() int      { return p.inner.Dim() }

func (p *cachedProvider) Embed(ctx context.Context, texts []string) (embed.Result, error) {
	if len(texts) == 0 {
		return embed.Result{}, nil
	}

	model := p.inner.Model()
	dim := p.inner.Dim()

	vectors := make([][]float32, len(texts))
	var missIdx []int
	var missTexts []string
	hits := 0

	for i, t := range texts {
		key := MakeKey(model, dim, t)
		vec, ok, err := p.cache.Get(ctx, key)
		if err != nil {
			return embed.Result{}, fmt.Errorf("cache get: %w", err)
		}
		if ok {
			vectors[i] = vec
			hits++
			continue
		}
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, t)
	}

	if len(missTexts) > 0 {
		fresh, err := p.inner.Embed(ctx, missTexts)
		if err != nil {
			return embed.Result{}, err
		}
		if len(fresh.Vectors) != len(missTexts) {
			return embed.Result{}, fmt.Errorf("cache wrap: got %d vectors for %d misses",
				len(fresh.Vectors), len(missTexts))
		}
		for j, i := range missIdx {
			vec := fresh.Vectors[j]
			vectors[i] = vec
			key := MakeKey(model, dim, missTexts[j])
			if err := p.cache.Put(ctx, key, vec); err != nil {
				return embed.Result{}, fmt.Errorf("cache put: %w", err)
			}
		}
	}

	return embed.Result{
		Vectors:     vectors,
		CacheHits:   hits,
		CacheMisses: len(missTexts),
	}, nil
}
