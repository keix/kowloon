// Package milvus is the production implementation of backend.Index.
// It owns one Milvus collection per result_type and stores both the
// vector and the structured metadata Lady Glass aggregates on.
//
// Schema sketch (transactions collection):
//
//	id            VARCHAR(256) PRIMARY KEY     (record ID "<job>:<kind>:<idx>")
//	record_type   VARCHAR(64)                  ("transaction", "chunk", …)
//	tenant_id     VARCHAR(64)                  filter
//	job_id        VARCHAR(128)                 filter + delete pivot
//	year_month    VARCHAR(16)                  filter ("2026-06")
//	text          VARCHAR(4096)                output, what was embedded
//	source_uri    VARCHAR(1024)                output
//	source_index  INT64                        output
//	metadata      JSON                         output + ad-hoc filters
//	vector        FLOAT_VECTOR(dim)            COSINE / HNSW
//
// The four scalar pivots (record_type, tenant_id, job_id, year_month)
// cover the planned filter dimensions; everything else lives in
// metadata JSON and can still be filtered via Milvus 2.4 JSON paths
// (metadata["foo"] == "bar") when callers need it.
package milvus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	mclient "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"

	"github.com/keix/kowloon"
)

// Client is the subset of mclient.Client the backend uses. Splitting
// it out keeps tests fakeable without spinning up a Milvus stack —
// integration testing against real Milvus is the milvus-probe + a
// running compose stack, not unit tests.
type Client interface {
	HasCollection(ctx context.Context, collName string) (bool, error)
	CreateCollection(ctx context.Context, schema *entity.Schema, shardsNum int32, opts ...mclient.CreateCollectionOption) error
	DescribeIndex(ctx context.Context, collName string, fieldName string, opts ...mclient.IndexOption) ([]entity.Index, error)
	CreateIndex(ctx context.Context, collName string, fieldName string, idx entity.Index, async bool, opts ...mclient.IndexOption) error
	LoadCollection(ctx context.Context, collName string, async bool, opts ...mclient.LoadCollectionOption) error
	Upsert(ctx context.Context, collName string, partitionName string, columns ...entity.Column) (entity.Column, error)
	Search(ctx context.Context, collName string, partitions []string, expr string, outputFields []string,
		vectors []entity.Vector, vectorField string, metricType entity.MetricType, topK int,
		sp entity.SearchParam, opts ...mclient.SearchQueryOptionFunc) ([]mclient.SearchResult, error)
	Delete(ctx context.Context, collName string, partitionName string, expr string) error
}

type Config struct {
	Collection string
	Dim        int
}

type Backend struct {
	client     Client
	collection string
	dim        int

	initOnce sync.Once
	initErr  error
}

func New(c Client, cfg Config) *Backend {
	if cfg.Collection == "" {
		cfg.Collection = "transactions"
	}
	return &Backend{
		client:     c,
		collection: cfg.Collection,
		dim:        cfg.Dim,
	}
}

// Ensure makes the collection + index + load idempotent. main.go calls
// it on startup so the first /v1/index-result does not pay the schema-
// creation cost; subsequent Upsert / Search calls also call ensure so
// out-of-order init is still safe.
func (b *Backend) Ensure(ctx context.Context) error {
	b.initOnce.Do(func() {
		b.initErr = b.bootstrap(ctx)
	})
	return b.initErr
}

func (b *Backend) bootstrap(ctx context.Context) error {
	if b.dim <= 0 {
		return fmt.Errorf("milvus: dim must be > 0, got %d", b.dim)
	}

	has, err := b.client.HasCollection(ctx, b.collection)
	if err != nil {
		return fmt.Errorf("milvus: has collection: %w", err)
	}
	if !has {
		schema := b.schema()
		if err := b.client.CreateCollection(ctx, schema, 1); err != nil {
			return fmt.Errorf("milvus: create collection: %w", err)
		}
	}

	indexes, err := b.client.DescribeIndex(ctx, b.collection, vectorField)
	if err != nil || len(indexes) == 0 {
		idx, err := entity.NewIndexHNSW(entity.COSINE, 8, 64)
		if err != nil {
			return fmt.Errorf("milvus: build index: %w", err)
		}
		if err := b.client.CreateIndex(ctx, b.collection, vectorField, idx, false); err != nil {
			return fmt.Errorf("milvus: create index: %w", err)
		}
	}

	if err := b.client.LoadCollection(ctx, b.collection, false); err != nil {
		return fmt.Errorf("milvus: load collection: %w", err)
	}
	return nil
}

