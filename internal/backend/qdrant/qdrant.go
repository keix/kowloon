// Package qdrant implements backend.Index against Qdrant. It talks
// directly to the REST API over net/http — the qdrant-go-client SDK is
// intentionally not pulled in because Kowloon's usage is a handful of
// endpoints (collection lifecycle + points upsert/query/delete) and the
// REST surface is small enough that owning it is cheaper than
// depending on the SDK's generated types.
//
// One collection holds all records of a given result_type — the
// collection name mirrors the ResultType ("transactions" today). Point
// IDs are UUIDv5-derived from Kowloon's string record IDs so an
// upsert against the same record ID overwrites deterministically,
// which is the property re-index depends on.
package qdrant

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/keix/kowloon"
)

// indexedFields are the payload keys promoted to keyword-indexed
// columns so filter expressions are fast. Everything else in the
// record's metadata is still stored on the payload and filterable —
// just without the index.
var indexedFields = []string{
	"tenant_id",
	"job_id",
	"record_type",
	"year_month",
	"merchant",
	"merchant_normalized",
	"foreign_currency",
	"country",
	"category",
	"import_batch_id",
}

// structuralPayloadKeys are the payload keys that carry Kowloon's
// record shape rather than caller-supplied metadata. Search response
// extraction lifts these out into Record fields; everything else
// falls into Record.Metadata.
var structuralPayloadKeys = map[string]struct{}{
	"original_id":  {},
	"record_type":  {},
	"text":         {},
	"source_uri":   {},
	"source_index": {},
}

type Config struct {
	// Endpoint is the base URL of the Qdrant cluster, e.g.
	// https://xxxx.cloud.qdrant.io:6333 (Qdrant Cloud) or
	// http://127.0.0.1:6333 (local docker).
	Endpoint string

	// APIKey is the "api-key" header value. Empty means no auth,
	// which is fine for local dev.
	APIKey string

	// Collection defaults to "transactions".
	Collection string

	// Dim is the vector dimensionality. Must match the embedder.
	Dim int

	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
}

type Backend struct {
	endpoint   string
	apiKey     string
	collection string
	dim        int
	httpClient *http.Client

	initOnce sync.Once
	initErr  error
}

func New(cfg Config) *Backend {
	if cfg.Collection == "" {
		cfg.Collection = "transactions"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Backend{
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:     cfg.APIKey,
		collection: cfg.Collection,
		dim:        cfg.Dim,
		httpClient: cfg.HTTPClient,
	}
}

// Ensure makes the collection + payload indexes idempotent. main.go
// calls it on startup so the first Upsert / Search does not pay the
// schema-creation cost; subsequent methods also call ensure so
// out-of-order init is still safe.
func (b *Backend) Ensure(ctx context.Context) error {
	b.initOnce.Do(func() {
		b.initErr = b.bootstrap(ctx)
	})
	return b.initErr
}

func (b *Backend) bootstrap(ctx context.Context) error {
	if b.dim <= 0 {
		return fmt.Errorf("qdrant: dim must be > 0, got %d", b.dim)
	}

	exists, err := b.collectionExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if err := b.createCollection(ctx); err != nil {
			return err
		}
	}
	for _, field := range indexedFields {
		if err := b.ensurePayloadIndex(ctx, field); err != nil {
			return fmt.Errorf("qdrant: payload index %s: %w", field, err)
		}
	}
	return nil
}

func (b *Backend) collectionExists(ctx context.Context) (bool, error) {
	var resp struct {
		Result struct {
			Exists bool `json:"exists"`
		} `json:"result"`
	}
	if err := b.do(ctx, http.MethodGet, "/collections/"+b.collection+"/exists", nil, &resp); err != nil {
		return false, fmt.Errorf("qdrant: collection exists: %w", err)
	}
	return resp.Result.Exists, nil
}

func (b *Backend) createCollection(ctx context.Context) error {
	body := map[string]any{
		"vectors": map[string]any{
			"size":     b.dim,
			"distance": "Cosine",
		},
	}
	if err := b.do(ctx, http.MethodPut, "/collections/"+b.collection, body, nil); err != nil {
		return fmt.Errorf("qdrant: create collection: %w", err)
	}
	return nil
}

func (b *Backend) ensurePayloadIndex(ctx context.Context, field string) error {
	body := map[string]any{
		"field_name":   field,
		"field_schema": "keyword",
	}
	// Qdrant returns 200 whether the index already exists or is
	// newly created, so this call is idempotent.
	return b.do(ctx, http.MethodPut, "/collections/"+b.collection+"/index?wait=true", body, nil)
}

