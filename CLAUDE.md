# Blueshift Studio

Broadcast-grade web app that turns long-form TV interviews into approved, captioned social clips.
English-first in UI, API, code, and docs. Persian (`fa`) is the first supported **content** language,
implemented behind the `/internal/lang` abstraction — additional languages are additive, never structural.

## Stack summary

- **Backend:** Go. `cmd/app` = API server with embedded UI (`go:embed` of the web build). `cmd/worker` = pipeline entrypoint for Cloud Run Jobs (args: episode public_id, stage). No queue, no Redis.
- **Frontend:** SvelteKit (adapter-static), TypeScript strict. Interaction primitives = vendored shadcn-svelte on Bits UI in `web/src/lib/components/ui/`; bespoke studio components in `web/src/lib/components/studio/`. All color/type/spacing from `web/src/lib/tokens.css`, generated from `design/DESIGN.md`.
- **Data:** Cloud SQL PostgreSQL 18 (sqlc; pgvector, pg_trgm). Migrations via golang-migrate, **additive-only**.
- **Storage:** single GCS bucket, prefixes `masters/ proxies/ clips/`, signed URLs, keys prefixed `{org_id}/{episode_id}/...`.
- **AI:** ASR behind `/internal/asr` (Chirp 2 first impl); LLMs behind `/internal/llm` (Gemini/Claude), all responses JSON-schema-validated, all calls audited in `llm_calls`.
- **Media:** ffmpeg wrappers in `/internal/media` (proxy, scdet shots, cut, crop, libass caption burn).
- **Cloud:** Google Cloud us-central1 — Cloud Run service + Jobs (one image), Secret Manager, Identity Platform (roles live in Postgres), Cloud Logging + Error Reporting, `/healthz` `/readyz`. **Not used without an ADR:** Kubernetes, Pub/Sub, Redis, Terraform, microservices, separate frontend host, Sentry.

## Operating model — three roles, strict separation

- **Architect** (main session, Fable/max). The only role the human talks to. Does architecture, task decomposition, ADRs, milestone tracking, orchestrates the two subagents, arbitrates review disputes, authorizes commits and screenshot-baseline updates. **HARD RULE: the Architect never edits source files and never writes implementation code.** It writes only `CLAUDE.md`, files under `docs/`, and task specs under `tasks/`. For any non-trivial work it uses plan mode first and presents the plan to the human before dispatching.
- **Implementer** (`.claude/agents/implementer.md`, Opus). Receives a single task spec. Implements code AND tests together, runs `make check`, self-fixes until green. For UI tasks additionally runs the UI self-verification loop (below) and attaches screenshots. Returns summary, diffstat, test results, screenshot paths, open questions. Never commits without an APPROVED verdict relayed by the Architect.
- **Reviewer** (`.claude/agents/reviewer.md`, Opus). Adversarial by charter: its job is to find reasons to REJECT. Sees the task spec + the diff + (UI tasks) captured screenshots and `design/screens/` references — never the Implementer's reasoning. Returns exactly one verdict: `APPROVE` or `REJECT: [numbered findings, file:line, severity]`. Never edits files.

**The loop (every task):** Architect writes spec → Implementer implements → Reviewer reviews → on REJECT the Architect relays findings (max 3 cycles, then escalate to the human) → on APPROVE the Architect authorizes and the Implementer commits `feat|fix|chore(scope): summary [slug]`. Reviewer and Implementer are siblings orchestrated by the Architect — the reviewed party never manages its reviewer.

## Deterministic gates

- `.claude/settings.json` hooks: **PostToolUse** on `Write|Edit` runs the matching formatter on the touched file; **PreToolUse** on `Bash` matching `git commit` runs `make check` and blocks on nonzero exit.
- Real git hooks via `make setup` (`.githooks/pre-commit` → `make check`).
- Consequence: **nothing red can ever be committed, regardless of what any agent believes.**

## Quality assurance — `make check` is the single truth

`make check`: `gofmt -l` (must be empty) → `go vet` → `golangci-lint` → `go test ./... -race` → `cd web && svelte-check && eslint && vitest run` → builds → **vendor-leak gate**: grep `web/`, `/internal/api` DTOs, and migration seed data against the forbidden provider-name list (chirp, gemini, vertex, google, speech-to-text, anthropic, elevenlabs, …); any match fails the build. Also: hex gate — raw hex colors outside `tokens.css` (including `bg-[#…]` arbitrary-value utilities) fail the build.

