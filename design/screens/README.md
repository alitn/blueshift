# design/screens/ — canonical screen exports

**Status: awaiting export.** The human drops the canonical Claude Design PNG exports here.
These images are the visual ground truth the Reviewer compares screenshots against.

Expected files:

| File | Screen |
|------|--------|
| `library.png` | Library — episode grid/list with live pipeline stage status |
| `episode.png` | Episode view — transcript + ranked moment cards rail |
| `episode-editor.png` | In-place clip editor — sentence-selection trim, filmstrip, caption preview, 9:16 reframe |
| `render-drawer.png` | Render drawer — presets (Reels, Telegram), progress, download |
| `settings.png` | Settings — org, glossary, speaker directory, brand kit |
| `first-run.png` | First-run — sample pre-processed episode onboarding |

Process when files are added or replaced: the Architect diffs new exports against the current
screenshots baselines and opens tasks for each delta. Implementers never treat these files as
editable — they are inputs, not outputs.
