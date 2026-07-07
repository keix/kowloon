package memory

import (
	"context"
	"testing"
	"time"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/idempotency"
)

func req() kowloon.IndexResultRequest {
	return kowloon.IndexResultRequest{
		JobID:         "j",
		ResultURI:     "s3://b/k",
		SchemaVersion: "transactions.v1",
	}
}

func resp(kidx string, count int) kowloon.IndexResultResponse {
	return kowloon.IndexResultResponse{
		Status:            "indexed",
		KowloonCollection: "transactions",
		IndexJobID:        kidx,
		VectorCount:       count,
		EmbeddingModel:    "text-embedding-3-large",
		IndexedAt:         time.Unix(1_700_000_000, 0).UTC(),
	}
}

func TestLookupMiss(t *testing.T) {
	s := New()
	k := idempotency.MakeKey(req(), "rev", "m", 128, []byte("x"))
	_, ok, err := s.Lookup(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("want miss on empty store")
	}
}

func TestSaveLookup(t *testing.T) {
	s := New()
	ctx := context.Background()
	k := idempotency.MakeKey(req(), "rev", "m", 128, []byte("x"))
	r := resp("kidx_1", 33)

	if err := s.Save(ctx, k, r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Lookup(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want hit")
	}
	if got.IndexJobID != "kidx_1" || got.VectorCount != 33 {
		t.Errorf("got=%+v, want kidx_1/33", got)
	}
}

func TestSaveOverwrite(t *testing.T) {
	s := New()
	ctx := context.Background()
	k := idempotency.MakeKey(req(), "rev", "m", 128, []byte("x"))

	_ = s.Save(ctx, k, resp("kidx_a", 10))
	_ = s.Save(ctx, k, resp("kidx_b", 20))

	got, _, _ := s.Lookup(ctx, k)
	if got.IndexJobID != "kidx_b" || got.VectorCount != 20 {
		t.Errorf("want kidx_b/20, got %+v", got)
	}
	if s.Len() != 1 {
		t.Errorf("Len()=%d, want 1", s.Len())
	}
}

func TestDifferentKeysIsolated(t *testing.T) {
	s := New()
	ctx := context.Background()

	k1 := idempotency.MakeKey(req(), "rev", "m", 128, []byte("a"))
	k2 := idempotency.MakeKey(req(), "rev", "m", 128, []byte("b"))

	_ = s.Save(ctx, k1, resp("kidx_a", 10))
	_ = s.Save(ctx, k2, resp("kidx_b", 20))

	g1, _, _ := s.Lookup(ctx, k1)
	g2, _, _ := s.Lookup(ctx, k2)
	if g1.IndexJobID != "kidx_a" || g2.IndexJobID != "kidx_b" {
		t.Errorf("isolation broken: %v vs %v", g1, g2)
	}
}