Other targets: `make e2e`, `make eval`, `make demo`, `make setup`, `make dev`.

AI-output QA: every LLM call goes through `/internal/llm` with a JSON schema (invalid → one retry → hard fail). Golden tests (`make eval`): diarization text-anchor merge stability on fixtures; caption fidelity checker catches 100% of seeded mismatches; ZWNJ normalization is idempotent; rendered `.ass` matches golden files byte-for-byte. **Any change to prompts or `/internal/llm` requires `make eval`**; CI runs it on every PR. External APIs are recorded/replayed in tests; one nightly live smoke on one fixture opens an issue on drift.

## UI self-verification loop — no human needed to find UI bugs

`make demo` boots the full stack locally with deterministic seeded data (sample episode fixture, mocked AI via recorded fixtures) so agents can drive every flow offline. Definition of Done for ANY UI task adds, on top of `make check`:

1. Component tests (vitest + testing-library) for logic-bearing components.
2. Playwright E2E for the touched flow against `make demo`, including keyboard paths (J/K/L, single-key approve).
3. Visual regression: Playwright `toHaveScreenshot` at 1440×900 (and 1280×800) against committed baselines in `web/tests/__screenshots__/`. **Baseline updates are a deliberate act only the Architect authorizes**, never a side effect of making tests pass.
4. Screenshot evidence: capture the changed screens to `.artifacts/screens/<task-slug>/` for the Reviewer to view against `design/screens/`.
5. Token conformance assertions: computed styles of key elements match `tokens.css` (background ramp, accent, fonts); the hex gate greps the diff for raw hex values outside `tokens.css` and fails.
6. RTL assertions on transcript/caption components: direction, alignment, ZWNJ preservation in rendered DOM.
7. axe-core smoke on changed pages (no new critical violations).

The Reviewer REJECTS UI work whose screenshots visibly drift from `design/` even if all automated checks pass. The human sees UI only at milestone demos.

**Fast UI loop (2026-07-22, human-approved).** The Architect keeps `make dev` running (Vite HMR + seeded backend) and owns its lifecycle — agents never start/stop the human's servers, only the Architect-managed one. UI verification and screenshot evidence go through the **Playwright MCP server** (`.mcp.json`, isolated bundled Chromium — never a personal browser) against the running dev server; ad-hoc headless capture scripts are the fallback only. **Tiered checks:** while iterating, run targeted checks (affected vitest, svelte-check, eslint on touched files); run the full `make check` once before requesting review. For **tiny UI diffs (≤ ~20 changed lines, no logic)** the Reviewer verifies evidence + targeted checks and may rely on the commit-gate hook for the full suite — the hook remains the deterministic backstop for every commit.

`design/` is the single source of visual truth (see `design/DESIGN.md`). `web/src/lib/tokens.css` is generated to match `DESIGN.md` and is the only place raw hex values may appear. If `design/` and a spec conflict, the Architect resolves and updates `DESIGN.md` first.

## Domain model & data modeling

Hierarchy: **Org → Show → Episode → Moment → Clip**. There is no "project" concept.

