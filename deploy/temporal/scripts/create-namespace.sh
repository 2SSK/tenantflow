#!/bin/sh

# Registers the default namespace against the RUNNING Temporal server.
# Namespace creation is an API call to the server - that's why this runs as a
# separate one-shot container AFTER temporal is healthy, never in the schema
# setup container (which finishes before the server exists).

set -eu

NAMESPACES="${NAMESPACES:-default}"
TEMPORAL_ADDRESS="${TEMPORAL_ADDRESS:-temporal:7233}"
RETENTION="${DEFAULT_NAMESPACE_RETENTION:-30d}"
MAX_ATTEMPTS="${TEMPORAL_HEALTH_CHECK_MAX_ATTEMPTS:-30}"
SLEEP_SECONDS="${TEMPORAL_HEALTH_CHECK_SLEEP_SECONDS:-5}"

echo "Waiting for Temporal server at ${TEMPORAL_ADDRESS}..."
SERVER_HOST=$(echo "$TEMPORAL_ADDRESS" | cut -d: -f1)
SERVER_PORT=$(echo "$TEMPORAL_ADDRESS" | cut -d: -f2)

# Phase 1: wait for the TCP port to be reachable
attempt=1
while ! nc -z -w 10 "$SERVER_HOST" "$SERVER_PORT"; do
    if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
        echo "Temporal server port did not become available after $MAX_ATTEMPTS attempts"
    fi
    echo " port not ready yet (attempt $attempt/$MAX_ATTEMPTS)"
    attempt=$((attempt + 1))
    sleep "$SLEEP_SECONDS"
done
echo "TCP port reachable."

# Phase 2: wait for the server to report healthy (port open != fully started)
attempt=1
while ! temporal operator cluster health --address "$TEMPORAL_ADDRESS" >/dev/null 2>&1;do
    if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
        echo "ERROR: Temporal server did not become healthy after $MAX_ATTEMPTS attemplts" >&2
        exit 1
    fi
    echo "  not healthy yet (attempt $attempt/$MAX_ATTEMPTS)"
    attempt=$((attempt + 1))
    sleep "$SLEEP_SECONDS"
done
echo "Server healthy."

# Phase 3: create the namespace, but only if its doesn't already exist.
# Idempotent - safe to re-run on every `docker compose up`.
ensure_namespace(){
    ns="$1"
    if temporal operator namespace describe -n "$ns" --address "$TEMPORAL_ADDRESS" >/dev/null 2>&1; then
        echo "Namespace '${ns}' already exists - nothing to do."
        return
    fi
    temporal operator namespace create -n "$ns" --retention "$RETENTION" --address "$TEMPORAL_ADDRESS"
    echo "Namespace '${ns}' created."
}

echo "Server healthy. Ensuring namespaces exist: ${NAMESPACES}"
for NS in $NAMESPACES; do
    ensure_namespace "$NS"
done
