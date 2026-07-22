# DESIGN.md — the design contract

**Status: PLACEHOLDER.** The human exports Claude Design output into this directory manually;
this file is then transcribed from the design prompt + screenshots and becomes **the single
source of visual truth**. `web/src/lib/tokens.css` is generated to match this file and is the
only place raw hex values may appear (enforced by the hex gate in `make check`).

Rules of the contract:

- Every UI task spec references the relevant screen PNG in `design/screens/`.
- If `design/` and a task spec conflict, the Architect resolves the conflict and updates
  this file **first**, then the spec.
- When the human drops updated PNGs, the Architect diffs them against current baselines and
  opens tasks for the deltas. Baseline updates in `web/tests/__screenshots__/` are authorized
  only by the Architect.

## To be filled from the Claude Design export

### Color tokens
<!-- Exact hex values: background ramp (bg-0..bg-N), surface, border, text ramp,
     accent + accent states, semantic (success/warn/danger), caption-preview colors. -->
| Token | Hex | Usage |
|-------|-----|-------|
| _TBD_ | _TBD_ | _TBD_ |

### Typography
<!-- Type stack (UI font, Persian content font + fallbacks), size scale, weights,
     line heights; transcript & caption-preview specifics (RTL). -->
_TBD_

### Spacing, radii, elevation
<!-- Spacing scale (px), corner radii per component class, shadow/elevation rules. -->
_TBD_

### Component rules
<!-- Per-component notes: buttons (variants/states), cards, moment rail, transcript rows,
     editor chrome, drawers, toasts, focus rings, keyboard-visible states. -->
_TBD_

### Motion
<!-- Durations, easings, reduced-motion behavior. -->
_TBD_