func (b *Backend) Upsert(ctx context.Context, records []kowloon.Record, vectors [][]float32) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	if len(records) != len(vectors) {
		return fmt.Errorf("qdrant: %d records, %d vectors", len(records), len(vectors))
	}
	if len(records) == 0 {
		return nil
	}

	points := make([]map[string]any, len(records))
	for i, r := range records {
		payload := map[string]any{
			"original_id":  r.ID,
			"record_type":  string(r.RecordType),
			"text":         r.Text,
			"source_uri":   r.SourceURI,
			"source_index": r.SourceIndex,
		}
		for k, v := range r.Metadata {
			payload[k] = v
		}
		points[i] = map[string]any{
			"id":      recordIDToPointID(r.ID),
			"vector":  vectors[i],
			"payload": payload,
		}
	}

	body := map[string]any{"points": points}
	if err := b.do(ctx, http.MethodPut, "/collections/"+b.collection+"/points?wait=true", body, nil); err != nil {
		return fmt.Errorf("qdrant: upsert: %w", err)
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

	body := map[string]any{
		"query":        vector,
		"limit":        topK,
		"with_payload": true,
	}
	if f := buildFilter(query); f != nil {
		body["filter"] = f
	}

	var resp struct {
		Result struct {
			Points []qdrantPoint `json:"points"`
		} `json:"result"`
	}
	if err := b.do(ctx, http.MethodPost, "/collections/"+b.collection+"/points/query", body, &resp); err != nil {
		return nil, fmt.Errorf("qdrant: search: %w", err)
	}
	return extractMatches(resp.Result.Points), nil
}

func (b *Backend) DeleteByJob(ctx context.Context, jobID string) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	body := map[string]any{
		"filter": map[string]any{
			"must": []map[string]any{
				{"key": "job_id", "match": map[string]any{"value": jobID}},
			},
		},
	}
	if err := b.do(ctx, http.MethodPost, "/collections/"+b.collection+"/points/delete?wait=true", body, nil); err != nil {
		return fmt.Errorf("qdrant: delete by job %s: %w", jobID, err)
	}
	return nil
}

// buildFilter maps SearchRequest into a Qdrant filter. record_type is
// promoted to its own condition; every entry in Filters becomes an
// exact-match keyword condition. Qdrant treats all payload keys
// uniformly, so unlike Milvus there is no scalar-vs-JSON distinction
// to preserve here.
func buildFilter(q kowloon.SearchRequest) map[string]any {
	var must []map[string]any
	if q.RecordType != "" {
		must = append(must, map[string]any{
			"key":   "record_type",
			"match": map[string]any{"value": string(q.RecordType)},
		})
	}
	for k, v := range q.Filters {
		must = append(must, map[string]any{
			"key":   k,
			"match": map[string]any{"value": v},
		})
	}
	if len(must) == 0 {
		return nil
	}
	return map[string]any{"must": must}
}

type qdrantPoint struct {
	ID      any            `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

func extractMatches(points []qdrantPoint) []kowloon.Match {
	matches := make([]kowloon.Match, 0, len(points))
	for _, p := range points {
		rec := kowloon.Record{}
		if v, ok := p.Payload["original_id"].(string); ok {
			rec.ID = v
		}
		if v, ok := p.Payload["record_type"].(string); ok {
			rec.RecordType = kowloon.RecordType(v)
		}
		if v, ok := p.Payload["text"].(string); ok {
			rec.Text = v
		}
		if v, ok := p.Payload["source_uri"].(string); ok {
			rec.SourceURI = v
		}
		if v, ok := p.Payload["source_index"].(float64); ok {
			rec.SourceIndex = int(v)
		}

		metadata := make(map[string]string)
		for k, v := range p.Payload {
			if _, isStructural := structuralPayloadKeys[k]; isStructural {
				continue
			}
			if s, ok := v.(string); ok {
				metadata[k] = s
			}
		}
		if len(metadata) > 0 {
			rec.Metadata = metadata
		}

		matches = append(matches, kowloon.Match{Record: rec, Score: p.Score})
	}
	return matches
}

func (b *Backend) do(ctx context.Context, method, path string, body, dst any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.apiKey != "" {
		req.Header.Set("api-key", b.apiKey)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, string(payload))
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// recordIDToPointID computes a deterministic UUIDv5 from the record
// ID. Qdrant point IDs must be either UUIDs or uint64; our string
// record IDs like "job_123:tx:42" would otherwise not fit. UUIDv5
// gives stability across re-index, which is what the upsert-overwrite
// contract needs.
func recordIDToPointID(id string) string {
	h := sha1.New()
	h.Write(qdrantNamespaceUUID[:])
	h.Write([]byte(id))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// qdrantNamespaceUUID is the RFC 4122 DNS namespace UUID. Any fixed
// value works — pinning to the standard one keeps the derivation
// obvious to a future reader.
var qdrantNamespaceUUID = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}
