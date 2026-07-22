# RUNBOOK — operational procedures

## First user in production (manual, no personal data in the repo)

User rows are never seeded by migrations. Dev/demo users come from `fixtures/dev-seed.sql`
(generic `*@blueshift.local` identities, applied only by `make demo`/`make dev`/tests). In
staging/production the first (and any subsequent) user is created manually:

1. Create the account in Identity Platform (console or gcloud) with the person's real email.
   That store is the credential authority; the app database only maps email → org/role.
2. Connect to Cloud SQL (`gcloud sql connect <instance> --user=postgres --database=blueshift`)
   and run — substituting the placeholders at the prompt, never committing them anywhere:

   ```sql
   BEGIN;
   INSERT INTO users (email, display_name)
   VALUES ('<email>', '<display name>')
   ON CONFLICT (email) DO NOTHING;

   INSERT INTO memberships (org_id, user_id, role)
   SELECT o.id, u.id, '<editor|approver>'
   FROM orgs o, users u
   WHERE o.name = 'Blueshift Pilot' AND u.email = '<email>'
   ON CONFLICT (org_id, user_id) DO NOTHING;
   COMMIT;
   ```

3. Verify: sign in through the app (`AUTH_MODE=identity`); `GET /api/auth/me` must return the
   expected role.

Rules: this SQL template stays placeholder-only; real values live only in the production
database. See CLAUDE.md "No personal data in the repo — ever."
