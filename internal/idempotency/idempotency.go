// Package idempotency deduplicates full index-result invocations.
// Where internal/embed/cache prevents re-embedding the same text,
// idempotency prevents re-running the entire pipeline (S3 read +
// schema convert + embed + upsert) for the same (job_id, result_uri,
// schema_version, model, dimensions, content_hash) tuple.
//
// The Milvus PK upsert already prevents data corruption on retry —
// same record ID overwrites cleanly. What this layer adds is:
//
//   - skip embed + upsert cost on a duplicate POST
//   - return a deterministic response (same IndexJobID) across retries
//     so Lady Glass's index-kowloon stage sees a stable identifier
//   - log signal that a retry was a no-op, distinguishable from a
//     first-time full run
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/keix/kowloon"
)

// Key names the specific attempt an idempotency Store keys on. Model
// and Dimensions are both included so switching from
// text-embedding-3-large 3072d to 1536d correctly re-runs the pipeline
// — same content, different vectors. ContentHash catches the "S3
// object was overwritten in place" case; without it, the same URI
// would appear idempotent even when its bytes changed.
type Key struct {
	JobID         string
	ResultURI     string
	SchemaVersion string
	Model         string
	Dimensions    int
	ContentHash   string
}

// String returns the deterministic key form Stores use internally.
// The NUL byte separator prevents accidental collision between fields
// (e.g. a URI that happens to end with a valid schema_version value).
func (k Key) String() string {
	return k.JobID + "\x00" +
		k.ResultURI + "\x00" +
		k.SchemaVersion + "\x00" +
		k.Model + "\x00" +
		strconv.Itoa(k.Dimensions) + "\x00" +
		k.ContentHash
}

// MakeKey hashes the source content once and packs the identifying
// tuple. SHA-256 is the hash because it is the same primitive the
// embed cache uses, keeping the operator's mental model small.
func MakeKey(req kowloon.IndexResultRequest, model string, dim int, content []byte) Key {
	h := sha256.Sum256(content)
	return Key{
		JobID:         req.JobID,
		ResultURI:     req.ResultURI,
		SchemaVersion: req.SchemaVersion,
		Model:         model,
		Dimensions:    dim,
		ContentHash:   hex.EncodeToString(h[:]),
	}
}

// Store persists the mapping from Key to the IndexResultResponse that
// was returned on the first successful run. Implementations must be
// safe for concurrent use.
type Store interface {
	Lookup(ctx context.Context, key Key) (kowloon.IndexResultResponse, bool, error)
	Save(ctx context.Context, key Key, resp kowloon.IndexResultResponse) error
}
