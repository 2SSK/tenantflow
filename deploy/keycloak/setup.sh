#!/usr/bin/env sh
# Bootstrap a dev Keycloak for TenantFlow: realm, platform-operator role, and
# the API client. Idempotent — safe to run repeatedly.
#
# Requirements: curl + jq, a running Keycloak (make dev-up).

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

ensure_role() { # role-name
    if curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/roles/$1" >/dev/null 2>&1; then
        echo "==> role $1 exists"
    else
        echo "==> creating role $1"
        curl -fsS -X POST "$BASE/admin/realms/$REALM/roles" -H "$AUTH" -H "$CT" \
            -d "{\"name\":\"$1\"}" >/dev/null
    fi
}

ensure_role platform-operator
# Mutations (create/upgrade/migrate/backup/restore/delete/reconcile/retry and
# the failed-runs DLQ) require the admin role — see internal/router/router.go.
ensure_role platform-admin

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

# Demo/load-test user used by scripts/demo/run-demo.sh and cmd/loadtest.
# Give it both roles: mutations need platform-admin, reads work with either.
DEMO_USER="${DEMO_USER:-loadtest2}"
DEMO_PASS="${DEMO_PASS:-loadtest}"
if curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/users?username=$DEMO_USER" \
    | jq -e 'length > 0' >/dev/null 2>&1; then
    echo "==> demo user '$DEMO_USER' exists"
else
    echo "==> creating demo user '$DEMO_USER'"
    curl -fsS -X POST "$BASE/admin/realms/$REALM/users" -H "$AUTH" -H "$CT" \
        -d "{\"username\":\"$DEMO_USER\",\"enabled\":true,\"email\":\"$DEMO_USER@example.com\",\"emailVerified\":true,\"firstName\":\"Load\",\"lastName\":\"Tester\",
             \"credentials\":[{\"type\":\"password\",\"value\":\"$DEMO_PASS\",\"temporary\":false}]}" >/dev/null
fi
USER_ID=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/users?username=$DEMO_USER" | jq -r '.[0].id')
for ROLE in platform-operator platform-admin; do
    if curl -fsS -H "$AUTH" \
        "$BASE/admin/realms/$REALM/users/$USER_ID/role-mappings/realm" | jq -e \
        --arg r "$ROLE" 'any(.name == $r)' >/dev/null 2>&1; then
        echo "==> user '$DEMO_USER' has role $ROLE"
    else
        echo "==> assigning role $ROLE to '$DEMO_USER'"
        ROLE_REP=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/roles/$ROLE")
        curl -fsS -X POST -H "$AUTH" -H "$CT" \
            "$BASE/admin/realms/$REALM/users/$USER_ID/role-mappings/realm" \
            -d "[$ROLE_REP]" >/dev/null
    fi
done

echo "==> done"
