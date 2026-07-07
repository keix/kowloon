# Kowloon Embedding Specification

This document specifies what Kowloon embeds when it ingests a Lady Glass
archive through `POST /v1/index-result`, and what lives in vector-store
metadata instead. It is the contract implemented by the
`transactions.v1` schema converter
([`internal/schema/transactions`](../internal/schema/transactions/transactions.go)).

## Design principle

Embed **semantic identity**; filter **structured facts**.

A vector search is good at "coffee somewhere in Malaysia" and bad at
"exactly 1,680 yen on June 22nd". Dates, amounts, and currencies are
therefore excluded from the embedded text and carried only as metadata,
where exact-match filters (and Lady Glass's own aggregation against the
S3 source of truth) already handle them. Two consequences fall out of
this:

- **Better fuzzy search.** The vector is dominated by the merchant, the
  item, the category, and the place — the things a human query is
  actually about — instead of being diluted by row-unique digits.
- **A working embedding cache.** Card statements repeat the same
  merchants month after month. Without a date or an amount in the text,
  identical purchases collapse to one cache entry, and re-indexing a
  new statement embeds only the merchants it has never seen.

Kowloon still never parses or normalises source values: metadata keeps
the verbatim strings from the archive. The single deliberate exception
is described under [Country expansion](#country-expansion).

## Record types

One archived document produces:

| Record type   | Count        | ID                 | `source_index`          |
| ------------- | ------------ | ------------------ | ----------------------- |
| `transaction` | one per row  | `<job_id>:tx:<i>`  | row index `i`           |
| `summary`     | one per job  | `<job_id>:summary` | `-1` (whole document)   |

A document with zero transactions produces no records at all.

Searches **must** pass `record_type` in `SearchRequest`; an empty
`record_type` matches every type and will mix summaries into
transaction results.

## Transaction records

### Embedded text

```
<merchant part> | <description> | <category> | <country name> | paid in <foreign_currency>
```

Every segment except the merchant part is optional and omitted when its
source field is empty. Segments are joined with ` | `.

The **merchant part** combines the canonical and verbatim spellings:

| `merchant_normalized` | relation to `merchant`   | merchant part                 |
| --------------------- | ------------------------ | ----------------------------- |
| empty                 | —                        | `<merchant>`                  |
| present               | equal (case-insensitive) | `<merchant_normalized>`       |
| present               | different                | `<normalized> (<merchant>)`   |

Both spellings matter: the canonical name anchors the vector for
grouping-style queries, while the verbatim OCR string is what
`POST /v1/resolve/merchant` needs to match raw variants against.

Examples:

```
Starbucks (スターバックスコーヒージャパン) | cafe | Japan
Grab (GRAB* A-123) | transport | Malaysia | paid in MYR
Petronas
```

### Excluded from the text — by design

| Field                          | Why it is metadata-only                                        |
| ------------------------------ | -------------------------------------------------------------- |
| `date`                         | no semantic signal; `year_month` filter covers time queries    |
| `amount`, `foreign_amount`     | no semantic signal; aggregation is Lady Glass's job against S3 |
| `currency`                     | almost always JPY; `foreign_currency` carries the signal       |
| `issuer`, `card_name`          | identical on every row of a job — they would pull all of a statement's vectors in one direction while adding nothing a filter cannot do |

### Metadata

| Key                   | Source                          | Presence                    |
| --------------------- | ------------------------------- | --------------------------- |
| `tenant_id`           | request                         | always                      |
| `job_id`              | request (added by the indexer)  | always                      |
| `date`                | row, verbatim                   | always                      |
| `merchant`            | row, verbatim                   | always                      |
| `amount`              | row, verbatim                   | always                      |
| `currency`            | row; defaults to `JPY`          | always                      |
| `year_month`          | derived from `date` (`YYYY-MM`) | when the date parses        |
| `merchant_normalized` | row (enrich stage)              | when enriched               |
| `country`             | row (enrich stage), ISO code    | when enriched               |
| `category`            | row                             | when present                |
| `document_type`       | document                        | when present                |
| `issuer`              | document fields                 | when present                |
| `card_name`           | document fields                 | when present                |
| `foreign_amount`      | row, verbatim                   | with `foreign_currency`     |
| `foreign_currency`    | row, verbatim                   | with `foreign_amount`       |
| `import_batch_id`     | request                         | when supplied               |

All values are verbatim source strings (metadata is `map[string]string`
end to end). The Qdrant backend promotes `tenant_id`, `job_id`,
`record_type`, `document_type`, `year_month`, `merchant`,
`merchant_normalized`, `foreign_currency`, `country`, `category`, and
`import_batch_id` to keyword-indexed payload fields; everything else is
still filterable, just unindexed.

## Summary records

One per job, for document-level fuzzy search ("that statement with the
Malaysia trip"). Per-row detail stays on the transaction records.

### Embedded text

```
<document type, underscores → spaces> | <issuer> <card_name> | <year_month> | <N> transactions | merchants: <m1, m2, …> | countries: <c1, c2, …>
```

Example:

```
credit card statement | smbc platinum_preferred | 2026-07 | 45 transactions | merchants: Grab, Starbucks, FamilyMart | countries: Malaysia, Japan
```

- **merchants** — distinct, in row order, canonical spelling preferred,
  case-insensitive dedupe, capped at 20 entries.
- **countries** — distinct, in row order, expanded to English names.
- **year_month** — from the `statement_date` header field, falling back
  to the first row whose date parses.
- **No totals.** A total would require parsing amount strings, which
  the converter deliberately does not do; Lady Glass owns aggregation.

### Metadata

`tenant_id`, `job_id`, `transaction_count`, and — when present —
`document_type`, `issuer`, `card_name`, `year_month`,
`import_batch_id`.

## Country expansion

Metadata keeps the ISO 3166-1 alpha-2 code verbatim (`country: "MY"`),
but the embedded text uses the English name, because two-letter codes
carry little embedding signal:

| Code | Text form       |
| ---- | --------------- |
| JP   | Japan           |
| MY   | Malaysia        |
| SG   | Singapore       |
| TH   | Thailand        |
| US   | United States   |

The table mirrors the countries Lady Glass's `enrich_transactions`
stage can emit. An unknown code passes through verbatim rather than
being dropped, so a new enrich country still contributes signal until
this table catches up.

## Operational notes

- **Changing this spec changes the idempotency content hash.** The
  pipeline-level idempotency key includes a hash of the record
  contents, so redeploying with a different text rule will re-embed on
  the next `index-result` call rather than short-circuiting. To avoid
  old-rule and new-rule rows coexisting in one collection, drop and
  re-index per job: `DELETE /v1/jobs/{job_id}` followed by a fresh
  `POST /v1/index-result`.
- **The embedding cache keys on the exact text.** The merchant-centric
  format is what makes the cache effective; adding row-unique tokens
  back into the text silently degrades it to zero hits.

## Future work

- **Full-text chunks** (`RecordTypeChunk` is reserved): not possible
  today because Lady Glass's `ArchiveResult` drops the per-page OCR
  `text` when flattening. Archiving the full text is a Lady Glass
  change; the chunk converter lands here afterwards.
- **Numeric range filters** ("over 10,000 yen"): would need amounts
  parsed into numeric payload fields at the backend layer. Deferred —
  aggregation currently belongs to Lady Glass against the S3 archive.
