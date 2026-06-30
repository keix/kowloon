// Package kowloon holds the wire types Lady Glass and the kowloon-api
// HTTP server exchange. Interfaces (Index, Source, Schema, EmbeddingProvider)
// live in the internal/* packages so a client importing this root package
// does not pull in S3, OpenAI, or Milvus dependencies.
package kowloon

import "time"

// RecordType is the unit-of-indexing kind: one transaction, one text
// chunk, or one summary. Distinct from ResultType (the shape of the S3
// document the indexer was handed); a single ResultType maps to one
// RecordType after schema conversion.
type RecordType string

const (
	RecordTypeTransaction RecordType = "transaction"
	RecordTypeChunk       RecordType = "chunk"
	RecordTypeSummary     RecordType = "summary"
)

// ResultType identifies the shape of the archived S3 result the
// index-kowloon stage hands Kowloon. The schema converter for the
// (ResultType, SchemaVersion) pair owns the conversion from raw JSON
// bytes to []Record.
type ResultType string

const (
	ResultTypeTransactions ResultType = "transactions"
)

// Record is the unit that lives in the vector backend. Metadata is
// string-typed to match Milvus scalar-field constraints; numerics that
// aggregations care about (amount, foreign_amount) are kept here as the
// canonical string form and parsed on demand at query time.
type Record struct {
	ID          string            `json:"id"`
	RecordType  RecordType        `json:"record_type"`
	Text        string            `json:"text"`
	SourceURI   string            `json:"source_uri"`
	SourceIndex int               `json:"source_index"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// IndexResultRequest is the primary entry point. Lady Glass's
// index-kowloon stage hands Kowloon a URI to an already-archived S3
// result; Kowloon reads it, materialises records via the schema
// converter, embeds, and upserts to the vector backend.
//
// ImportBatchID is the only handle for "drop everything from this batch
// and re-index", which is the natural recovery flow when the embedding
// model changes — strongly recommended even though it is optional on
// the wire.
type IndexResultRequest struct {
	JobID         string     `json:"job_id"`
	TenantID      string     `json:"tenant_id"`
	ResultURI     string     `json:"result_uri"`
	ResultType    ResultType `json:"result_type"`
	SchemaVersion string     `json:"schema_version"`
	ImportBatchID string     `json:"import_batch_id,omitempty"`
}

// IndexResultResponse is the synchronous response. Lady Glass writes
// it into the StageRecord's ResultURI as JSON. EmbeddingModel is
// frozen on the response so a later model swap is auditable per row.
type IndexResultResponse struct {
	Status            string    `json:"status"`
	KowloonCollection string    `json:"kowloon_collection"`
	IndexJobID        string    `json:"index_job_id"`
	VectorCount       int       `json:"vector_count"`
	EmbeddingModel    string    `json:"embedding_model"`
	IndexedAt         time.Time `json:"indexed_at"`
}

// SearchRequest is the semantic search entry point. Filters are
// post-filters keyed by Record.Metadata names; the string-only contract
// matches Record.Metadata so callers do not need to know the backend's
// typed-field schema.
type SearchRequest struct {
	RecordType RecordType        `json:"record_type"`
	Text       string            `json:"text"`
	TopK       int               `json:"top_k"`
	Filters    map[string]string `json:"filters,omitempty"`
}

type Match struct {
	Record Record  `json:"record"`
	Score  float64 `json:"score"`
}

type SearchResponse struct {
	Matches []Match `json:"matches"`
}

// ResolveMerchantRequest collapses merchant-resolution to a single
// call: Kowloon searches the transaction index for similar merchant
// strings and returns the most likely canonical name plus the
// evidence that supported it.
type ResolveMerchantRequest struct {
	MerchantRaw string            `json:"merchant_raw"`
	Context     map[string]string `json:"context,omitempty"`
	TopK        int               `json:"top_k,omitempty"`
}

type ResolveMerchantResponse struct {
	Canonical  string  `json:"canonical"`
	Confidence float64 `json:"confidence"`
	Evidence   []Match `json:"evidence"`
}