const (
	fieldID          = "id"
	fieldRecordType  = "record_type"
	fieldTenantID    = "tenant_id"
	fieldJobID       = "job_id"
	fieldYearMonth   = "year_month"
	fieldText        = "text"
	fieldSourceURI   = "source_uri"
	fieldSourceIndex = "source_index"
	fieldMetadata    = "metadata"
	vectorField      = "vector"
)

func (b *Backend) schema() *entity.Schema {
	return &entity.Schema{
		CollectionName: b.collection,
		Description:    "Kowloon " + b.collection,
		AutoID:         false,
		Fields: []*entity.Field{
			{Name: fieldID, DataType: entity.FieldTypeVarChar, PrimaryKey: true, AutoID: false, TypeParams: map[string]string{"max_length": "256"}},
			{Name: fieldRecordType, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "64"}},
			{Name: fieldTenantID, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "64"}},
			{Name: fieldJobID, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "128"}},
			{Name: fieldYearMonth, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "16"}},
			{Name: fieldText, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "4096"}},
			{Name: fieldSourceURI, DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "1024"}},
			{Name: fieldSourceIndex, DataType: entity.FieldTypeInt64},
			{Name: fieldMetadata, DataType: entity.FieldTypeJSON},
			{Name: vectorField, DataType: entity.FieldTypeFloatVector, TypeParams: map[string]string{"dim": fmt.Sprintf("%d", b.dim)}},
		},
	}
}

func (b *Backend) Upsert(ctx context.Context, records []kowloon.Record, vectors [][]float32) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	if len(records) != len(vectors) {
		return fmt.Errorf("milvus: %d records, %d vectors", len(records), len(vectors))
	}
	if len(records) == 0 {
		return nil
	}

	ids := make([]string, len(records))
	recordTypes := make([]string, len(records))
	tenants := make([]string, len(records))
	jobs := make([]string, len(records))
	yms := make([]string, len(records))
	texts := make([]string, len(records))
	uris := make([]string, len(records))
	indices := make([]int64, len(records))
	metaJSON := make([][]byte, len(records))

	for i, r := range records {
		ids[i] = r.ID
		recordTypes[i] = string(r.RecordType)
		tenants[i] = r.Metadata["tenant_id"]
		jobs[i] = r.Metadata["job_id"]
		yms[i] = r.Metadata["year_month"]
		texts[i] = r.Text
		uris[i] = r.SourceURI
		indices[i] = int64(r.SourceIndex)
		raw, err := json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("milvus: marshal metadata for %s: %w", r.ID, err)
		}
		metaJSON[i] = raw
	}

	cols := []entity.Column{
		entity.NewColumnVarChar(fieldID, ids),
		entity.NewColumnVarChar(fieldRecordType, recordTypes),
		entity.NewColumnVarChar(fieldTenantID, tenants),
		entity.NewColumnVarChar(fieldJobID, jobs),
		entity.NewColumnVarChar(fieldYearMonth, yms),
		entity.NewColumnVarChar(fieldText, texts),
		entity.NewColumnVarChar(fieldSourceURI, uris),
		entity.NewColumnInt64(fieldSourceIndex, indices),
		entity.NewColumnJSONBytes(fieldMetadata, metaJSON),
		entity.NewColumnFloatVector(vectorField, b.dim, vectors),
	}

	if _, err := b.client.Upsert(ctx, b.collection, "", cols...); err != nil {
		return fmt.Errorf("milvus: upsert: %w", err)
	}
	return nil
}

