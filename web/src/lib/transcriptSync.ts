/**
 * Player ↔ transcript sync logic (m1-transcript-sync): the pure ms → segment
 * mapping and the auto-follow suspension gate. Both are plain functions with
 * no DOM or Svelte dependency so they are exhaustively unit-testable; the
 * TranscriptPane and the episode route consume them.
 *
 * ## Current-segment policy (the contract)
 *
 * - A segment is current while `start_ms ≤ t < end_ms`.
 * - In a silence gap between segments the PREVIOUS segment stays current until
 *   the next one begins — no flicker, no dead zones.
 * - Before the first segment nothing is current (index -1).
 * - After the last segment ends the last segment stays current (it is the
 *   trailing "gap").
 *
 * For segments sorted by start (non-overlapping, as the transcript API
 * guarantees: ordered by idx with monotonic times), those four rules collapse
 * to one: **the current segment is the last one with `start_ms ≤ t`**, or none
 * when no segment has started yet. `end_ms` never needs consulting — the
 * keep-previous gap rule extends every segment to the start of its successor.
 *
 * ## Auto-follow policy (documented per the task spec)
 *
 * The transcript auto-scrolls the current segment into view only when it is
 * outside the pane's viewport. We never fight the user: any manual scroll
 * intent on the pane (wheel, touch, scrollbar/pointer grab, scroll keys)
 * suspends auto-follow for {@link FOLLOW_SUSPEND_MS} (~4s). Auto-follow
 * resumes on the first segment change after that idle window has elapsed, or
 * immediately when the user clicks/activates a segment (an explicit "take me
 * there" beats a stale "leave me alone").
 */

/** The minimal timing shape the mapper needs (a TranscriptSegment satisfies it). */
export type SegmentTiming = { startMs: number };

/**
 * segmentIndexAt returns the index of the segment current at `tMs`, or -1 when
 * playback has not reached the first segment. Segments must be sorted by
 * startMs (the API's idx order). Binary search: O(log n) at the player's ~4Hz
 * cadence over multi-thousand-segment interviews.
 */
export function segmentIndexAt(segments: readonly SegmentTiming[], tMs: number): number {
  let lo = 0;
  let hi = segments.length - 1;
  let found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (segments[mid].startMs <= tMs) {
      found = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return found;
}

/** How long a manual transcript scroll suspends auto-follow, in ms (~4s). */
export const FOLLOW_SUSPEND_MS = 4000;

/** FollowGate decides whether the pane may auto-scroll right now. */
export type FollowGate = {
  /** A manual scroll intent (wheel/touch/pointer/scroll key) at time `now`. */
  noteUserScroll(now: number): void;
  /** The user activated a segment: an explicit jump resumes follow at once. */
  noteSelect(): void;
  /** True when auto-follow may scroll at time `now`. */
  shouldFollow(now: number): boolean;
};

/**
 * createFollowGate builds the auto-follow suspension gate. Time is passed in
 * (epoch ms) rather than read from a clock so tests control it exactly.
 */
export function createFollowGate(suspendMs: number = FOLLOW_SUSPEND_MS): FollowGate {
  let suspendedUntil = Number.NEGATIVE_INFINITY;
  return {
    noteUserScroll(now: number): void {
      suspendedUntil = now + suspendMs;
    },
    noteSelect(): void {
      suspendedUntil = Number.NEGATIVE_INFINITY;
    },
    shouldFollow(now: number): boolean {
      return now >= suspendedUntil;
    }
  };
}
