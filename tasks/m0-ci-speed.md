# Task: m0-ci-speed — cut PR CI wall-clock, measured

**Milestone:** M0 · **Type:** CI performance · **Slug:** `m0-ci-speed`

## Baseline (run 29975381569, green): ~5.5 min wall; big steps: make check 128s, e2e 41s, playwright install 16s, ffmpeg+migrate 15s

## Scope (pr.yml + Makefile only where needed)

1. **Kill the duplicate web build:** `make check` builds web+go; then `make e2e`'s demo boot
   runs `make build` again. In CI, let the demo boot reuse the existing build when fresh
   (env flag e.g. `BS_SKIP_BUILD=1` set by pr.yml between check and e2e, honored by
   tools/demo/lib.sh's build step — guard so local behavior unchanged by default).
2. **Split into two parallel jobs**: `go` (setup-go, lint pin, go-side of make check split
   OR keep make check whole in one job and move e2e+web to the second — choose the split
   that keeps `make check` semantics intact somewhere; simplest: job A = full make check;
   job B = e2e (needs web deps + browsers + service container only). Branch protection's
   required check must cover BOTH (rename/require both contexts — note the new required
   check names for the Architect to update the ruleset).
3. **Cache hard:** playwright browsers (~/.cache/ms-playwright keyed on the playwright
   version), bun cache, keep setup-go cache. apt ffmpeg is 15s — leave unless trivial.
4. **Path filter:** skip the e2e job (not make check) when the diff touches only *.md,
   docs/, tasks/, design/ (paths-ignore on job level via dorny/paths-filter or
   `paths-ignore` on a split workflow — choose the simplest that can't accidentally skip
   required checks on code PRs; if a required check would be skipped-but-required, use the
   filter action inside the job to early-exit success instead).
5. **Measure:** after landing, the Architect will time the next PR run; include in your
   report your predicted wall-clock and the reasoning.

## Acceptance

- make check green locally; YAML parses; local make demo/dev behavior unchanged (flag off).
- Honest note of any new required-check names for the ruleset.

## Evidence

Summary; diff; predicted timings; validation.
