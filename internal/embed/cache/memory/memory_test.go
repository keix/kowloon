package memory

import (
	"context"
	"testing"

	"github.com/keix/kowloon/internal/embed/cache"
)

func TestGetMiss(t *testing.T) {
	c := New(10)
	k := cache.MakeKey("m", 4, "hello")
	_, ok, err := c.Get(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("want miss on empty cache")
	}
}

func TestPutGet(t *testing.T) {
	c := New(10)
	ctx := context.Background()
	k := cache.MakeKey("m", 4, "hello")
	vec := []float32{1, 2, 3, 4}

	if err := c.Put(ctx, k, vec); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want hit")
	}
	if len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Errorf("vec mismatch: %v", got)
	}
}

func TestLRUEviction(t *testing.T) {
	c := New(2)
	ctx := context.Background()

	k1 := cache.MakeKey("m", 1, "a")
	k2 := cache.MakeKey("m", 1, "b")
	k3 := cache.MakeKey("m", 1, "c")

	_ = c.Put(ctx, k1, []float32{1})
	_ = c.Put(ctx, k2, []float32{2})
	_ = c.Put(ctx, k3, []float32{3}) // evicts k1 (oldest)

	if c.Len() != 2 {
		t.Errorf("Len()=%d, want 2", c.Len())
	}
	if _, ok, _ := c.Get(ctx, k1); ok {
		t.Error("k1 should have been evicted")
	}
	if _, ok, _ := c.Get(ctx, k2); !ok {
		t.Error("k2 should still be present")
	}
	if _, ok, _ := c.Get(ctx, k3); !ok {
		t.Error("k3 should be present")
	}
}

func TestLRUGetPromotes(t *testing.T) {
	c := New(2)
	ctx := context.Background()

	k1 := cache.MakeKey("m", 1, "a")
	k2 := cache.MakeKey("m", 1, "b")
	k3 := cache.MakeKey("m", 1, "c")

	_ = c.Put(ctx, k1, []float32{1})
	_ = c.Put(ctx, k2, []float32{2})
	_, _, _ = c.Get(ctx, k1)             // promote k1 to MRU
	_ = c.Put(ctx, k3, []float32{3})     // evicts k2 now, not k1

	if _, ok, _ := c.Get(ctx, k1); !ok {
		t.Error("k1 should have been protected by Get")
	}
	if _, ok, _ := c.Get(ctx, k2); ok {
		t.Error("k2 should have been evicted")
	}
}

func TestPutSameKeyUpdatesValue(t *testing.T) {
	c := New(10)
	ctx := context.Background()
	k := cache.MakeKey("m", 1, "a")

	_ = c.Put(ctx, k, []float32{1})
	_ = c.Put(ctx, k, []float32{99})

	got, ok, _ := c.Get(ctx, k)
	if !ok || got[0] != 99 {
		t.Errorf("want updated value 99, got %v", got)
	}
	if c.Len() != 1 {
		t.Errorf("Len()=%d, want 1 (no duplicate)", c.Len())
	}
}
