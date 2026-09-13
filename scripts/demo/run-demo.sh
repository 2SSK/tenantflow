#!/usr/bin/env bash
#
# run-demo.sh — one-take demo of TenantFlow's lifecycle, failure, DLQ replay,
# and reconcile convergence. Every beat is deterministic and prints what the
# video should show on screen.
#
# Beats (see docs/demo.md for the shot list):
#   1. Happy path:  create -> active   (audit timeline)
#   2. Failure:     a delete whose terminal CAS is sabotaged -> DLQ
#   3. Replay:      /retry converges the torn-down world back to active
#   4. Reconcile:   external drift (PUBLIC CONNECT) -> detected -> repaired
#
# Requirements: curl, jq, docker (stack from make dev-up running).
# Usage:        sh scripts/demo/run-demo.sh [--cleanup]
# Idempotent:   every run uses a unique tenant suffix, so re-runs never collide.

set -euo pipefail

BASE="${BASE:-http://localhost:9090}"
KCBASE="${KCBASE:-http://localhost:8081}"
REALM="${REALM:-tenantflow}"
CLIENT_SECRET="${TENANTFLOW_KEYCLOAK_SECRET:-api-secret-123}"
PG_USER="${POSTGRES_USER:-temporal}"
PG_DB="${TENANTFLOW_DATABASE_URL:-}"
CLEANUP="${1:-}"

say()  { printf '\n\033[1;36m=== %s ===\033[0m\n' "$*"; }
ok()   { printf '\033[32m  ok: %s\033[0m\n' "$*"; }

# shellcheck disable=SC2016
TOKEN="$(curl -fsS -d grant_type=password -d client_id=tenantflow-api \
  -d client_secret="$CLIENT_SECRET" -d username=loadtest2 -d password=loadtest \
  "$KCBASE/realms/$REALM/protocol/openid-connect/token" | jq -r .access_token)"

AUTH=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json")

# The tenant row is written by the workflow's first activity, so a GET before
# that lands returns 404 with no status — treat it as "pending", like the
# load-test harness does.
tenant_status() { curl -sS "$BASE/api/v1/tenants/$1" "${AUTH[@]}" | jq -r '.status // "pending"'; }
wait_status() { # tenant target
  for _ in $(seq 1 120); do
    s="$(tenant_status "$1")"
    [ "$s" = "$2" ] && return 0
    sleep 0.2
  done
  echo "TIMEOUT waiting for $1 -> $2 (last: $s)" >&2; exit 1
}

SUFFIX="$(date +%H%M%S)"
GOOD="demo-good-$SUFFIX"
FAIL="demo-fail-$SUFFIX"
RCON="demo-recon-$SUFFIX"

# ============================================================================
say "Beat 1 — happy path: provision $GOOD (dedicated)"
# ============================================================================
curl -fsS -X POST "$BASE/api/v1/tenants" "${AUTH[@]}" \
  -d "{\"tenantID\":\"$GOOD\",\"isolationMode\":\"dedicated\"}" | jq -c .
wait_status "$GOOD" active
ok "$GOOD is active"
curl -fsS "$BASE/api/v1/tenants/$GOOD/events" "${AUTH[@]}" | jq -c \
  '.events[] | {t: (.CreatedAt|split("T")[1]|split(".")[0]), type: .EventType}' | head -5

# ============================================================================
say "Beat 2 — failure: delete $FAIL, sabotage the terminal CAS mid-saga"
# ============================================================================
curl -fsS -X POST "$BASE/api/v1/tenants" "${AUTH[@]}" \
  -d "{\"tenantID\":\"$FAIL\",\"isolationMode\":\"dedicated\"}" >/dev/null
wait_status "$FAIL" active
ok "$FAIL provisioned"

