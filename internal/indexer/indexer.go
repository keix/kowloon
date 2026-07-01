// Package indexer is the orchestration layer. It composes the four
// seams (source → schema → embed → backend) so the kowloon-api HTTP
// service has one concrete Service implementation to wire up. Lady
// Glass's index-kowloon stage POSTs a URI and Kowloon ends with vectors
// in Milvus; everything in between is this package's job.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/backend"
	"github.com/keix/kowloon/internal/embed"
	"github.com/keix/kowloon/internal/schema"
	"github.com/keix/kowloon/internal/source"
)

type Indexer struct {
	Source   source.Source
	Schemas  map[string]schema.Schema
	Embedder embed.Provider
	Backend  backend.Index

	// Now is the clock used for IndexedAt and IndexJobID. Tests
	// override it to make outputs deterministic.
	Now func() time.Time
}

// New constructs an Indexer with sane defaults. The Schemas map is
// keyed by IndexResultRequest.SchemaVersion ("transactions.v1", …);
// IndexResult returns ErrBadRequest when an unregistered version
// arrives.
func New(src source.Source, schemas map[string]schema.Schema, emb embed.Provider, be backend.Index) *Indexer {
	return &Indexer{
		Source:   src,
		Schemas:  schemas,
		Embedder: emb,
		Backend:  be,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (i *Indexer) IndexResult(ctx context.Context, req kowloon.IndexResultRequest) (kowloon.IndexResultResponse, error) {
	sch, ok := i.Schemas[req.SchemaVersion]
	if !ok {
		return kowloon.IndexResultResponse{}, kowloon.ErrBadRequest{
			Err: fmt.Errorf("no schema registered for %s", req.SchemaVersion),
		}
	}

	raw, err := i.Source.Read(ctx, req.ResultURI)
	if err != nil {
		return kowloon.IndexResultResponse{}, fmt.Errorf("source read %s: %w", req.ResultURI, err)
	}

	records, err := sch.Convert(raw, req)
	if err != nil {
		return kowloon.IndexResultResponse{}, fmt.Errorf("schema convert: %w", err)
	}

	// job_id is the pivot DeleteByJob runs against on both backends —
	// enforce it at this layer so future schemas cannot forget to set it.
	for idx := range records {
		if records[idx].Metadata == nil {
			records[idx].Metadata = map[string]string{}
		}
		records[idx].Metadata["job_id"] = req.JobID
	}

	collection := collectionFor(req.ResultType)
	indexJobID := fmt.Sprintf("kidx_%d", i.Now().UnixNano())

	if len(records) == 0 {
		i.logIndex(req, 0, 0)
		return kowloon.IndexResultResponse{
			Status:            "indexed",
			KowloonCollection: collection,
			IndexJobID:        indexJobID,
			VectorCount:       0,
			EmbeddingModel:    i.Embedder.Model(),
			IndexedAt:         i.Now(),
		}, nil
	}

	texts := make([]string, len(records))
	for idx, r := range records {
		texts[idx] = r.Text
	}
	embedded, err := i.Embedder.Embed(ctx, texts)
	if err != nil {
		return kowloon.IndexResultResponse{}, fmt.Errorf("embed: %w", err)
	}
	if len(embedded.Vectors) != len(records) {
		return kowloon.IndexResultResponse{}, fmt.Errorf("embed: got %d vectors for %d records", len(embedded.Vectors), len(records))
	}

	if err := i.Backend.Upsert(ctx, records, embedded.Vectors); err != nil {
		return kowloon.IndexResultResponse{}, fmt.Errorf("backend upsert: %w", err)
	}

	i.logIndex(req, len(records), embedded.CacheHits)

	return kowloon.IndexResultResponse{
		Status:            "indexed",
		KowloonCollection: collection,
		IndexJobID:        indexJobID,
		VectorCount:       len(records),
		EmbeddingModel:    i.Embedder.Model(),
		IndexedAt:         i.Now(),
	}, nil
}

// logIndex emits the structured line the operator watches for cost /
// cache visibility. embedded = records-cached: without a cache wrap
// the provider reports 0 hits so embedded==records, which is the
// right semantic for "no cache in the chain".
func (i *Indexer) logIndex(req kowloon.IndexResultRequest, records, cached int) {
	log.Printf("index_result job_id=%s result_uri=%s records=%d embedded=%d cached=%d model=%s dimensions=%d",
		req.JobID, req.ResultURI, records, records-cached, cached, i.Embedder.Model(), i.Embedder.Dim())
}

func (i *Indexer) Search(ctx context.Context, req kowloon.SearchRequest) (kowloon.SearchResponse, error) {
	embedded, err := i.Embedder.Embed(ctx, []string{req.Text})
	if err != nil {
		return kowloon.SearchResponse{}, fmt.Errorf("embed query: %w", err)
	}
	if len(embedded.Vectors) == 0 {
		return kowloon.SearchResponse{}, errors.New("embed returned no vectors")
	}

	matches, err := i.Backend.Search(ctx, req, embedded.Vectors[0])
	if err != nil {
		return kowloon.SearchResponse{}, fmt.Errorf("backend search: %w", err)
	}
	return kowloon.SearchResponse{Matches: matches}, nil
}

func (i *Indexer) ResolveMerchant(ctx context.Context, req kowloon.ResolveMerchantRequest) (kowloon.ResolveMerchantResponse, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}

	resp, err := i.Search(ctx, kowloon.SearchRequest{
		RecordType: kowloon.RecordTypeTransaction,
		Text:       req.MerchantRaw,
		TopK:       topK,
		Filters:    req.Context,
	})
	if err != nil {
		return kowloon.ResolveMerchantResponse{}, err
	}

	canonical, confidence := chooseMerchant(resp.Matches)
	return kowloon.ResolveMerchantResponse{
		Canonical:  canonical,
		Confidence: confidence,
		Evidence:   resp.Matches,
	}, nil
}

func (i *Indexer) DeleteJob(ctx context.Context, jobID string) error {
	return i.Backend.DeleteByJob(ctx, jobID)
}

func collectionFor(t kowloon.ResultType) string {
	return string(t)
}

// chooseMerchant aggregates score per canonical name and returns the
// one with the highest summed score. Confidence is that winner's share
// of the total — bounded to [0, 1] and 0 when no match exposed a
// canonical name in its metadata.
func chooseMerchant(matches []kowloon.Match) (string, float64) {
	scores := map[string]float64{}
	for _, m := range matches {
		canonical := strings.TrimSpace(m.Record.Metadata["merchant_normalized"])
		if canonical == "" {
			continue
		}
		scores[canonical] += m.Score
	}
	if len(scores) == 0 {
		return "", 0
	}

	type entry struct {
		name  string
		score float64
	}
	entries := make([]entry, 0, len(scores))
	total := 0.0
	for name, score := range scores {
		entries = append(entries, entry{name, score})
		total += score
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].score > entries[j].score })

	confidence := 0.0
	if total > 0 {
		confidence = entries[0].score / total
	}
	return entries[0].name, confidence
}
