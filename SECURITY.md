# Security Policy

## Supported Versions

TenantFlow has not reached a 1.0 release. Security fixes land on `main` and
are released as soon as they pass the CI gates. There are **no backport
branches** — if you rely on a specific commit, track `main` or pin a commit
SHA.

| Version | Supported          |
| ------- | ------------------ |
| main    | ✅ active          |
| < 1.0   | ⚠️ fix on main only |

## Reporting a Vulnerability

**Do not open a public issue for security problems.** Use GitHub's private
vulnerability reporting:

> Repository → Security → **Report a vulnerability**

If you cannot use private reporting, email a report to the maintainer (linked
from the repository profile) and mention `[SECURITY]` in the subject line.
Encrypted reports are preferred if you have the maintainer's PGP key; if you
do not, a plain-text report with no secrets beyond reproduction steps is
acceptable.

### What to include

- The affected component (e.g. `internal/auth`, `internal/handler`, Web UI)
- Reproduction steps — ideally a minimal request/payload that triggers it
- Proof of impact (what an attacker gains, under which conditions)
- Proposed fix, if you have one (a patch or a branch is welcome)

### Response timeline

| Step | Target |
| ---- | ------ |
| Acknowledge receipt | within 5 business days |
| Triage + first assessment | within 10 business days |
| Fix on `main` (severe, reproducible) | as fast as the CI gates allow |
| Disclosure | after a fix ships, or after 90 days for unfixed reports |

## Scope

In scope:

- The Go control plane (`cmd/api`, `cmd/worker`, `internal/…`)
- The Web admin UI (`web/`)
- Temporal / Keycloak / PostgreSQL integration code

Out of scope (responsibility of the operator/deployment):

- Secrets hygiene on the host that runs `.env` or the Docker stack
- Hardening of Keycloak, Temporal, or PostgreSQL themselves

## Good-practice reminders for maintainers

- Never commit `.env` — only `.env.example` with dev placeholders.
- Integration-test credentials (`admin`/`admin`, `temporal`/`temporal`) are
  ephemeral container defaults on CI runners, never production values.
- Rotate `TENANTFLOW_KEYCLOAK_SECRET` when promoting a config from dev to prod.