# Task: m1-episode-delete — soft-delete an episode (API + Library action)

**Milestone:** M1 (human-authorized library cleanup; missing product primitive) · **Type:** full-stack small · **Slug:** `m1-episode-delete`

## Scope

1. **API:** `DELETE /api/episodes/{id}` — auth required, org-scoped (foreign/unknown →
   404). Soft delete: set `episodes.deleted_at = now()` (column exists per the
   soft-delete convention). Idempotent (already-deleted → 204 again). 204 on success.
   Verify EVERY existing episode read path excludes `deleted_at IS NOT NULL` (list,
   get, transcript, proxy, retry, pipeline claims/sweeps — grep the queries; fix any
   that don't filter; the sweeps must not resurrect or bill deleted episodes).
   Storage objects are NOT removed (soft delete only; GC is a later concern — document).
2. **Web:** a remove action on the Library row (kebab or inline button per
   design/DESIGN.md conventions — check the prototype for a row-action pattern; keep it
   minimal and keyboard-reachable) with a confirm step (danger styling from tokens);
   optimistic removal from the list on 204. Component test + e2e (delete a row in the
   demo flow, gone after reload). Deleted rows never render.
3. **Tests:** DB-backed soft-delete + idempotency + org-scoping; read-path exclusion
   (deleted episode invisible to list/transcript/proxy AND unclaimable by stages/sweeps);
   web component + e2e. Baselines: if the row-action changes committed shots, STOP and
   report (Architect regenerates).

## Acceptance

- make check + make e2e green. Reviewer verifies org-scoping, every read/claim path
  excludes deleted, idempotency, danger-styled confirm per design, no baselines touched.
- Architect (post-deploy): cleans up the verification rows in prod (keeping the human's
  original upload + the designated 3-speaker sample).

## Evidence

Summary; diffs; test transcript; baseline-impact statement; open questions.
