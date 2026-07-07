// Command kowloon-api is the HTTP front of the semantic-memory service
// Lady Glass's index-kowloon stage talks to. It reads its config from
// the env contract owned by the NixOS infra repo:
//
//	KOWLOON_ADDR                 listen address (default :8080)
//	KOWLOON_BACKEND              memory | milvus | qdrant
//	KOWLOON_VECTOR_ENDPOINT      milvus host:port or qdrant base URL
//	KOWLOON_VECTOR_API_KEY       qdrant api key (unused for milvus)
//	KOWLOON_EMBEDDING_MODEL      OpenAI embedding model name
//	OPENAI_API_KEY               required
//	AWS_REGION                   used for S3 and DDB
//	KOWLOON_CACHE                memory | dynamodb | none  (default memory)
//	KOWLOON_EMBED_CACHE_TABLE    DDB table when cache=dynamodb
//	KOWLOON_IDEMPOTENCY          memory | dynamodb | none  (default memory)
//	KOWLOON_IDEMPOTENCY_TABLE    DDB table when idempotency=dynamodb
//	KOWLOON_OIDC_ISSUER          OIDC issuer for bearer auth; unset = auth off
//	KOWLOON_OIDC_JWKS_URL        JWKS URL (default <issuer>/jwks.json)
//	KOWLOON_OIDC_AUDIENCE        expected "aud" claim (e.g. kowloon)
//
// /healthz is wired before any backend init so the deploy plumbing
// stays observable even if the vector backend or OpenAI are unreachable.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	mclient "github.com/milvus-io/milvus-sdk-go/v2/client"

	"github.com/keix/kowloon/internal/backend"
	backendmemory "github.com/keix/kowloon/internal/backend/memory"
	"github.com/keix/kowloon/internal/backend/milvus"
	"github.com/keix/kowloon/internal/backend/qdrant"
	"github.com/keix/kowloon/internal/embed"
	embedcache "github.com/keix/kowloon/internal/embed/cache"
	cacheddb "github.com/keix/kowloon/internal/embed/cache/dynamodb"
	cachememory "github.com/keix/kowloon/internal/embed/cache/memory"
	"github.com/keix/kowloon/internal/embed/openai"
	"github.com/keix/kowloon/internal/httpapi"
	idempotencyddb "github.com/keix/kowloon/internal/idempotency/dynamodb"
	idempotencymemory "github.com/keix/kowloon/internal/idempotency/memory"
	"github.com/keix/kowloon/internal/indexer"
	"github.com/keix/kowloon/internal/schema"
	"github.com/keix/kowloon/internal/schema/transactions"
	s3source "github.com/keix/kowloon/internal/source/s3"
)

