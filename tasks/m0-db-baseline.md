# Task: m0-db-baseline — migration 0001, sqlc, ids codec

**Milestone:** M0 (docs/SPEC-M0.md §4) · **Type:** backend/data · **Slug:** `m0-db-baseline`

## Goal

The database baseline exists and is additive-only from here on: migration 0001 with the core
tables and conventions, seed data, sqlc wired, a minimal store package, and the `/internal/ids`
public-ID codec with exhaustive tests.

## Allowed dependencies (declared stack, no ADR needed)

`github.com/jackc/pgx/v5` (+ pgxpool), `github.com/golang-migrate/migrate/v4`, and `sqlc` as a
**codegen tool** (generated code checked in; the tool itself is a dev prerequisite documented in
the README/Makefile, not a runtime dep). Nothing else new.

## Scope

1. **Migration `0001_baseline`** (golang-migrate, `migrations/`; up + down, but down is a
   no-op comment — we never destructively roll back; additive-only starts now):
   - `CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_trgm;`
   - Conventions on every table: PK `id bigint GENERATED ALWAYS AS IDENTITY`; `created_at`/
     `updated_at timestamptz NOT NULL DEFAULT now()` (UTC only); exposed entities get
     `public_id uuid NOT NULL DEFAULT uuidv7() UNIQUE`; user-facing entities get
     `deleted_at timestamptz` (soft delete); status columns are `text` + `CHECK`, never enums.
   - Tables: `orgs`; `shows(org_id FK)`; `users(email UNIQUE, display_name)`;
     `memberships(org_id, user_id, role text CHECK (role IN ('editor','approver')), UNIQUE(org_id,user_id))`;
     `episodes(org_id FK, show_id FK, title, source_filename, language text NOT NULL DEFAULT 'fa'
     /* BCP-47 */, status text NOT NULL DEFAULT 'uploaded' CHECK (status IN
     ('uploaded','processing','ready','failed')), duration_ms bigint, master_object_key text,
     proxy_object_key text, error_id text, public_id, deleted_at)`;
     `llm_calls(org_id, episode_id nullable FK, model text, prompt_version text, input_hash text,
     raw_response jsonb, cost_cents integer, latency_ms integer)`;
     `correction_log(org_id, episode_id, segment_idx integer, before jsonb, after jsonb,
     corrected_by bigint FK users)`;
     `config(org_id nullable FK — NULL = global, key text, value jsonb, UNIQUE(org_id, key))`.
   - Indexes: FKs; `episodes(org_id, status)`; `config` unique above.
2. **Migration `0002_seed`** (idempotent, `ON CONFLICT DO NOTHING`): one org ("Blueshift
   Pilot"), one show ("Special Interviews"); **no user rows** (dev users live in
   `fixtures/dev-seed.sql`, prod users are manual per `docs/RUNBOOK.md` — no personal data
   in the repo, ever); config rows: `allow_self_approval` →
   `true` (global), `platform_presets` → jsonb array with the two presets from design
   (`reels`: 1080×1920 H.264 high, −14 LUFS, captions burned; `telegram`: 720×1280 H.264
   CRF 23, captions burned). **No provider names in any seed value** (vendor gate greps
   migrations).
3. **sqlc.** `sqlc.yaml` (pgx/v5, `internal/store/db` output); queries for what M0 needs only:
   orgs (get by id), episodes (insert, get by public_id + org, list by org, update status),
   memberships (get role by user+org), config (get by key with org fallback to global).
   Generated code committed; `make check` must not require a live DB.
4. **`internal/store`.** Thin bootstrap: pgxpool from `DATABASE_URL` config (extend
   `internal/config`), `Ping(ctx)`, and wiring a `db` readiness check into `/readyz` **only
   when `DATABASE_URL` is set** (app must still boot without a DB for now).
5. **`/internal/ids`.** Codec: `Encode(prefix, uuid) string` / `Decode(prefix, s) (uuid, error)`
   rendering prefixed lowercase base32 (Crockford, no padding) of the 16 UUID bytes — e.g.
   `ep_0h2x…`. Typed prefixes: `ep_`, `sh_`, `mo_`, `clip_`, `sp_` (exported constants + a
   registry; unknown prefix = error). Exhaustive tests: round-trip all prefixes over random
   and edge UUIDs (zero, max), wrong-prefix rejection, bad length/characters, case handling,
   and a fuzz test for Decode. Incremental DB ids never appear in this package.
6. **Makefile:** `make migrate-up` target (uses `DATABASE_URL`), used later by demo/CI; no
   change to `make check` semantics.
7. **Tests:** ids exhaustive (pure). Store/migration tests that need Postgres must skip
   cleanly with a logged reason when `TEST_DATABASE_URL` is unset (they run under make demo/CI
   later); if it IS set, run migrations up on a scratch schema and smoke the sqlc queries.

## Out of scope

Auth (m0-auth), upload (m0-upload), worker (m0-worker-ingest), segments/moments/clips tables
(M1 — additive later), admin UI, any HTTP routes beyond the readyz check wiring.

## Acceptance

- `make check` fully green without a database present.
- With a local Postgres 18 + `TEST_DATABASE_URL`: migrations apply cleanly from empty; re-apply
  of seed is a no-op; sqlc smoke tests pass.
- `go vet`/lint clean on generated code (exclude via config if needed, but committed).
- Vendor gate green (migrations contain no provider names).
- ids codec: 100% of prefixes round-trip; malformed inputs all rejected with typed errors.

## Evidence to return

Summary; deviations with reasons; `git diff --stat` (+ `git status --short` for new files);
tail of `make check`; if you ran DB-backed tests locally, say against what.
