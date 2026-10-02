#!/usr/bin/env bash
# load-test.sh — one-command runner for the control-plane scale load test.
#
# Wraps cmd/loadtest: the binary resolves the API URL from the environment
# (TENANTFLOW_API_URL / TENANTFLOW_HTTP_PORT) and auto-fetches a Keycloak
# token when -token is not given, so this script needs no arguments for a
# default 100-tenant run. Any extra arguments are passed straight through
# to the binary (e.g. -prefix lt2 -timeout 5m).
#
# Optional: set OUT=<csv> to also sample control-plane resources with
# scripts/loadtest-metrics.sh in the background. The sampler requires the
# API and worker as host processes (/tmp/tf-api-bin, /tmp/tf-worker-bin)
# and stops automatically when the load test finishes.
#
# Examples:
#   bash scripts/load-test.sh                    # 100 tenants, concurrency 10
#   N=500 C=25 bash scripts/load-test.sh         # bigger run
#   OUT=/tmp/lt-500.csv N=500 bash scripts/load-test.sh -delete
set -u

cd "$(dirname "$0")/.."

# Defaults mirror cmd/loadtest's flags; override via the environment.
N="${N:-100}"
C="${C:-10}"
MODE="${MODE:-shared}"
DELETE="${DELETE:-false}"
BASE="${TENANTFLOW_API_URL:-http://localhost:${TENANTFLOW_HTTP_PORT:-9090}}"

# Preflight: /status is the one public route ("GET /status stays public",
# internal/router/router.go), so we can probe it without a token. Failing
# fast here beats a wall of connection-refused errors from the binary.
if ! curl -sf -m 2 "$BASE/status" >/dev/null 2>&1; then
  echo "error: API not reachable at $BASE/status" >&2
  echo "  start the stack with:  make dev-up && make dev-setup" >&2
  echo "  then run:              go run ./cmd/api   (and  go run ./cmd/worker)" >&2
  exit 1
fi

args=(-n "$N" -c "$C" -mode "$MODE")
[ "$DELETE" = "true" ] && args+=(-delete)

if [ -n "${OUT:-}" ]; then
  : > "$OUT"
  bash scripts/loadtest-metrics.sh "$OUT" &
  sampler_pid=$!
  trap 'kill "$sampler_pid" 2>/dev/null || true' EXIT INT TERM
  echo "sampling control-plane metrics to $OUT (sampler pid $sampler_pid)"
fi

# No `exec`: the EXIT trap above must fire to stop the sampler.
# The script's exit status is go run's, so failures propagate to CI.
go run ./cmd/loadtest "${args[@]}" "$@"