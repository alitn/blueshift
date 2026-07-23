# Task: m0-abandoned-uploads — TTL sweep + honest AWAITING UPLOAD state

**Milestone:** M0 polish (human-found) · **Type:** backend + web · **Slug:** `m0-abandoned-uploads`

## Problem

A create can succeed server-side and then the CLIENT abandons the upload (CORS failure,
closed tab, lost network). Rows sit at `status='uploaded' AND master_object_key IS NULL`
forever, and the Library shows them as QUEUED — a lie. No DB transaction can span the
browser's future PUT to GCS; the correct pattern is provisional state + expiry.

## Scope

1. **Sweep (app-side, Occam over pg_cron):** goroutine ticker in cmd/app (hourly; first
   run ~1 min after boot) deleting rows matching the exact orphan gate
   (`status='uploaded' AND master_object_key IS NULL AND created_at < now()-interval '6 hours'`)
   across all orgs (sweep is system-level); sqlc query `SweepAbandonedEpisodes :execrows`;
   log count at INFO when >0. Ticker respects ctx shutdown. Configurable interval/TTL via
   env with defaults (SWEEP_INTERVAL=1h, UPLOAD_TTL=6h); disabled when DATABASE_URL unset.
2. **UI honesty:** Library rows where the API can tell upload never completed render state
   `AWAITING UPLOAD` (muted chip, step 1 pending) instead of QUEUED. API: expose a boolean
   (`has_master`) or derive from a new nullable DTO field — pick the neutral-DTO-consistent
   option. QUEUED remains for has_master && status=uploaded (waiting for worker).
3. **Tests:** sweep query gate (DB-backed: fresh-but-young survives, old orphan deleted,
   old-but-keyed survives); ticker unit (fake clock or short interval); UI state mapping.

## Acceptance

- make check green; DB-backed tests cover the three gate cases.
- Local verification: create an episode via API without uploading, observe AWAITING
  UPLOAD in the dev UI; with UPLOAD_TTL=1s + SWEEP_INTERVAL=2s in a transient env, row
  disappears.

## Evidence

Summary; diffs; verification transcript; open questions.
