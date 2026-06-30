// Package source is the read side of the indexer's pipeline. Implementations
// fetch an archived result by URI; the bytes are then handed to a schema
// converter. v0 only ships an S3 implementation (internal/source/s3) but
// the interface stays narrow enough that fixtures, local files, or HTTP
// readers slot in without touching the indexer.
package source

import "context"

type Source interface {
	Read(ctx context.Context, uri string) ([]byte, error)
}
