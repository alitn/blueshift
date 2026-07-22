# Task: m0-gate-proofs — the deliberate-failure demonstrations (AC 2/3/4/6)

**Milestone:** M0 (docs/SPEC-M0.md §Acceptance) · **Type:** procedural proof · **Slug:** `m0-gate-proofs`

## Goal

Prove, once each and with recorded evidence, that the gates actually bite. Nothing ships in
this task; every seeded failure is reverted. Requires m0-ci-deploy done + human prerequisites
(remote, branch protection) in place.

## Proofs

- **P1 (AC2) — failing test blocks deploy.** Branch `proof/red-test`: add a trivially failing
  Go test; push; open PR. Evidence: red `pr` check, GitHub "merge blocked" state. Also show
  red main cannot promote: the promote workflow is manual-only and the staging job's e2e gate
  is what feeds rc tags — evidence that no rc tag is produced from a red run. Close PR,
  delete branch.
- **P2 (AC3) — drifted screenshot blocks merge.** Branch `proof/visual-drift`: change one
  studio component's visual property (via a token *misuse* that the hex gate permits, e.g.
  swapping a token variable) so rendered output drifts; push; PR. Evidence: Playwright
  `toHaveScreenshot` diff failure in CI, merge blocked, diff artifact uploaded. **Do not
  touch baseline files.** Close PR, delete branch.
- **P3 (AC6) — vendor leak + raw hex fail the build.** Locally (and optionally on a proof
  branch): (a) add a provider name string to a `web/src` file → `make check` fails at
  vendor gate; (b) add a raw hex color to a component → fails at hex gate. Capture both
  outputs; revert. If done on a branch, show the red CI run too.
- **P4 (AC4) — red commit is impossible.** Locally: with a deliberately failing test in the
  tree, attempt `git commit` — show the PreToolUse/commit-gate hook and `.githooks/pre-commit`
  each block (capture output); revert.

## Rules

- Every seeded defect is committed only on `proof/*` branches, never `main`/`master`.
- Baselines untouched. No gate weakened, even temporarily.
- Evidence (CI run URLs, output tails) goes into `tasks/queue.md` log via the Architect and
  a summary in `docs/SPEC-M0.md` acceptance sign-off section (Architect writes both; you
  return the raw evidence).

## Acceptance

All four proofs demonstrated with captured evidence; working tree and main branch left
byte-identical to before the task; `make check` green.

## Evidence to return

Per proof: what was seeded, the red evidence (verbatim tails/URLs), the revert confirmation.
