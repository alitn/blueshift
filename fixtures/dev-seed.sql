-- fixtures/dev-seed.sql — dev/demo user identities only.
--
-- Applied by `make dev-seed` (and, from m0-demo-seed, by `make demo`/`make dev`).
-- NEVER applied in staging or production: those environments provision users
-- per docs/RUNBOOK.md. Keep this file free of real personal data.
--
-- Idempotent: every insert is guarded, so re-running is a no-op. Depends on the
-- "Blueshift Pilot" org and its baseline rows from migration 0002.

-- Two dev users: one approver, one editor.
INSERT INTO users (email, display_name) VALUES
    ('dev-approver@blueshift.local', 'Dev Approver'),
    ('dev-editor@blueshift.local', 'Dev Editor')
ON CONFLICT (email) DO NOTHING;

-- Memberships in the pilot org: approver approves, editor edits.
INSERT INTO memberships (org_id, user_id, role)
SELECT o.id, u.id, 'approver'
FROM orgs o, users u
WHERE o.name = 'Blueshift Pilot' AND u.email = 'dev-approver@blueshift.local'
ON CONFLICT (org_id, user_id) DO NOTHING;

INSERT INTO memberships (org_id, user_id, role)
SELECT o.id, u.id, 'editor'
FROM orgs o, users u
WHERE o.name = 'Blueshift Pilot' AND u.email = 'dev-editor@blueshift.local'
ON CONFLICT (org_id, user_id) DO NOTHING;
