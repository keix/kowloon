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

	// Revision identifies the converter's output logic and must be bumped
	// whenever a change to Convert alters the records or embed text
	// produced from unchanged input. It feeds the idempotency key, so a
	// bump is what forces a re-index to actually re-run rather than being
	// skipped as a duplicate — the mechanism behind "drop the collection
	// and rebuild". Distinct from SchemaVersion, which names the *input*
	// shape the converter accepts; Revision names the *output* it emits.
	Revision() string
}
