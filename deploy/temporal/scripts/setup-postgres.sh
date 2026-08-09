#!/bin/sh

# Database + schema provisioning for Temporal.
# Runs inside temporalio/admin-tools as a one-shot container.

set -eu

# Fail fast with a clear message if required env vars are missing.
# Better to crash loudly than connect with empty credentials.
: "${POSTGRES_SEEDS:?ERROR: POSTGRES_SEEDS environment varialbe is required}"
: "${POSTGRES_USER:?ERROR: POSTGRES_USER environment varialbe is required}"
: "${POSTGRES_PWD:?ERROR: POSTGRES_PWD environment varialbe is required}"

DB_PORT="${DB_PORT:-5432}"

echo 'Waiting for PostgreSQL port to be available...'
nc -z -w 10 "${POSTGRES_SEEDS}" "$DB_PORT"  # extra insurance beyond healthcheck
echo 'PostgreSQL port is available'

# --- temporal (main) database ---
temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal create

temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal setup-schema -v 0.0

temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal update-schema \
    -d /etc/temporal/schema/postgresql/v12/temporal/versioned

# --- temporal_visibility (search/visibility database) ---
temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal_visibility create

temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal_visibility setup-schema -v 0.0

temporal-sql-tool --plugin postgres12 --ep "${POSTGRES_SEEDS}" -p "${DB_PORT}" \
    -u "${POSTGRES_USER}" -pw "${POSTGRES_PWD}" --db temporal_visibility update-schema \
    -d /etc/temporal/schema/postgresql/v12/visibility/versioned

echo 'PostgreSQL schema setup complete'