- Tenancy from day one, invisible until needed: `orgs` (one row initially); `org_id` FK on every root table; every query org-scoped; storage keys prefixed `{org_id}/`. `shows` table; `episodes.show_id`; setup auto-creates one show. `users` + `memberships(org_id, user_id, role)` with role in `('editor','approver')`; users are seeded, no admin UI until M2; config flag `allow_self_approval=true` (default on until the M2 roles split). `brand_kits`, `glossary_terms`, `speaker_directory` keyed by `org_id`; `brand_kits.show_id` nullable now, unused until per-show styles.
- Additive-only rule: new product concepts may only add new tables or new nullable columns. Never repurpose, rename, or delete in the same release.
- IDs: internal PK = `bigint GENERATED ALWAYS AS IDENTITY` (never exposed). External = `public_id uuid NOT NULL DEFAULT uuidv7() UNIQUE` on every exposed entity; API/URLs render prefixed base32 via `/internal/ids` (`ep_`, `sh_`, `mo_`, `clip_`, `sp_`). Incremental IDs never leave the database.
- Transcripts: `segments` (one row per speaker turn): `episode_id, idx, speaker_id, start_ms int, end_ms int, text, words jsonb` — words = array of `[w, start_ms, end_ms, conf]`; `embedding vector` (pgvector) for semantic moment search; pg_trgm index on `text`. Moments reference (segment span + word offsets). Corrections rewrite one segment's words + insert into `correction_log` (use PG18 OLD/NEW RETURNING).
- Language as data, never as identity: `episodes.language` (BCP-47 text; 'fa' is the first) drives everything downstream; `glossary_terms.language`; RTL, ZWNJ handling, caption shaping, and engine choice are resolved through the `/internal/lang` registry at runtime. Adding a language = a new `lang/<code>` package + config rows + fixtures; zero schema changes, zero UI changes beyond rendering what the registry declares. No fa assumptions outside `lang/fa`.
- Conventions: `timestamptz` UTC only; status fields = text + CHECK constraint (not native enums); `llm_calls` audit table (model, prompt_version, input_hash, raw_response jsonb, cost); soft delete via `deleted_at` on user-facing entities; money in integer cents; platform presets as config rows (jsonb), not code; no partitioning below tens of millions of rows.

## Standing rules

**UI component policy.** Interaction primitives (dialog, drawer, popover, dropdown, tooltip, tabs, select, toast, panes) come from vendored shadcn-svelte components built on Bits UI, restyled exclusively from tokens, and are consumed only through the local wrappers in `components/ui/` — never imported ad hoc, never hand-rolled. All studio-specific components are hand-rolled in `components/studio/`. No other UI kits (Skeleton, Flowbite, DaisyUI, etc.) without an ADR. The Tailwind theme is generated from `tokens.css`; arbitrary-value color utilities (e.g. `bg-[#…]`) are added to the leak/hex gate and fail the build.

**Vendor neutrality — the stack never leaks.** External provider and model names (Chirp, Google, Vertex, Gemini, ElevenLabs, etc.) must never appear in UI strings, API paths/payloads/DTOs, client-visible errors, exported files, public IDs, or customer-facing docs. Externally, everything is a Blueshift engine with our own labels (`engine: bs-asr-1`, `model: bs-lm-2.1`, `ENGINE 142 MS`). Provider errors are caught at the `/internal/asr` and `/internal/llm` boundaries and mapped to neutral messages with internal error IDs; raw provider errors go to server logs only. Provider names may exist only in `/internal/asr`, `/internal/llm`, `/internal/lang` engine-selection config, deploy scripts, server logs, and internal repo docs (specs/ADRs are internal and may name providers). The vendor-leak gate in `make check` enforces this deterministically, and the Reviewer rejects any client-visible surface that hints at the underlying stack.

**Occam with teeth:** no new dependency, service, or abstraction without a human-approved ADR.

**Research before solving (human-directed 2026-07-23).** For any non-trivial problem —
especially anything touching an external system, provider API, protocol, or unfamiliar
domain — do extensive online research first (official docs, issue trackers, community
patterns) before diagnosing, speccing, or designing. Never guess; never invent a custom
protocol or mechanism where a documented one exists. Specs and ADRs for such work cite
the sources they are based on.

**No personal data in the repo — ever.** Real names, personal emails, or any other personal data of the human (or anyone else) must never appear in code, migrations, seeds, tests, fixtures, docs, or commits. Dev/demo identities are generic (`dev-approver@blueshift.local`, `dev-editor@blueshift.local`, display names "Dev Approver"/"Dev Editor"). Production users are created manually per `docs/RUNBOOK.md` — values supplied at run time, never committed.

**Never touch the human's running processes.** Agents must never kill, restart, or reuse the human's applications (browsers especially). Headless captures use an isolated browser instance with a dedicated temporary `--user-data-dir`, and terminate only the PID they spawned. Prefer the Playwright-bundled Chromium once the harness exists.

**Verbatim invariant:** caption text is copied from ASR output, never generated; timestamps come only from ASR/ffmpeg; LLMs decide, they never measure. Every human correction is written to `correction_log`.

Tasks >~1 day get split by the Architect. The human sees plans before execution and demos at milestone gates; everything else runs through the loop.
