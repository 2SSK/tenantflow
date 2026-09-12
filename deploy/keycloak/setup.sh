#!/usr/bin/env sh
# Bootstrap a dev Keycloak for TenantFlow: realm, platform-operator role, and
# the API client. Idempotent — safe to run repeatedly.
#
# Requirements: curl + jq, a running Keycloak (make dev-up).
# Values come from the environment (defaults match .env.example and CI):
#   TENANTFLOW_KEYCLOAK_URL      http://localhost:8081
#   KEYCLOAK_ADMIN_USER          admin
#   KEYCLOAK_ADMIN_PASSWORD      admin
#   TENANTFLOW_KEYCLOAK_REALM    tenantflow
#   TENANTFLOW_KEYCLOAK_CLIENT_ID tenantflow-api
#   TENANTFLOW_KEYCLOAK_SECRET   api-secret-123 (empty => public client)
#   TENANTFLOW_KEYCLOAK_REDIRECT_URL http://localhost:3000/callback

set -eu

BASE="${TENANTFLOW_KEYCLOAK_URL:-http://localhost:8081}"
ADMIN_USER="${KEYCLOAK_ADMIN_USER:-admin}"
ADMIN_PASS="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
REALM="${TENANTFLOW_KEYCLOAK_REALM:-tenantflow}"
CLIENT_ID="${TENANTFLOW_KEYCLOAK_CLIENT_ID:-tenantflow-api}"
CLIENT_SECRET="${TENANTFLOW_KEYCLOAK_SECRET:-}"
REDIRECT="${TENANTFLOW_KEYCLOAK_REDIRECT_URL:-http://localhost:3000/callback}"

echo "==> waiting for Keycloak at $BASE"
for i in $(seq 1 60); do
  if curl -fsS "$BASE/realms/master" >/dev/null 2>&1; then break; fi
  sleep 2
  [ "$i" = 60 ] && { echo "Keycloak not reachable" >&2; exit 1; }
done

TOKEN=$(curl -fsS \
  -d client_id=admin-cli -d username="$ADMIN_USER" -d password="$ADMIN_PASS" \
  -d grant_type=password \
  "$BASE/realms/master/protocol/openid-connect/token" | jq -r .access_token)
AUTH="Authorization: Bearer $TOKEN"
CT="Content-Type: application/json"

if [ "$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$BASE/admin/realms/$REALM")" = "200" ]; then
  echo "==> realm '$REALM' exists"
else
  echo "==> creating realm '$REALM'"
  curl -fsS -X POST "$BASE/admin/realms" -H "$AUTH" -H "$CT" \
    -d "{\"realm\":\"$REALM\",\"enabled\":true}" >/dev/null
fi

if curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/roles/platform-operator" >/dev/null 2>&1; then
  echo "==> role platform-operator exists"
else
  echo "==> creating role platform-operator"
  curl -fsS -X POST "$BASE/admin/realms/$REALM/roles" -H "$AUTH" -H "$CT" \
    -d '{"name":"platform-operator"}' >/dev/null
fi

EXISTING=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/clients?clientId=$CLIENT_ID")
if [ "$(printf '%s' "$EXISTING" | jq 'length')" != "0" ]; then
  echo "==> client '$CLIENT_ID' exists"
else
  echo "==> creating client '$CLIENT_ID'"
  if [ -n "$CLIENT_SECRET" ]; then
    curl -fsS -X POST "$BASE/admin/realms/$REALM/clients" -H "$AUTH" -H "$CT" \
      -d "{\"clientId\":\"$CLIENT_ID\",\"enabled\":true,\"publicClient\":false,\"secret\":\"$CLIENT_SECRET\",\"redirectUris\":[\"$REDIRECT\"]}" >/dev/null
  else
    curl -fsS -X POST "$BASE/admin/realms/$REALM/clients" -H "$AUTH" -H "$CT" \
      -d "{\"clientId\":\"$CLIENT_ID\",\"enabled\":true,\"publicClient\":true,\"redirectUris\":[\"$REDIRECT\"]}" >/dev/null
  fi
fi

echo "==> done"