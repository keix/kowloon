package memory

import (
	"context"
	"testing"

	"github.com/keix/kowloon"
)

func TestUpsert_LenMismatch(t *testing.T) {
	b := New()
	err := b.Upsert(context.Background(),
		[]kowloon.Record{{ID: "a"}, {ID: "b"}},
		[][]float32{{1}},
	)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestSearch_OrdersByCosine(t *testing.T) {
	b := New()
	ctx := context.Background()

	records := []kowloon.Record{
		{ID: "j:0", RecordType: kowloon.RecordTypeTransaction, Metadata: map[string]string{"job_id": "j"}},
		{ID: "j:1", RecordType: kowloon.RecordTypeTransaction, Metadata: map[string]string{"job_id": "j"}},
	}
	vectors := [][]float32{
		{1, 0, 0},
		{0, 1, 0},
	}
	if err := b.Upsert(ctx, records, vectors); err != nil {
		t.Fatal(err)
	}

	matches, err := b.Search(ctx, kowloon.SearchRequest{TopK: 2}, []float32{0.9, 0.1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Record.ID != "j:0" {
		t.Errorf("top match=%+v", matches)
	}
	if matches[0].Score <= matches[1].Score {
		t.Errorf("scores not descending: %v", matches)
	}
}

func TestSearch_FiltersByMetadata(t *testing.T) {
	b := New()
	ctx := context.Background()
	_ = b.Upsert(ctx,
		[]kowloon.Record{
			{ID: "j:0", RecordType: kowloon.RecordTypeTransaction, Metadata: map[string]string{"merchant": "FamilyMart"}},
			{ID: "j:1", RecordType: kowloon.RecordTypeTransaction, Metadata: map[string]string{"merchant": "Petronas"}},
		},
		[][]float32{{1, 0}, {1, 0}},
	)

	matches, _ := b.Search(ctx, kowloon.SearchRequest{
		TopK:    10,
		Filters: map[string]string{"merchant": "Petronas"},
	}, []float32{1, 0})
	if len(matches) != 1 || matches[0].Record.ID != "j:1" {
		t.Errorf("filtered=%+v", matches)
	}
}

func TestSearch_FiltersByRecordType(t *testing.T) {
	b := New()
	ctx := context.Background()
	_ = b.Upsert(ctx,
		[]kowloon.Record{
			{ID: "a", RecordType: kowloon.RecordTypeTransaction},
			{ID: "b", RecordType: kowloon.RecordTypeChunk},
		},
		[][]float32{{1}, {1}},
	)

	matches, _ := b.Search(ctx, kowloon.SearchRequest{
		RecordType: kowloon.RecordTypeTransaction,
		TopK:       10,
	}, []float32{1})
	if len(matches) != 1 || matches[0].Record.ID != "a" {
		t.Errorf("record-type filter=%+v", matches)
	}
}

func TestDeleteByJob(t *testing.T) {
	b := New()
	ctx := context.Background()
	_ = b.Upsert(ctx,
		[]kowloon.Record{
			{ID: "j1:0", Metadata: map[string]string{"job_id": "j1"}},
			{ID: "j1:1", Metadata: map[string]string{"job_id": "j1"}},
			{ID: "j2:0", Metadata: map[string]string{"job_id": "j2"}},
		},
		[][]float32{{1}, {2}, {3}},
	)

	if err := b.DeleteByJob(ctx, "j1"); err != nil {
		t.Fatal(err)
	}

	matches, _ := b.Search(ctx, kowloon.SearchRequest{TopK: 100}, []float32{1})
	if len(matches) != 1 || matches[0].Record.ID != "j2:0" {
		t.Errorf("after delete: %+v", matches)
	}
}
