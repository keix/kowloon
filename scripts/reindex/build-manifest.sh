#!/usr/bin/env bash
#
# build-manifest.sh — scaffold a replay manifest by listing the Lady
# Glass result bucket. Emits one JSON object per line (JSONL), each an
# IndexResultRequest ready for replay.sh to POST to /v1/index-result.
#
# It parses the fields it can from the S3 key convention
#
#   s3://<bucket>/results/<result_type>/tenant=<tenant>/year=<YYYY>/month=<MM>/<name>.json
#
# and SYNTHESISES job_id from the key (path minus prefix/extension,
# sanitised). That is correct for a freshly-dropped collection — every
# record ID is stable across replay re-runs so upserts overwrite — but
# it does NOT match Lady Glass's original job_ids. If you need the
# original job_ids (e.g. so a later Lady-Glass DeleteByJob still lines
# up), replace the synthesised job_id below with a real one, or hand
# replay.sh a manifest you built from Lady Glass's own job records.
#
# Usage:
#   S3_BUCKET=lady-glass-keix S3_PREFIX=results/transactions/ \
#     IMPORT_BATCH_ID=2026-07-07-embed-text-v2 \
#     scripts/reindex/build-manifest.sh > manifest.jsonl
#
# Then eyeball manifest.jsonl before replaying.
set -euo pipefail

: "${S3_BUCKET:?set S3_BUCKET (e.g. lady-glass-keix)}"
S3_PREFIX="${S3_PREFIX:-results/}"
IMPORT_BATCH_ID="${IMPORT_BATCH_ID:-}"

# result_type -> schema_version. Extend as new result shapes land.
schema_version_for() {
  case "$1" in
    transactions) echo "transactions.v1" ;;
    *) echo "" ;;
  esac
}

# Only .json result objects are index inputs; skip everything else
# (manifests, _SUCCESS markers, directory placeholders).
aws s3api list-objects-v2 \
  --bucket "$S3_BUCKET" \
  --prefix "$S3_PREFIX" \
  --query 'Contents[].Key' \
  --output text \
| tr '\t' '\n' \
| grep -E '\.json$' \
| while read -r key; do
    [ -n "$key" ] || continue

    # results/<result_type>/tenant=<tenant>/...
    result_type="$(printf '%s' "$key" | sed -nE 's#^results/([^/]+)/.*#\1#p')"
    tenant="$(printf '%s' "$key"      | sed -nE 's#.*/tenant=([^/]+)/.*#\1#p')"
    schema_version="$(schema_version_for "$result_type")"

    if [ -z "$result_type" ] || [ -z "$tenant" ] || [ -z "$schema_version" ]; then
      echo "SKIP (unrecognised key layout): $key" >&2
      continue
    fi

    # Synthesised job_id: key without the results/ prefix and .json
    # suffix, path separators and '=' folded to '-'. Deterministic, so
    # re-running replay overwrites rather than duplicating.
    job_id="$(printf '%s' "$key" \
      | sed -E 's#^results/##; s#\.json$##; s#[/=]#-#g')"

    jq -nc \
      --arg job_id "$job_id" \
      --arg tenant "$tenant" \
      --arg uri "s3://${S3_BUCKET}/${key}" \
      --arg rt "$result_type" \
      --arg sv "$schema_version" \
      --arg batch "$IMPORT_BATCH_ID" \
      '{
         job_id: $job_id,
         tenant_id: $tenant,
         result_uri: $uri,
         result_type: $rt,
         schema_version: $sv
       }
       + (if $batch == "" then {} else {import_batch_id: $batch} end)'
  done
