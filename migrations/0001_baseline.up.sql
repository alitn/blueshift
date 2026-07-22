-- 0001_baseline — the additive-only starting point.
--
-- Conventions enforced on every table (see the repo domain-model rules):
-- internal PK is `bigint GENERATED ALWAYS AS IDENTITY` and never
-- leaves the database; `created_at`/`updated_at` are `timestamptz` in UTC;
-- exposed entities carry a `public_id uuid` (uuidv7, PG18 built-in);
-- user-facing entities carry `deleted_at` for soft delete; status columns are
-- `text` + `CHECK`, never native enums. From here on: additive changes only.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- orgs — the tenant root. One row initially; org_id fans out to every root
-- table and prefixes every storage key.
CREATE TABLE orgs (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id  uuid        NOT NULL DEFAULT uuidv7() UNIQUE,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- shows — a program under an org. Setup auto-creates one.
CREATE TABLE shows (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id  uuid        NOT NULL DEFAULT uuidv7() UNIQUE,
    org_id     bigint      NOT NULL REFERENCES orgs (id),
    title      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX shows_org_id_idx ON shows (org_id);

-- users — seeded identities; no self-service admin UI until M2.
CREATE TABLE users (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email        text        NOT NULL UNIQUE,
    display_name text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

-- memberships — a user's role within an org.
CREATE TABLE memberships (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint      NOT NULL REFERENCES orgs (id),
    user_id    bigint      NOT NULL REFERENCES users (id),
    role       text        NOT NULL CHECK (role IN ('editor', 'approver')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);
CREATE INDEX memberships_user_id_idx ON memberships (user_id);

-- episodes — a source recording moving through the pipeline. `language` is
-- BCP-47 and drives all downstream language behavior via /internal/lang.
CREATE TABLE episodes (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id         uuid        NOT NULL DEFAULT uuidv7() UNIQUE,
    org_id            bigint      NOT NULL REFERENCES orgs (id),
    show_id           bigint      NOT NULL REFERENCES shows (id),
    title             text        NOT NULL,
    source_filename   text        NOT NULL,
    language          text        NOT NULL DEFAULT 'fa', -- BCP-47
    status            text        NOT NULL DEFAULT 'uploaded'
                          CHECK (status IN ('uploaded', 'processing', 'ready', 'failed')),
    duration_ms       bigint,
    master_object_key text,
    proxy_object_key  text,
    error_id          text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE INDEX episodes_org_id_status_idx ON episodes (org_id, status);
CREATE INDEX episodes_show_id_idx ON episodes (show_id);

-- llm_calls — append-only audit of every model call (through /internal/llm).
CREATE TABLE llm_calls (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint      NOT NULL REFERENCES orgs (id),
    episode_id     bigint      REFERENCES episodes (id),
    model          text        NOT NULL,
    prompt_version text        NOT NULL,
    input_hash     text        NOT NULL,
    raw_response   jsonb,
    cost_cents     integer,
    latency_ms     integer,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX llm_calls_org_id_idx ON llm_calls (org_id);
CREATE INDEX llm_calls_episode_id_idx ON llm_calls (episode_id);

-- correction_log — every human correction to a segment (verbatim invariant).
CREATE TABLE correction_log (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       bigint      NOT NULL REFERENCES orgs (id),
    episode_id   bigint      NOT NULL REFERENCES episodes (id),
    segment_idx  integer     NOT NULL,
    before       jsonb       NOT NULL,
    after        jsonb       NOT NULL,
    corrected_by bigint      NOT NULL REFERENCES users (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX correction_log_org_id_idx ON correction_log (org_id);
CREATE INDEX correction_log_episode_id_idx ON correction_log (episode_id);
CREATE INDEX correction_log_corrected_by_idx ON correction_log (corrected_by);

-- config — key/value settings. A NULL org_id is a global default; an org row
-- with the same key overrides it. NULLS NOT DISTINCT so a single global row per
-- key is enforced (a plain UNIQUE would treat every NULL org_id as distinct).
CREATE TABLE config (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint      REFERENCES orgs (id), -- NULL = global default
    key        text        NOT NULL,
    value      jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (org_id, key)
);
