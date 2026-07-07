#!/usr/bin/env bash
#
# replay.sh — re-index Lady Glass results by POSTing each line of a
# manifest to Kowloon's /v1/index-result. Use after the embed-text
# scheme changed and the collection was dropped, to rebuild every
# vector under the new scheme.
#
# Manifest: JSONL, one IndexResultRequest per line (see
# build-manifest.sh). Each object must carry job_id, tenant_id,
# result_uri, result_type, schema_version; import_batch_id is optional.
#
#   scripts/reindex/replay.sh manifest.jsonl
#
# Config (env):
#   KOWLOON_API   base URL of the kowloon-api to hit (default
#                 http://127.0.0.1:8080)
#   CONCURRENCY   parallel in-flight requests (default 4)
#   DRY_RUN=1     print what would be POSTed, send nothing
#
# ── idempotency ────────────────────────────────────────────────────
# The idempotency key includes the converter's Revision (see
# schema.Schema.Revision). As long as you bumped that revision in the
# same change that altered the embed text, a re-index re-runs correctly
# against a NORMAL instance — no special flags, no clearing the store.
# A vector_count of 0 for a non-empty document is the tell-tale that the
# revision was NOT bumped and idempotency is skipping the work; this
# script flags it as a WARN. Only if you deliberately re-run WITHOUT a
# revision bump do you need KOWLOON_IDEMPOTENCY=none on the target.
# ───────────────────────────────────────────────────────────────────
set -euo pipefail

MANIFEST="${1:?usage: replay.sh <manifest.jsonl>}"
KOWLOON_API="${KOWLOON_API:-http://127.0.0.1:8080}"
CONCURRENCY="${CONCURRENCY:-4}"
DRY_RUN="${DRY_RUN:-0}"

[ -f "$MANIFEST" ] || { echo "manifest not found: $MANIFEST" >&2; exit 1; }

endpoint="${KOWLOON_API%/}/v1/index-result"
total="$(grep -cve '^[[:space:]]*$' "$MANIFEST" || true)"
echo "replaying $total job(s) -> $endpoint (concurrency=$CONCURRENCY, dry_run=$DRY_RUN)" >&2

# post_one <json-line> — POST a single request, retrying transient
# failures. Prints a one-line status per job. Exit code is advisory:
# xargs collects failures via the trailing marker in stdout.
post_one() {
  local line="$1"
  local job_id uri
  job_id="$(printf '%s' "$line" | jq -r '.job_id')"
  uri="$(printf '%s' "$line" | jq -r '.result_uri')"

  if [ "$DRY_RUN" = "1" ]; then
    printf 'DRY  %s\n' "$job_id"
    return 0
  fi

  local attempt=1 max=4 body http
  while :; do
    body="$(mktemp)"
    http="$(curl -sS -o "$body" -w '%{http_code}' \
      -X POST "$endpoint" \
      -H 'Content-Type: application/json' \
      --data-binary "$line" || echo 000)"

    if [ "$http" = "200" ]; then
      local vc idem
      vc="$(jq -r '.vector_count // 0' <"$body")"
      # Idempotent hits return status=indexed too, but with no new work.
      # We can't see the flag in the response body, so surface a warning
      # when vector_count is 0 for a job we expected to produce records.
      idem=""
      [ "$vc" = "0" ] && idem="  (WARN: 0 vectors — idempotency skip or empty doc?)"
      printf 'OK   %s  vectors=%s%s\n' "$job_id" "$vc" "$idem"
      rm -f "$body"
      return 0
    fi

    if [ "$attempt" -ge "$max" ]; then
      printf 'FAIL %s  http=%s  %s\n' "$job_id" "$http" "$(head -c 200 <"$body")"
      rm -f "$body"
      return 1
    fi
    sleep "$attempt"
    attempt=$((attempt + 1))
    rm -f "$body"
  done
}

# Fan out with a hand-rolled semaphore: at most CONCURRENCY jobs in
# flight. xargs is avoided on purpose — it applies its own quote
# processing to each item and would mangle the JSON's double quotes.
# Each job writes OK/FAIL/DRY to its own status file; we tally at the end.
statusdir="$(mktemp -d)"
trap 'rm -rf "$statusdir"' EXIT
n=0
while IFS= read -r line; do
  [ -n "${line//[[:space:]]/}" ] || continue
  n=$((n + 1))
  (
    if out="$(post_one "$line")"; then :; fi
    printf '%s\n' "$out"
    printf '%s' "$out" > "$statusdir/$n"
  ) &
  # Throttle: once CONCURRENCY are running, wait for one to finish.
  while [ "$(jobs -rp | wc -l)" -ge "$CONCURRENCY" ]; do wait -n 2>/dev/null || true; done
done < "$MANIFEST"
wait

# grep exits non-zero when nothing matches; tolerate it under set -e.
fails="$(grep -l '^FAIL' "$statusdir"/* 2>/dev/null | wc -l | tr -d ' ')" || true
: "${fails:=0}"
echo "done: $((total - fails))/$total ok, $fails failed" >&2
[ "$fails" -eq 0 ]
