package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/keix/kowloon/internal/embed"
)

// fakeCache is an inline map-backed Cache — we cannot import the
// memory package from this test file because memory itself imports
// this package.
type fakeCache struct {
	data map[string][]float32
	err  error
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[string][]float32{}} }

func (c *fakeCache) Get(_ context.Context, key Key) ([]float32, bool, error) {
	if c.err != nil {
		return nil, false, c.err
	}
	v, ok := c.data[key.String()]
	return v, ok, nil
}

func (c *fakeCache) Put(_ context.Context, key Key, vec []float32) error {
	if c.err != nil {
		return c.err
	}
	c.data[key.String()] = vec
	return nil
}

type fakeProvider struct {
	calls [][]string
	err   error
}

func (f *fakeProvider) Model() string { return "fake-model" }
func (f *fakeProvider) Dim() int      { return 4 }

func (f *fakeProvider) Embed(_ context.Context, texts []string) (embed.Result, error) {
	if f.err != nil {
		return embed.Result{}, f.err
	}
	f.calls = append(f.calls, append([]string(nil), texts...))
	vs := make([][]float32, len(texts))
	for i, t := range texts {
		vs[i] = []float32{float32(len(t)), float32(i), 0, 0}
	}
	return embed.Result{Vectors: vs}, nil
}

func TestWrap_EmptyInput(t *testing.T) {
	inner := &fakeProvider{}
	w := Wrap(inner, newFakeCache())
	result, err := w.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Vectors) != 0 {
		t.Errorf("vectors=%v", result.Vectors)
	}
	if len(inner.calls) != 0 {
		t.Errorf("inner should not be called on empty input, calls=%d", len(inner.calls))
	}
}

func TestWrap_AllMisses(t *testing.T) {
	inner := &fakeProvider{}
	w := Wrap(inner, newFakeCache())
	result, err := w.Embed(context.Background(), []string{"a", "bb"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHits != 0 || result.CacheMisses != 2 {
		t.Errorf("hits=%d misses=%d, want 0/2", result.CacheHits, result.CacheMisses)
	}
	if len(inner.calls) != 1 || len(inner.calls[0]) != 2 {
		t.Errorf("expected 1 call with 2 texts, got %v", inner.calls)
	}
}

func TestWrap_AllHits(t *testing.T) {
	inner := &fakeProvider{}
	w := Wrap(inner, newFakeCache())
	ctx := context.Background()

	if _, err := w.Embed(ctx, []string{"a", "bb"}); err != nil {
		t.Fatal(err)
	}
	result, err := w.Embed(ctx, []string{"a", "bb"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHits != 2 || result.CacheMisses != 0 {
		t.Errorf("hits=%d misses=%d, want 2/0", result.CacheHits, result.CacheMisses)
	}
	if len(inner.calls) != 1 {
		t.Errorf("second call should not have hit the inner provider, calls=%d", len(inner.calls))
	}
}

func TestWrap_MixedPreservesOrder(t *testing.T) {
	inner := &fakeProvider{}
	w := Wrap(inner, newFakeCache())
	ctx := context.Background()

	if _, err := w.Embed(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	result, err := w.Embed(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHits != 1 || result.CacheMisses != 2 {
		t.Errorf("hits=%d misses=%d, want 1/2", result.CacheHits, result.CacheMisses)
	}
	if len(inner.calls) != 2 || len(inner.calls[1]) != 2 ||
		inner.calls[1][0] != "b" || inner.calls[1][1] != "c" {
		t.Errorf("second inner call=%v, want [b c] only", inner.calls[1])
	}
	if len(result.Vectors) != 3 {
		t.Fatalf("vectors=%d, want 3", len(result.Vectors))
	}
}

func TestWrap_InnerErrorPropagates(t *testing.T) {
	inner := &fakeProvider{err: errors.New("boom")}
	w := Wrap(inner, newFakeCache())
	_, err := w.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestWrap_CacheGetErrorPropagates(t *testing.T) {
	inner := &fakeProvider{}
	badCache := newFakeCache()
	badCache.err = errors.New("cache down")
	w := Wrap(inner, badCache)
	_, err := w.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestWrap_ModelAndDimForwarded(t *testing.T) {
	inner := &fakeProvider{}
	w := Wrap(inner, newFakeCache())
	if w.Model() != "fake-model" {
		t.Errorf("Model()=%q", w.Model())
	}
	if w.Dim() != 4 {
		t.Errorf("Dim()=%d", w.Dim())
	}
}