curl -fsS -X DELETE "$BASE/api/v1/tenants/$FAIL" "${AUTH[@]}" | jq -c .
# Poll for the 'deleting' state (it appears the moment the saga starts) and
# flip the CAS source underneath it. The dedicated pre-delete backup keeps the
# saga busy for seconds, so the flip is deterministic, not a race.
for _ in $(seq 1 100); do
  s="$(tenant_status "$FAIL")"
  if [ "$s" = "deleting" ]; then
    docker exec tenantflow-postgres psql -U "$PG_USER" -d tenantflow -qc \
      "UPDATE tenants SET status='failed' WHERE tenant_id='$FAIL'"
    ok "CAS sabotaged while $FAIL was $s"
    break
  fi
  [ "$s" = "deleted" ] && { echo "delete finished before sabotage (unlikely)"; exit 1; }
  sleep 0.1
done
# The saga's final CAS (deleting -> deleted) now conflicts -> non-retryable ->
# the run lands in the DLQ instead of looping forever.
sleep 8
curl -fsS "$BASE/api/v1/tenants/$FAIL" "${AUTH[@]}" | jq -c '{status}'
echo "  DLQ:"
curl -fsS "$BASE/api/v1/failed-runs" "${AUTH[@]}" | jq -c \
  --arg t "$FAIL" '.runs[] | select(.tenantID==$t) | {tenantID, workflowID, errorMessage}'
echo "  world after failed teardown (database|role):"
docker exec tenantflow-postgres psql -U "$PG_USER" -d postgres -tAc \
  "SELECT (SELECT count(*) FROM pg_database WHERE datname='tenant_$FAIL'),
          (SELECT count(*) FROM pg_roles WHERE rolname='tenant_$FAIL')"

# ============================================================================
say "Beat 3 — replay: /retry converges the torn-down world back to active"
# ============================================================================
curl -fsS -X POST "$BASE/api/v1/tenants/$FAIL/retry" "${AUTH[@]}" | jq -c .
wait_status "$FAIL" active
ok "$FAIL is active again — DB and role recreated"
docker exec tenantflow-postgres psql -U "$PG_USER" -d postgres -tAc \
  "SELECT (SELECT count(*) FROM pg_database WHERE datname='tenant_$FAIL'),
          (SELECT count(*) FROM pg_roles WHERE rolname='tenant_$FAIL')"

# ============================================================================
say "Beat 4 — reconcile: drift (PUBLIC CONNECT) detected and repaired"
# ============================================================================
curl -fsS -X POST "$BASE/api/v1/tenants" "${AUTH[@]}" \
  -d "{\"tenantID\":\"$RCON\",\"isolationMode\":\"dedicated\"}" >/dev/null
wait_status "$RCON" active
ok "$RCON provisioned with CONNECT revoked from PUBLIC"
docker exec tenantflow-postgres psql -U "$PG_USER" -d tenantflow -qc \
  "GRANT CONNECT ON DATABASE \"tenant_$RCON\" TO PUBLIC"
ok "drift introduced: PUBLIC CONNECT granted"
curl -fsS -X POST "$BASE/api/v1/tenants/$RCON/reconcile" "${AUTH[@]}" | jq -c .
sleep 6
curl -fsS "$BASE/api/v1/tenants/$RCON/events" "${AUTH[@]}" | jq -c \
  --arg t "$RCON" '.events[] | select(.EventType=="TENANT_DRIFT_DETECTED" or .EventType=="TENANT_RECONCILE_CONVERGED") | {type: .EventType, payload: .Payload}'
if docker exec tenantflow-postgres psql -U "$PG_USER" -d postgres -tAc \
  "SELECT has_database_privilege('public','tenant_$RCON','CONNECT')" | grep -q f; then
  ok "CONNECT revoked again — converged"
else
  echo "NOT converged!" >&2; exit 1
fi

say "Demo beats complete"
if [ "$CLEANUP" = "--cleanup" ]; then
  for t in "$GOOD" "$FAIL" "$RCON"; do
    curl -fsS -X DELETE "$BASE/api/v1/tenants/$t" "${AUTH[@]}" >/dev/null || true
  done
  echo "  cleaned up $GOOD $FAIL $RCON (soft-deleted)"
fi