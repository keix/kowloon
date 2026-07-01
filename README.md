# Kowloon / 九龍

Kowloon is the vector memory controller for Lady Glass.  
Lady Glass reads documents and preserves their results. Kowloon turns those results into searchable memory.

## Why Kowloon

Lady Glass was from there. She read documents I could not.

Kowloon is the landscape of her memory.

## Architecture

Kowloon is called from Lady Glass as an explicit stage, not as a notification side-effect.

On the write side, the chain archives its result to a permanent S3 prefix and then hands Kowloon a URI to that archive; Kowloon reads it, embeds, and indexes.

On the read side, the user's query goes to Lady Glass's query API — Lady Glass calls Kowloon's search and resolve endpoints for candidates, then grounds the answer in the structured result on S3.

```mermaid
flowchart LR
    LG([Lady Glass chain])
    LGQ([Lady Glass query API])
    Admin([admin / debug])

    subgraph K["Kowloon"]
        direction LR

        API[httpapi]
        IDX[indexer]
        SRC[source/s3]
        SCH[schema/transactions]
        EMB[embed]
        BE[backend]

        API --> IDX
        IDX --> SRC
        IDX --> SCH
        IDX --> EMB
        IDX --> BE
    end

    S3[(S3 — permanent results)]
    OAI([OpenAI Embeddings])
    VEC[(Vector backend)]

    LG -->|archive-result stage| S3
    LG -->|index-kowloon stage| API
    LGQ -->|semantic candidates| API
    LGQ -->|read normalized result| S3
    Admin -.-> API

    SRC -. read result_uri .-> S3
    EMB -. embed text .-> OAI
    BE -. upsert / search .-> VEC

    style K fill:none,stroke:#888,stroke-width:1.5px
```

| Layer       | Owns                                                                |
| ----------- | ------------------------------------------------------------------- |
| httpapi     | routing, JSON (un)marshal, status-code mapping                      |
| indexer     | source → schema → embed → backend pipeline, with idempotency guard  |
| source      | reads archived results (S3 in v0)                                   |
| schema      | typed-result → `[]Record` conversion (`transactions.v1`, …)         |
| embed       | `EmbeddingProvider` abstraction (OpenAI `text-embedding-3-large`)   |
| cache       | embedding dedupe (memory LRU or DynamoDB behind the same interface) |
| backend     | vector store (in-memory, Qdrant, or Milvus behind the same interface) |
| idempotency | pipeline-level dedupe on (job, uri, schema, model, dim, content)    |

Kowloon never writes to the permanent bucket. Lady Glass owns the source of truth; Kowloon's index is rebuildable from it. Kowloon returns candidates; Lady Glass returns answers.

## API

Kowloon exposes five HTTP endpoints. v0 is unauthenticated and intended for private-network deployment only; an `X-Api-Key` header will be added before wider deploy. See [`types.go`](types.go) for the full request and response contract.

```text
POST   /v1/index-result          ingest an archived S3 result; returns the indexed count
POST   /v1/search                semantic candidates with metadata filters
POST   /v1/resolve/merchant      canonical merchant + evidence for a raw string
DELETE /v1/jobs/{job_id}         delete every record indexed under a job
GET    /healthz                  liveness probe
```

`/v1/index-result` is the primary entry point — Lady Glass's `index-kowloon` stage calls it with the archived URI and Kowloon takes care of fetching, schema conversion, embedding, and upsert.

`/v1/search` and `/v1/resolve/merchant` are the retrieval primitives Lady Glass calls during query composition; direct callers are admin or debug only. `DELETE /v1/jobs/{job_id}` is the re-index recovery handle, used when an embedding model swap requires dropping a batch.

## AWS Deploy

Kowloon is designed to run as a long-lived process on AWS.

In production, Kowloon embeds its own HTTP server and is managed by `systemd`. Lady Glass calls Kowloon over a private HTTP endpoint from the `index-kowloon` stage.

Kowloon reads archived stage results from S3, converts them into records, embeds them, and writes them through a configured vector backend.

```text
Lady Glass stage
  -> private HTTP
  -> Kowloon systemd service
  -> vector backend
```

The expected production shape is:

```
EC2 / NixOS
  systemd
    kowloon.service

  Kowloon
    HTTP API
    S3 result reader
    schema conversion
    embedding pipeline
    vector backend
```

The vector backend is selected by configuration. The primary AWS deployment uses Qdrant Cloud; a self-hosted Qdrant server also runs via `docker-compose.local.yml` for the integration loop. Milvus stays available as an alternative implementation behind the same interface.

Kowloon treats vector storage as an interface, not as an identity.

```
KOWLOON_ADDR=10.x.x.x:8080
KOWLOON_BACKEND=qdrant
KOWLOON_VECTOR_ENDPOINT=https://xxxx.cloud.qdrant.io:6333
KOWLOON_VECTOR_API_KEY=...
KOWLOON_EMBEDDING_MODEL=text-embedding-3-large
```

Kowloon also owns two DynamoDB tables for cross-restart persistence — an embedding cache and an idempotency store, provisioned by the CDK stack in `infra/cdk/`.

```
KOWLOON_CACHE=dynamodb
KOWLOON_EMBED_CACHE_TABLE=KowloonEmbeddingCache
KOWLOON_IDEMPOTENCY=dynamodb
KOWLOON_IDEMPOTENCY_TABLE=KowloonIdempotency
```

This keeps Lady Glass as the user-facing system, Kowloon as the private semantic memory service, and the vector backend as a rebuildable index backed by archived S3 results.

## Acknowledgments

Kowloon is the distant landscape of memory she shared with me.

## License

Kowloon is licensed under the MIT License.  
Copyright (c) 2026 Kei Sawamura a.k.a. Master *void  
