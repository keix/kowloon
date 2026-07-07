package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/embed"
	idemmem "github.com/keix/kowloon/internal/idempotency/memory"
	"github.com/keix/kowloon/internal/schema"
)

type fakeSource struct {
	data map[string][]byte
	err  error
}

func (f *fakeSource) Read(_ context.Context, uri string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.data[uri]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

type fakeSchema struct{}

func (fakeSchema) Revision() string { return "test" }

func (fakeSchema) Convert(raw []byte, req kowloon.IndexResultRequest) ([]kowloon.Record, error) {
	var texts []string
	if err := json.Unmarshal(raw, &texts); err != nil {
		return nil, err
	}
	out := make([]kowloon.Record, len(texts))
	for i, s := range texts {
		out[i] = kowloon.Record{
			ID:          req.JobID + ":tx:" + itoa(i),
			RecordType:  kowloon.RecordTypeTransaction,
			Text:        s,
			SourceURI:   req.ResultURI,
			SourceIndex: i,
			Metadata:    map[string]string{"tenant_id": req.TenantID},
		}
	}
	return out, nil
}

type fakeEmbedder struct {
	dim   int
	calls [][]string
	err   error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) (embed.Result, error) {
	if f.err != nil {
		return embed.Result{}, f.err
	}
	f.calls = append(f.calls, append([]string(nil), texts...))
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		for j := range v {
			v[j] = float32(i*10 + j)
		}
		out[i] = v
	}
	return embed.Result{Vectors: out}, nil
}

func (f *fakeEmbedder) Model() string { return "fake-test" }
func (f *fakeEmbedder) Dim() int      { return f.dim }

type upsertCall struct {
	records []kowloon.Record
	vectors [][]float32
}

type fakeBackend struct {
	upserts []upsertCall
	delJobs []string
	matches []kowloon.Match
}

func (b *fakeBackend) Upsert(_ context.Context, records []kowloon.Record, vectors [][]float32) error {
	b.upserts = append(b.upserts, upsertCall{records, vectors})
	return nil
}

func (b *fakeBackend) Search(_ context.Context, _ kowloon.SearchRequest, _ []float32) ([]kowloon.Match, error) {
	return b.matches, nil
}

func (b *fakeBackend) DeleteByJob(_ context.Context, jobID string) error {
	b.delJobs = append(b.delJobs, jobID)
	return nil
}

func newTestIndexer(src *fakeSource, emb *fakeEmbedder, be *fakeBackend) *Indexer {
	ix := New(src, map[string]schema.Schema{"transactions.v1": fakeSchema{}}, emb, be)
	ix.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return ix
}