// TTL defaults balance "long enough that reindex is free" against
// "short enough that a model swap does not leave zombie entries
// forever". Both are overrideable in code but not via env for now;
// production tuning happens on the same PR as any related change.
const (
	embedCacheTTL       = 90 * 24 * time.Hour
	idempotencyStoreTTL = 30 * 24 * time.Hour
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

	ctx := context.Background()

	// AWS config is loaded once and reused by S3, DDB cache, and
	// DDB idempotency. LoadDefaultConfig picks up creds via the
	// standard chain (env → shared config → IMDS on EC2).
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}

	cacheKind := envOr("KOWLOON_CACHE", "memory")
	switch cacheKind {
	case "memory":
		embedder = embedcache.Wrap(embedder, cachememory.New(cachememory.DefaultCapacity))
	case "dynamodb":
		table := envOr("KOWLOON_EMBED_CACHE_TABLE", "KowloonEmbeddingCache")
		embedder = embedcache.Wrap(embedder, cacheddb.New(cacheddb.Config{
			Client:    awsddb.NewFromConfig(awsCfg),
			TableName: table,
			TTL:       embedCacheTTL,
		}))
	case "none":
		// no wrap
	default:
		log.Fatalf("unknown KOWLOON_CACHE=%q (want memory|dynamodb|none)", cacheKind)
	}

	be, err := buildBackend(ctx, backendKind, embedder.Dim())
	if err != nil {
		log.Fatalf("backend %s: %v", backendKind, err)
	}

	src := s3source.New(awss3.NewFromConfig(awsCfg))

	schemas := map[string]schema.Schema{
		transactions.SchemaVersion: transactions.New(),
	}
	ix := indexer.New(src, schemas, embedder, be)

	idemKind := envOr("KOWLOON_IDEMPOTENCY", "memory")
	switch idemKind {
	case "memory":
		ix.Idempotency = idempotencymemory.New()
	case "dynamodb":
		table := envOr("KOWLOON_IDEMPOTENCY_TABLE", "KowloonIdempotency")
		ix.Idempotency = idempotencyddb.New(idempotencyddb.Config{
			Client:    awsddb.NewFromConfig(awsCfg),
			TableName: table,
			TTL:       idempotencyStoreTTL,
		})
	case "none":
		// no idempotency layer
	default:
		log.Fatalf("unknown KOWLOON_IDEMPOTENCY=%q (want memory|dynamodb|none)", idemKind)
	}

	server := httpapi.NewServer(ix)

	// Bearer auth is off unless KOWLOON_OIDC_ISSUER is set — the loopback
	// deployment stays unauthenticated until an OIDC provider (Asteroid)
	// is wired. When set, every route except /healthz requires a valid
	// ES256 token from that issuer.
	oidcIssuer := os.Getenv("KOWLOON_OIDC_ISSUER")
	authMW, err := httpapi.NewBearerAuth(httpapi.OIDCConfig{
		Issuer:   oidcIssuer,
		JWKSURL:  os.Getenv("KOWLOON_OIDC_JWKS_URL"),
		Audience: os.Getenv("KOWLOON_OIDC_AUDIENCE"),
	})
	if err != nil {
		log.Fatalf("oidc auth: %v", err)
	}
	authState := "disabled"
	if oidcIssuer != "" {
		authState = "enabled"
	}

	log.Printf("kowloon-api listening on %s (backend=%s, model=%s, cache=%s, idempotency=%s, auth=%s)",
		addr, backendKind, embedder.Model(), cacheKind, idemKind, authState)
	if err := http.ListenAndServe(addr, authMW(server.Routes())); err != nil {
		log.Fatal(err)
	}
}

func buildBackend(ctx context.Context, kind string, dim int) (backend.Index, error) {
	switch kind {
	case "memory":
		return backendmemory.New(), nil
	case "milvus":
		endpoint := envOr("KOWLOON_VECTOR_ENDPOINT", "127.0.0.1:19530")
		c, err := mclient.NewClient(ctx, mclient.Config{Address: endpoint})
		if err != nil {
			return nil, err
		}
		mb := milvus.New(c, milvus.Config{Collection: "transactions", Dim: dim})
		if err := mb.Ensure(ctx); err != nil {
			return nil, err
		}
		return mb, nil
	case "qdrant":
		endpoint := os.Getenv("KOWLOON_VECTOR_ENDPOINT")
		if endpoint == "" {
			return nil, &unknownBackend{kind: "qdrant: KOWLOON_VECTOR_ENDPOINT is required"}
		}
		qb := qdrant.New(qdrant.Config{
			Endpoint:   endpoint,
			APIKey:     os.Getenv("KOWLOON_VECTOR_API_KEY"),
			Collection: "transactions",
			Dim:        dim,
		})
		if err := qb.Ensure(ctx); err != nil {
			return nil, err
		}
		return qb, nil
	default:
		return nil, &unknownBackend{kind: kind}
	}
}

type unknownBackend struct{ kind string }

func (e *unknownBackend) Error() string {
	return "unknown KOWLOON_BACKEND " + e.kind + " (want memory|milvus|qdrant)"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
