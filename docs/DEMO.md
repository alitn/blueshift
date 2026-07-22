# DEMO.md — the M1 demo script (skeleton)

Target: end-to-end on **staging**, under **15 minutes**, **zero live-processing waits**
(the sample episode is pre-processed; nothing renders live except the final clip, which is
short). Filled in as M1 lands; timings measured on a dry run before every showing.

## Preconditions

- [ ] Staging deployed from the release-candidate tag; `/healthz` green.
- [ ] Sample episode (1-hr Persian interview fixture) pre-processed: transcript, speakers,
      moments all `ready`.
- [ ] Demo user seeded (editor role, self-approval on).
- [ ] Reset procedure run: _TBD (script that restores the demo org to pristine state)_.

## Script

| # | Beat | Action | What to say | Time |
|---|------|--------|-------------|------|
| 1 | First-run | Sign in fresh | Trust: it comes with a worked example | 1 min |
| 2 | Library | Open library, show stage status | Pipeline is visible, never a black box | 1 min |
| 3 | Episode | Open sample episode | Transcript, speakers with evidence | 2 min |
| 4 | Moments | Walk ranked cards: rationale + Persian quote | LLMs decide, they never measure | 2 min |
| 5 | Three clicks | Approve-as-is → render → done | The headline: 3 clicks to a clip | 1 min |
| 6 | Adjust | Open editor: trim by sentence, filmstrip, flash-frame warning | Broadcast-grade control | 3 min |
| 7 | Captions | Live Persian preview; show fidelity checker blocking a seeded mismatch | Verbatim invariant, enforced | 2 min |
| 8 | Reframe | Per-shot 9:16 preview | — | 1 min |
| 9 | Export | Render drawer: Reels + Telegram, download | — | 1 min |
| 10 | Trust | Show audit trail for everything just done | Every decision recorded | 1 min |

## Fallbacks

_TBD: what to do if staging is down (local `make demo`), if render is slow (pre-rendered
clip), etc._