func TestIndexResult_Happy(t *testing.T) {
	src := &fakeSource{data: map[string][]byte{
		"s3://b/k.json": []byte(`["row1","row2","row3"]`),
	}}
	emb := &fakeEmbedder{dim: 4}
	be := &fakeBackend{}
	ix := newTestIndexer(src, emb, be)

	resp, err := ix.IndexResult(context.Background(), kowloon.IndexResultRequest{
		JobID:         "job_42",
		TenantID:      "keix",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.VectorCount != 3 {
		t.Errorf("VectorCount=%d, want 3", resp.VectorCount)
	}
	if resp.EmbeddingModel != "fake-test" {
		t.Errorf("EmbeddingModel=%q", resp.EmbeddingModel)
	}
	if resp.KowloonCollection != "transactions" {
		t.Errorf("KowloonCollection=%q", resp.KowloonCollection)
	}
	if !strings.HasPrefix(resp.IndexJobID, "kidx_") {
		t.Errorf("IndexJobID=%q, want kidx_*", resp.IndexJobID)
	}
	if resp.Status != "indexed" {
		t.Errorf("Status=%q", resp.Status)
	}

	if len(be.upserts) != 1 {
		t.Fatalf("upserts=%d, want 1", len(be.upserts))
	}
	if len(be.upserts[0].records) != 3 {
		t.Errorf("upserted records=%d, want 3", len(be.upserts[0].records))
	}
	if len(be.upserts[0].vectors) != 3 || len(be.upserts[0].vectors[0]) != 4 {
		t.Errorf("vectors shape: %d records, %d dim",
			len(be.upserts[0].vectors), len(be.upserts[0].vectors[0]))
	}
}

func TestIndexResult_UnknownSchema(t *testing.T) {
	ix := New(&fakeSource{}, map[string]schema.Schema{}, &fakeEmbedder{dim: 2}, &fakeBackend{})
	_, err := ix.IndexResult(context.Background(), kowloon.IndexResultRequest{
		JobID:         "j",
		TenantID:      "t",
		ResultURI:     "s3://b/k.json",
		ResultType:    "unknown",
		SchemaVersion: "unknown.v1",
	})
	if err == nil {
		t.Fatal("want error")
	}
	var bad kowloon.ErrBadRequest
	if !errors.As(err, &bad) {
		t.Errorf("err type=%T, want kowloon.ErrBadRequest: %v", err, err)
	}
}

func TestIndexResult_EmptyRecords(t *testing.T) {
	src := &fakeSource{data: map[string][]byte{
		"s3://b/k.json": []byte(`[]`),
	}}
	emb := &fakeEmbedder{dim: 2}
	be := &fakeBackend{}
	ix := newTestIndexer(src, emb, be)

	resp, err := ix.IndexResult(context.Background(), kowloon.IndexResultRequest{
		JobID:         "j",
		TenantID:      "t",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.VectorCount != 0 {
		t.Errorf("VectorCount=%d, want 0", resp.VectorCount)
	}
	if len(emb.calls) != 0 {
		t.Errorf("Embed called %d times on empty records", len(emb.calls))
	}
	if len(be.upserts) != 0 {
		t.Errorf("Upsert called %d times on empty records", len(be.upserts))
	}
}

func TestSearch_EmbedThenBackend(t *testing.T) {
	be := &fakeBackend{
		matches: []kowloon.Match{
			{Record: kowloon.Record{ID: "r1"}, Score: 0.9},
		},
	}
	emb := &fakeEmbedder{dim: 3}
	ix := newTestIndexer(&fakeSource{}, emb, be)

	resp, err := ix.Search(context.Background(), kowloon.SearchRequest{Text: "hello", TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.calls) != 1 || emb.calls[0][0] != "hello" {
		t.Errorf("embed calls=%v", emb.calls)
	}
	if len(resp.Matches) != 1 || resp.Matches[0].Record.ID != "r1" {
		t.Errorf("matches=%+v", resp.Matches)
	}
}

func TestResolveMerchant_PicksHighestSummedScore(t *testing.T) {
	be := &fakeBackend{
		matches: []kowloon.Match{
			{Record: kowloon.Record{Metadata: map[string]string{"merchant_normalized": "FamilyMart"}}, Score: 0.6},
			{Record: kowloon.Record{Metadata: map[string]string{"merchant_normalized": "FamilyMart"}}, Score: 0.5},
			{Record: kowloon.Record{Metadata: map[string]string{"merchant_normalized": "Seven"}}, Score: 0.4},
		},
	}
	ix := newTestIndexer(&fakeSource{}, &fakeEmbedder{dim: 2}, be)

	resp, err := ix.ResolveMerchant(context.Background(), kowloon.ResolveMerchantRequest{MerchantRaw: "FM KLCC"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Canonical != "FamilyMart" {
		t.Errorf("Canonical=%q, want FamilyMart", resp.Canonical)
	}
	if resp.Confidence <= 0 || resp.Confidence > 1 {
		t.Errorf("Confidence=%v, want (0,1]", resp.Confidence)
	}
	if len(resp.Evidence) != 3 {
		t.Errorf("Evidence=%d, want 3", len(resp.Evidence))
	}
}

func TestResolveMerchant_NoCanonical(t *testing.T) {
	be := &fakeBackend{
		matches: []kowloon.Match{
			{Record: kowloon.Record{Metadata: map[string]string{}}, Score: 0.9},
		},
	}
	ix := newTestIndexer(&fakeSource{}, &fakeEmbedder{dim: 2}, be)

	resp, err := ix.ResolveMerchant(context.Background(), kowloon.ResolveMerchantRequest{MerchantRaw: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Canonical != "" || resp.Confidence != 0 {
		t.Errorf("got (%q, %v), want empty/0", resp.Canonical, resp.Confidence)
	}
}

func TestDeleteJob(t *testing.T) {
	be := &fakeBackend{}
	ix := newTestIndexer(&fakeSource{}, &fakeEmbedder{dim: 1}, be)

	if err := ix.DeleteJob(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	if len(be.delJobs) != 1 || be.delJobs[0] != "job_x" {
		t.Errorf("delJobs=%v", be.delJobs)
	}
}

func TestIdempotency_SecondPostSkipsAllWork(t *testing.T) {
	src := &fakeSource{data: map[string][]byte{
		"s3://b/k.json": []byte(`["row1","row2","row3"]`),
	}}
	emb := &fakeEmbedder{dim: 4}
	be := &fakeBackend{}
	ix := newTestIndexer(src, emb, be)
	ix.Idempotency = idemmem.New()

	req := kowloon.IndexResultRequest{
		JobID:         "job_42",
		TenantID:      "keix",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	}

	first, err := ix.IndexResult(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.VectorCount != 3 {
		t.Errorf("first VectorCount=%d, want 3", first.VectorCount)
	}
	if len(be.upserts) != 1 || len(emb.calls) != 1 {
		t.Fatalf("first run should embed+upsert once, embed_calls=%d upserts=%d", len(emb.calls), len(be.upserts))
	}

	second, err := ix.IndexResult(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.calls) != 1 {
		t.Errorf("second run should not embed, embed_calls=%d", len(emb.calls))
	}
	if len(be.upserts) != 1 {
		t.Errorf("second run should not upsert, upserts=%d", len(be.upserts))
	}
	if second.IndexJobID != first.IndexJobID {
		t.Errorf("second IndexJobID=%q, want same as first %q", second.IndexJobID, first.IndexJobID)
	}
	if second.VectorCount != first.VectorCount {
		t.Errorf("second VectorCount=%d, want %d", second.VectorCount, first.VectorCount)
	}
}

func TestIdempotency_DifferentContentIsFreshRun(t *testing.T) {
	src := &fakeSource{data: map[string][]byte{
		"s3://b/k.json": []byte(`["row1","row2"]`),
	}}
	emb := &fakeEmbedder{dim: 4}
	be := &fakeBackend{}
	ix := newTestIndexer(src, emb, be)
	ix.Idempotency = idemmem.New()

	req := kowloon.IndexResultRequest{
		JobID:         "job_42",
		TenantID:      "keix",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	}

	if _, err := ix.IndexResult(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	// Same URI but different content — content_hash catches this.
	src.data["s3://b/k.json"] = []byte(`["row1","row2","row3"]`)

	if _, err := ix.IndexResult(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(emb.calls) != 2 {
		t.Errorf("changed content should trigger fresh embed, embed_calls=%d, want 2", len(emb.calls))
	}
}

func TestIdempotency_Nil_AlwaysReRuns(t *testing.T) {
	src := &fakeSource{data: map[string][]byte{
		"s3://b/k.json": []byte(`["row1","row2"]`),
	}}
	emb := &fakeEmbedder{dim: 4}
	be := &fakeBackend{}
	ix := newTestIndexer(src, emb, be) // Idempotency stays nil

	req := kowloon.IndexResultRequest{
		JobID:         "job_42",
		TenantID:      "keix",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	}

	if _, err := ix.IndexResult(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.IndexResult(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(emb.calls) != 2 {
		t.Errorf("without idempotency, both runs should embed, embed_calls=%d, want 2", len(emb.calls))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	k := len(buf)
	for i > 0 {
		k--
		buf[k] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[k:])
}
