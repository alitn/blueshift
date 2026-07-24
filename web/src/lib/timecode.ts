/**
 * formatTimecode renders a millisecond offset as zero-padded mm:ss. Minutes
 * are not capped at 60 — a 74-minute mark reads "74:12" (broadcast-style
 * running minutes, matching the prototype). Non-finite or negative input
 * clamps to 00:00. Shared by the transcript pane and the moment rail; render
 * it in the mono stack for tabular digits.
 */
export function formatTimecode(ms: number): string {
  const totalSeconds = Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0;
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds % 60;
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${pad(m)}:${pad(s)}`;
}