func (b *Backend) Search(ctx context.Context, query kowloon.SearchRequest, vector []float32) ([]kowloon.Match, error) {
	if err := b.Ensure(ctx); err != nil {
		return nil, err
	}
	topK := query.TopK
	if topK <= 0 {
		topK = 10
	}

	expr := buildExpr(query)

	sp, err := entity.NewIndexHNSWSearchParam(64)
	if err != nil {
		return nil, fmt.Errorf("milvus: search param: %w", err)
	}

	out := []string{fieldID, fieldRecordType, fieldText, fieldSourceURI, fieldSourceIndex, fieldMetadata}
	results, err := b.client.Search(ctx, b.collection, []string{}, expr, out,
		[]entity.Vector{entity.FloatVector(vector)}, vectorField, entity.COSINE, topK, sp)
	if err != nil {
		return nil, fmt.Errorf("milvus: search: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	return extractMatches(results[0])
}

func (b *Backend) DeleteByJob(ctx context.Context, jobID string) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	expr := fmt.Sprintf("%s == %q", fieldJobID, jobID)
	if err := b.client.Delete(ctx, b.collection, "", expr); err != nil {
		return fmt.Errorf("milvus: delete by job %s: %w", jobID, err)
	}
	return nil
}

// buildExpr assembles the Milvus filter expression from a SearchRequest.
// Scalar pivots (record_type, tenant_id, job_id, year_month) match by
// equality; everything else falls through to the JSON metadata field,
// where Milvus 2.4 supports `metadata["foo"] == "bar"` filters.
func buildExpr(q kowloon.SearchRequest) string {
	var conds []string
	if q.RecordType != "" {
		conds = append(conds, fmt.Sprintf("%s == %q", fieldRecordType, string(q.RecordType)))
	}
	for k, v := range q.Filters {
		switch k {
		case fieldTenantID, fieldJobID, fieldYearMonth, fieldRecordType:
			conds = append(conds, fmt.Sprintf("%s == %q", k, v))
		default:
			conds = append(conds, fmt.Sprintf(`%s["%s"] == %q`, fieldMetadata, k, v))
		}
	}
	return strings.Join(conds, " && ")
}

func extractMatches(r mclient.SearchResult) ([]kowloon.Match, error) {
	count := r.ResultCount
	if count == 0 {
		return nil, nil
	}

	cols := indexColumns(r.Fields)
	matches := make([]kowloon.Match, 0, count)
	for i := 0; i < count; i++ {
		id, _ := r.IDs.GetAsString(i)

		rec := kowloon.Record{ID: id}
		if c, ok := cols[fieldRecordType]; ok {
			if v, err := c.GetAsString(i); err == nil {
				rec.RecordType = kowloon.RecordType(v)
			}
		}
		if c, ok := cols[fieldText]; ok {
			rec.Text, _ = c.GetAsString(i)
		}
		if c, ok := cols[fieldSourceURI]; ok {
			rec.SourceURI, _ = c.GetAsString(i)
		}
		if c, ok := cols[fieldSourceIndex]; ok {
			if v, err := c.GetAsInt64(i); err == nil {
				rec.SourceIndex = int(v)
			}
		}
		if c, ok := cols[fieldMetadata]; ok {
			raw, err := c.GetAsString(i)
			if err == nil && raw != "" {
				var m map[string]string
				if err := json.Unmarshal([]byte(raw), &m); err == nil {
					rec.Metadata = m
				}
			}
		}

		var score float64
		if i < len(r.Scores) {
			score = float64(r.Scores[i])
		}
		matches = append(matches, kowloon.Match{Record: rec, Score: score})
	}
	return matches, nil
}

func indexColumns(cols []entity.Column) map[string]entity.Column {
	out := make(map[string]entity.Column, len(cols))
	for _, c := range cols {
		out[c.Name()] = c
	}
	return out
}
