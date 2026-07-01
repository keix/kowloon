// Command kowloon-api is the HTTP front of the semantic-memory service
// Lady Glass's index-kowloon stage talks to. It reads its config from
// the env contract owned by the NixOS infra repo (KOWLOON_ADDR,
// KOWLOON_BACKEND, MILVUS_ENDPOINT, KOWLOON_EMBEDDING_MODEL,
// OPENAI_API_KEY, AWS_REGION) and wires:
//
//	source  -> S3 (default AWS credential chain)
//	schema  -> transactions.v1
//	embed   -> OpenAI text-embedding-3-small
//	backend -> memory | milvus
//
// /healthz is wired before any backend init so the deploy plumbing
// stays observable even if Milvus or OpenAI are unreachable.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	mclient "github.com/milvus-io/milvus-sdk-go/v2/client"

	"github.com/keix/kowloon/internal/backend"
	backendmemory "github.com/keix/kowloon/internal/backend/memory"
	"github.com/keix/kowloon/internal/backend/milvus"
	"github.com/keix/kowloon/internal/embed"
	embedcache "github.com/keix/kowloon/internal/embed/cache"
	cachememory "github.com/keix/kowloon/internal/embed/cache/memory"
	"github.com/keix/kowloon/internal/embed/openai"
	"github.com/keix/kowloon/internal/httpapi"
	"github.com/keix/kowloon/internal/idempotency"
	idempotencymemory "github.com/keix/kowloon/internal/idempotency/memory"
	"github.com/keix/kowloon/internal/indexer"
	"github.com/keix/kowloon/internal/schema"
	"github.com/keix/kowloon/internal/schema/transactions"
	s3source "github.com/keix/kowloon/internal/source/s3"
)

func main() {
	addr := envOr("KOWLOON_ADDR", ":8080")
	backendKind := envOr("KOWLOON_BACKEND", "memory")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}

	var embedder embed.Provider = openai.New(openai.Config{
		APIKey: apiKey,
		Model:  envOr("KOWLOON_EMBEDDING_MODEL", openai.DefaultModel),
	})

	cacheKind := envOr("KOWLOON_CACHE", "memory")
	switch cacheKind {
	case "memory":
		embedder = embedcache.Wrap(embedder, cachememory.New(cachememory.DefaultCapacity))
	case "none":
		// no wrap
	default:
		log.Fatalf("unknown KOWLOON_CACHE=%q (want memory|none)", cacheKind)
	}

	ctx := context.Background()

	be, err := buildBackend(ctx, backendKind, embedder.Dim())
	if err != nil {
		log.Fatalf("backend %s: %v", backendKind, err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}
	src := s3source.New(awss3.NewFromConfig(awsCfg))

	schemas := map[string]schema.Schema{
		transactions.SchemaVersion: transactions.New(),
	}
	ix := indexer.New(src, schemas, embedder, be)

	idemKind := envOr("KOWLOON_IDEMPOTENCY", "memory")
	switch idemKind {
	case "memory":
		var store idempotency.Store = idempotencymemory.New()
		ix.Idempotency = store
	case "none":
		// no idempotency layer
	default:
		log.Fatalf("unknown KOWLOON_IDEMPOTENCY=%q (want memory|none)", idemKind)
	}

	server := httpapi.NewServer(ix)
	log.Printf("kowloon-api listening on %s (backend=%s, model=%s, cache=%s, idempotency=%s)",
		addr, backendKind, embedder.Model(), cacheKind, idemKind)
	if err := http.ListenAndServe(addr, server.Routes()); err != nil {
		log.Fatal(err)
	}
}

func buildBackend(ctx context.Context, kind string, dim int) (backend.Index, error) {
	switch kind {
	case "memory":
		return backendmemory.New(), nil
	case "milvus":
		endpoint := envOr("MILVUS_ENDPOINT", "127.0.0.1:19530")
		c, err := mclient.NewClient(ctx, mclient.Config{Address: endpoint})
		if err != nil {
			return nil, err
		}
		mb := milvus.New(c, milvus.Config{Collection: "transactions", Dim: dim})
		if err := mb.Ensure(ctx); err != nil {
			return nil, err
		}
		return mb, nil
	default:
		return nil, &unknownBackend{kind: kind}
	}
}

type unknownBackend struct{ kind string }

func (e *unknownBackend) Error() string {
	return "unknown KOWLOON_BACKEND " + e.kind + " (want memory|milvus)"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
