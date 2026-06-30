// Package schema converts raw archived result bytes into records the
// embedder and backend can consume. Each implementation handles one
// (result_type, schema_version) pair — transactions.v1 is the only one
// shipped in v0; receipts.v1 etc. land alongside their Lady Glass
// chain.
package schema

import "github.com/keix/kowloon"

type Schema interface {
	// Convert reads the archived bytes and produces records ready to embed.
	// The request is passed through so converters can populate metadata
	// (tenant_id, import_batch_id, year_month) without re-parsing the URI.
	Convert(raw []byte, req kowloon.IndexResultRequest) ([]kowloon.Record, error)
}
