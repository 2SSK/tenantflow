# Contributing to TenantFlow

Thanks for wanting to contribute! TenantFlow is a small, opinionated codebase
— the contribution bar is deliberately low, but the quality gates are not.

## Ground rules

- **No backwards-compatibility tax.** The project removes obsolete paths
  instead of adding compatibility layers. Prefer the simplest implementation
  that fully satisfies the current requirement.
- **A working product comes first.** Build on the smallest version that works
  end-to-end; each new capability must keep the last product working.
- **Modularity.** Keep components separated by concern (`workflow`,
  `activities`, `cloud`, `identity`, `repository`, `metrics`, …).
- **Dependencies earn their place.** Prefer established libraries; do not
  reimplement common functionality. Check what the codebase already provides
  before adding a package.

## Development setup

```bash
# 1. Copy environment and adjust values
cp .env.example .env

# 2. Start the stack (PostgreSQL :5433, Temporal :7233/:8080, Keycloak :8081)
docker compose up -d

# 3. First-time Keycloak bootstrap (realm + client + role) — see deploy/
make dev-setup   # creates realm/tenantflow, client, platform-operator role

# 4. Run the API and worker
make run-api     # TFLOW_API on :9090
make run-worker  # consumes Temporal task queues
```

## Quality gates

All checks must pass before a merge; the CI pipelines in
`.github/workflows/` enforce them on every push/PR:

```bash
make fmt-check   # gofmt, excluding internal/auth/middleware.go (see below)
make vet         # go vet ./...
make build       # go build ./...
make test        # unit tests
make integration # integration tests (requires the docker stack)
```

### The middleware.go exception

`internal/auth/middleware.go` is **intentionally not gofmt-formatted**. Its
over-long lines are a deliberate, reviewed alerting experiment (Phase 5).
Never run `gofmt -w` on it; the format check excludes it.

### Integration tests

`make integration` runs the `integration` build tag against real services:

- PostgreSQL on `localhost:5433` (user `temporal` / password `temporal`)
- Keycloak on `localhost:8081` with the `tenantflow` realm and the
  `platform-operator` role present

The identity test skips (does not fail) when Keycloak is unreachable. CI
provisions the realm and role automatically; locally use `make dev-setup`.

## Commit conventions

History uses conventional commits — match it:

```
feat:     new capability          fix:      bug fix
test:     tests only              docs:     docs only
refactor: behavior-preserving     chore:    maintenance
ci:       pipelines/config        sec:      security hardening
deps:     dependency updates
```

Write the *why* in the body; the diff already shows the what. Aim for one
commit per self-contained change.

## Pull-request checklist

- [ ] `make fmt-check vet build test` all green
- [ ] Activity/workflow changes include retry-safety reasoning (see
      `docs/idempotency.md`) and cover the failure matrix
      (`docs/failure-matrix.md`)
- [ ] Integration tests added/updated for anything touching real I/O
- [ ] `docs/` updated when behavior or contracts change
- [ ] No `.env` or real secrets in the diff (`git diff --check` clean)

## Questions

Open an issue for designs you want feedback on before writing code — the
failure matrix and idempotency docs are the source of truth for correctness.