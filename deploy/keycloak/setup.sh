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
# Must match the API's default (internal/config) and scripts/demo/run-demo.sh.
# A public client would reject the secret this repo sends everywhere.
CLIENT_SECRET="${TENANTFLOW_KEYCLOAK_SECRET:-api-secret-123}"
REDIRECT="${TENANTFLOW_KEYCLOAK_REDIRECT_URL:-http://localhost:3000/api/auth/callback/keycloak}"

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
    UUID=$(printf '%s' "$EXISTING" | jq -r '.[0].id')
    REP=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/clients/$UUID")
    PUBLIC=$(printf '%s' "$REP" | jq -r .publicClient)
    DIRECT=$(printf '%s' "$REP" | jq -r .directAccessGrantsEnabled)
    # GET /client-secret returns 404 when no secret is set (e.g. public client).
    CUR_SECRET=$({ curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/clients/$UUID/client-secret" 2>/dev/null || printf '{"value":""}'; } | jq -r .value)
    echo "==> client '$CLIENT_ID' exists (publicClient=$PUBLIC, directAccessGrants=$DIRECT)"
    if [ "$PUBLIC" = "true" ] || [ "$DIRECT" != "true" ] || [ "$CUR_SECRET" != "$CLIENT_SECRET" ]; then
        echo "==> updating client '$CLIENT_ID' to confidential, direct-access, with the expected secret"
        # Keycloak 26: PUT with "secret" sets a fixed secret; the dedicated
        # client-secret endpoint ignores "value" and regenerates instead.
        curl -fsS -X PUT "$BASE/admin/realms/$REALM/clients/$UUID" -H "$AUTH" -H "$CT" \
            -d "{\"clientId\":\"$CLIENT_ID\",\"enabled\":true,\"publicClient\":false,\"directAccessGrantsEnabled\":true,\"secret\":\"$CLIENT_SECRET\",\"redirectUris\":[\"$REDIRECT\"]}" >/dev/null
    fi
else
    echo "==> creating client '$CLIENT_ID'"
    # directAccessGrantsEnabled is required by scripts/demo/run-demo.sh
    # (password grant) and cmd/loadtest.
    curl -fsS -X POST "$BASE/admin/realms/$REALM/clients" -H "$AUTH" -H "$CT" \
        -d "{\"clientId\":\"$CLIENT_ID\",\"enabled\":true,\"publicClient\":false,\"directAccessGrantsEnabled\":true,\"secret\":\"$CLIENT_SECRET\",\"redirectUris\":[\"$REDIRECT\"]}" >/dev/null
fi

# Default users. Both get both roles: mutations need platform-admin, reads
# work with either. Wrapped in a function so adding users is one line.
ensure_user() { # username password first-name last-name
    USERNAME=$1
    PASSWORD=$2
    FIRST=$3
    LAST=$4
    if curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/users?username=$USERNAME" \
        | jq -e 'length > 0' >/dev/null 2>&1; then
        echo "==> user '$USERNAME' exists"
    else
        echo "==> creating user '$USERNAME'"
        curl -fsS -X POST "$BASE/admin/realms/$REALM/users" -H "$AUTH" -H "$CT" \
            -d "{\"username\":\"$USERNAME\",\"enabled\":true,\"email\":\"$USERNAME@example.com\",\"emailVerified\":true,\"firstName\":\"$FIRST\",\"lastName\":\"$LAST\",\"credentials\":[{\"type\":\"password\",\"value\":\"$PASSWORD\",\"temporary\":false}]}" >/dev/null
    fi
    USER_ID=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/users?username=$USERNAME" | jq -r '.[0].id')
    for ROLE in platform-operator platform-admin; do
        if curl -fsS -H "$AUTH" \
            "$BASE/admin/realms/$REALM/users/$USER_ID/role-mappings/realm" | jq -e \
            --arg r "$ROLE" 'any(.name == $r)' >/dev/null 2>&1; then
            echo "==> user '$USERNAME' has role $ROLE"
        else
            echo "==> assigning role $ROLE to '$USERNAME'"
            ROLE_REP=$(curl -fsS -H "$AUTH" "$BASE/admin/realms/$REALM/roles/$ROLE")
            curl -fsS -X POST -H "$AUTH" -H "$CT" \
                "$BASE/admin/realms/$REALM/users/$USER_ID/role-mappings/realm" \
                -d "[$ROLE_REP]" >/dev/null
        fi
    done
}

# Demo/load-test user used by scripts/demo/run-demo.sh and cmd/loadtest.
ensure_user "${DEMO_USER:-loadtest2}" "${DEMO_PASS:-loadtest}" "Load" "Tester"

# Default admin for the web UI. This is the first account for the custom
# login page. Override with APP_ADMIN_USER / APP_ADMIN_PASS if needed.
ensure_user "${APP_ADMIN_USER:-tenantflow-admin}" "${APP_ADMIN_PASS:-tenantflow}" "TenantFlow" "Admin"

echo "==> done"
