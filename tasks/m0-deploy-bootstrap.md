# Task: m0-deploy-bootstrap — fixes found during first real provisioning/rollout

**Milestone:** M0 · **Type:** CI/deploy fix · **Slug:** `m0-deploy-bootstrap`

## Findings from the first live run (project video-clipping-503022, run 29966051914)

1. **`deploy/gcloud.sh`:** `gcloud sql instances create --database-version=POSTGRES_18` now
   defaults to ENTERPRISE_PLUS edition, which rejects `db-g1-small`:
   `Invalid Tier (db-g1-small) for (ENTERPRISE_PLUS) Edition`. The Architect created the
   instance manually with `--edition=enterprise`; script must match.
2. **`.github/workflows/deploy.yml`:** first-ever deploy failed with
   `ERROR: (gcloud.run.deploy) --no-traffic not supported when creating a new service.`
   Everything before it (WIF auth, Docker build+push) succeeded.

## Scope

1. `deploy/gcloud.sh`: add `--edition=enterprise` to the sql create (comment: PG18 defaults
   to ENTERPRISE_PLUS which forbids shared-core tiers).
2. `deploy.yml` bootstrap path: before the candidate deploy, detect whether the service
   exists (`gcloud run services describe blueshift-app`). If it does NOT (first deploy):
   deploy **without** `--no-traffic` (traffic to a brand-new service harms nothing — there
   are no users and no previous revision), keep `--tag candidate`, then **skip the
   10%-shift and watch steps** (there is no stable revision to split against) but still run
   migrations BEFORE the deploy's smoke and still run the candidate smoke — order for
   bootstrap: deploy → migrate → smoke → done. If it DOES exist: current behavior exactly
   (no-traffic → migrate → smoke → 10% → watch → 100%). Implement via a step that sets a
   `bootstrap=true/false` output consumed by later steps' `if:` conditions.
3. `deploy/README.md`: one short paragraph documenting the bootstrap path.

## Acceptance

- `make check` green; YAML parses; `bash -n` clean.
- Reviewer walks both paths: bootstrap (steps skipped correctly, smoke still gates) and
  steady-state (byte-equivalent to current behavior).
- No other behavior change.

## Evidence

Summary; diffs; validation output.
