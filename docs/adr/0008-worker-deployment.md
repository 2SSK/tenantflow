# ADR-0008: Temporal worker must be a versioned, stateless process

- Status: accepted
- Date: 2026
- Deciders: project owner (Phase 13.2, external-review hardening)

## Context

TenantFlow's control plane is a Temporal application: the worker process
(written in Go) registers workflows and activities, the Temporal server
holds history, and the API starts workflows. Proven at scale in the load run
(shared 100-batch P99 1.46s, delete storm 84.9s, zero leftovers), the system
fails the moment the worker and the registered code drift apart — for
example, an operator restarts the worker from a build that predates a
workflow change, and Temporal replays history with code that can no longer
produce the same decisions.

Anything that breaks the worker's determinism or makes its state implicit is
a deployment hazard:

- Old binaries being restarted (stale registration / replay mismatch).
- Any in-worker mutable state (caches, flags, env-dependent branching) that
  would make history replay non-deterministic.
- Multiple worker versions writing the same task queue with conflicting
  registrations.

## Decision

The worker is **stateless, versioned, and aligned with the deployment**:

1. **Statelessness is a deterministic-replay requirement.** The worker holds
   no in-process state that influences workflow decisions; every decision is
   derived from workflow history + arguments + activity results. This is what
   makes Temporal replay (and therefore failover between workers) correct.
2. **Version the binary as a unit, not just the code.** The worker binary is
   rebuilt from the same commit as the API and shipped as a pair
   (`/tmp/tf-api-bin` + `/tmp/tf-worker-bin` in dev; the same image in
   production). Deploying one without the other is a dependency violation.
3. **Registration is driven by code, not configuration.** The worker
   registers workflows/activities in `internal/worker/worker.go`; adding an
   activity is a code change (see P0 fix where `RestoreTenantFromBackup` and
   `MarkReconcileUnrecoverable` became registerable activities), never a
   runtime knob. A restarted worker therefore cannot silently run a stale
   activity set.
4. **Fail fast on mismatch.** A worker that cannot register, connect to the
   task queue, or whose binary predates a required activity aborts at startup
   (the load run and demo rely on this: restart always uses the freshly built
   binary, and the demo/load scripts kill + rebuild + restart, never hot-patch).
5. **One task queue, one worker family.** All workers serve
   `tenantflow-provision`; there is no split-brain from parallel deployments
   with different code. Scaling is horizontal (idempotent registrations).

### Why not `WorkerVersioning`/the Temporal `Versioning API`?

Temporal's versioning is designed for *deploy-time compatibility* (old
workers finishing in-flight runs while new workers take over). TenantFlow's
release model is all-or-nothing (API + worker shipped together from the same
commit), every workflow is short-lived (seconds, not weeks), and none of the
workflows use the workflow-versioning APIs inside their code. Investing in
Temporal-side versioning now would be speculative indirection — the review
explicitly rejected over-building this. If runs ever outlive a deployment
window, this ADR's decision is revisited.

## Consequences

Positive:

- Replays are deterministic by construction — the failure matrix's
  "workflow died mid-repair, restart converges it" guarantees hold because
  the restarted process has identical behavior.
- Operational playbook is tiny: *build from HEAD, restart both binaries*.
- The two 13.1 live validations (restore-from-backup, no-backup escalation)
  ran across worker restarts and reproduced exactly — evidence the property
  holds in practice.

Negative:

- A stale worker (from an old build) is unsafe to run and the team must
  resist the temptation to "just restart the old binary" during incidents.
- No mixed-version rolling upgrades; deploys are atomic restarts (accepted
  for a control plane that tolerates sub-second downtime on restart).

## References

- `internal/worker/worker.go` — registration list (the single source of truth)
- `scripts/demo/run-demo.sh`, `scripts/loadtest` — kill + rebuild + restart
  pattern
- `docs/failure-matrix.md` — replay guarantees that depend on determinism
- Phase 13.1 live validation — restore + escalation across worker restarts

---

## Compatibility requirements (from 2026-09 external review)

External review of the worker/deploymint model (Phase 15 review) flagged that
the decision above is right for TenantFlow's *operation-day* posture but is
silent on *change-day* compatibility. The following are requirements — not a
revision of the decision — that any evolution of this ADR must keep true:

1. **Activities stay registered across deploys.** A redeploy may add or
   remove activities, but it must never leave a workflow's previously
   recorded activity references unregistered *while in-flight runs exist*.
   Registration is code-driven (`internal/worker/worker.go`); removing an
   activity a running workflow still references is a replay hazard, so
   removals count as behavior changes (gated per 3) and must wait for the run
   to close where possible.

2. **Workflow histories stay replayable.** Every deployed binary must be able
   to replay *any* history the Temporal server holds for the tenantflow task
   queue from the moment it was started — otherwise the "resume from DLQ"
   promise (ADR-0004 saga, failure-matrix §3) silently breaks. All-or-nothing
   versioned deployment is the mechanism that keeps this true.

3. **Behavior changes are gated with `workflow.GetVersion`, never silent.**
   Changing what a workflow decides (new step, reordered step, new activity
   argument shape, changed guard semantics) without a version marker is a
   determinism violation on replay. Every behavior change must be introduced
   with the Temporal SDK versioning API so old and new decisions coexist
   deterministically during the deployment window. (The current codebase has
   no versioned workflows yet — this requirement gates *future* changes, it
   does not mandate retrofitting.)

4. **Deploys are atomic restarts, not rolling mixed versions.** The
   all-or-nothing model (API + worker at the same commit, one task queue, one
   worker family) means a deployment must not run two binaries with different
   code against the same queue at the same time. The accepted negative
   consequence (sub-second downtime on restart) is the price of replay
   determinism; a mixed-version rolling upgrade would reintroduce the exact
   drift this ADR was written to rule out.