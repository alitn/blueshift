import { describe, expect, it } from 'vitest';
import {
  createFollowGate,
  FOLLOW_SUSPEND_MS,
  segmentIndexAt,
  type SegmentTiming
} from './transcriptSync';

// The mapping contract (m1-transcript-sync): containment start_ms ≤ t < end_ms,
// keep-previous through silence gaps, none before the first segment, and the
// last segment holds after the transcript ends. endMs appears in fixtures to
// document the gaps the policy bridges, even though the mapper never reads it.

/** seg builds a timing fixture; endMs documents the gap the policy bridges. */
function seg(startMs: number, endMs: number): SegmentTiming & { endMs: number } {
  return { startMs, endMs };
}

// Three turns with real silence gaps: [1000,2200) … [2600,4600) … [5000,6000).
const GAPPED = [seg(1000, 2200), seg(2600, 4600), seg(5000, 6000)];

/** Linear reference implementation of the same policy, for cross-checking. */
function referenceIndexAt(segments: readonly SegmentTiming[], tMs: number): number {
  let found = -1;
  for (let i = 0; i < segments.length; i++) {
    if (segments[i].startMs <= tMs) found = i;
  }
  return found;
}

describe('segmentIndexAt — degenerate inputs', () => {
  it('returns -1 for an empty transcript at any time', () => {
    expect(segmentIndexAt([], 0)).toBe(-1);
    expect(segmentIndexAt([], -50)).toBe(-1);
    expect(segmentIndexAt([], 999999)).toBe(-1);
  });

  it('handles a single segment across its whole timeline', () => {
    const one = [seg(1000, 2000)];
    expect(segmentIndexAt(one, 0)).toBe(-1); // before first: none
    expect(segmentIndexAt(one, 999)).toBe(-1); // 1ms before start: none
    expect(segmentIndexAt(one, 1000)).toBe(0); // inclusive start boundary
    expect(segmentIndexAt(one, 1999)).toBe(0); // inside
    expect(segmentIndexAt(one, 2000)).toBe(0); // at end: kept (trailing gap)
    expect(segmentIndexAt(one, 100000)).toBe(0); // long after: kept
  });
});

describe('segmentIndexAt — before the first segment', () => {
  it('returns -1 for negative and pre-roll times', () => {
    expect(segmentIndexAt(GAPPED, -1)).toBe(-1);
    expect(segmentIndexAt(GAPPED, 0)).toBe(-1);
    expect(segmentIndexAt(GAPPED, 999)).toBe(-1);
  });

  it('a segment starting at 0 is current at t=0 (the at-rest baseline case)', () => {
    const fromZero = [seg(0, 2200), seg(2600, 4600)];
    expect(segmentIndexAt(fromZero, 0)).toBe(0);
  });
});

describe('segmentIndexAt — containment (start_ms ≤ t < end_ms)', () => {
  it('maps times inside each segment to that segment', () => {
    expect(segmentIndexAt(GAPPED, 1500)).toBe(0);
    expect(segmentIndexAt(GAPPED, 3000)).toBe(1);
    expect(segmentIndexAt(GAPPED, 5500)).toBe(2);
  });

  it('start boundary is inclusive: t = start_ms belongs to that segment', () => {
    expect(segmentIndexAt(GAPPED, 1000)).toBe(0);
    expect(segmentIndexAt(GAPPED, 2600)).toBe(1);
    expect(segmentIndexAt(GAPPED, 5000)).toBe(2);
  });

  it('accepts fractional ms (video currentTime*1000 is a float)', () => {
    expect(segmentIndexAt(GAPPED, 999.999)).toBe(-1);
    expect(segmentIndexAt(GAPPED, 1000.0001)).toBe(0);
    expect(segmentIndexAt(GAPPED, 2599.999)).toBe(0); // still the gap → previous
    expect(segmentIndexAt(GAPPED, 2600.5)).toBe(1);
  });
});

describe('segmentIndexAt — silence gaps keep the previous segment', () => {
  it('t exactly at a segment end stays on that segment (end is exclusive, gap begins)', () => {
    expect(segmentIndexAt(GAPPED, 2200)).toBe(0);
    expect(segmentIndexAt(GAPPED, 4600)).toBe(1);
  });

  it('t anywhere inside a gap stays on the previous segment', () => {
    expect(segmentIndexAt(GAPPED, 2400)).toBe(0); // mid gap 2200→2600
    expect(segmentIndexAt(GAPPED, 2599)).toBe(0); // last ms of the gap
    expect(segmentIndexAt(GAPPED, 4800)).toBe(1); // mid gap 4600→5000
  });

  it('the handoff is exact: the next segment takes over at its own start', () => {
    expect(segmentIndexAt(GAPPED, 2599)).toBe(0);
    expect(segmentIndexAt(GAPPED, 2600)).toBe(1);
  });

  it('back-to-back segments (no gap) hand off at the shared boundary', () => {
    const contiguous = [seg(0, 1000), seg(1000, 2000)];
    expect(segmentIndexAt(contiguous, 999)).toBe(0);
    expect(segmentIndexAt(contiguous, 1000)).toBe(1);
  });
});

describe('segmentIndexAt — after the last segment', () => {
  it('keeps the last segment current beyond its end (trailing gap policy)', () => {
    expect(segmentIndexAt(GAPPED, 6000)).toBe(2);
    expect(segmentIndexAt(GAPPED, 60_000_000)).toBe(2);
  });
});

describe('segmentIndexAt — binary search matches a linear reference', () => {
  it('agrees with the reference on every boundary±1 of a long transcript', () => {
    // 1,000 segments: [i*1000, i*1000+700) with a 300ms gap after each.
    const many = Array.from({ length: 1000 }, (_, i) => seg(i * 1000, i * 1000 + 700));
    const probes: number[] = [-1, 0];
    for (let i = 0; i < many.length; i++) {
      const s = many[i];
      probes.push(s.startMs - 1, s.startMs, s.startMs + 1, s.endMs - 1, s.endMs, s.endMs + 1);
    }
    probes.push(many[many.length - 1].endMs + 12345);
    for (const t of probes) {
      expect(segmentIndexAt(many, t)).toBe(referenceIndexAt(many, t));
    }
  });
});

describe('createFollowGate — auto-follow suspension (~4s, never fight the user)', () => {
  it('follows by default', () => {
    const gate = createFollowGate();
    expect(gate.shouldFollow(0)).toBe(true);
    expect(gate.shouldFollow(123456)).toBe(true);
  });

  it('suspends for FOLLOW_SUSPEND_MS after a manual scroll, then resumes', () => {
    const gate = createFollowGate();
    gate.noteUserScroll(10_000);
    expect(gate.shouldFollow(10_000)).toBe(false);
    expect(gate.shouldFollow(10_000 + FOLLOW_SUSPEND_MS - 1)).toBe(false);
    expect(gate.shouldFollow(10_000 + FOLLOW_SUSPEND_MS)).toBe(true); // idle elapsed
  });

  it('repeated scrolling keeps extending the suspension window', () => {
    const gate = createFollowGate();
    gate.noteUserScroll(0);
    gate.noteUserScroll(3000); // still scrolling → window slides
    expect(gate.shouldFollow(4000)).toBe(false);
    expect(gate.shouldFollow(3000 + FOLLOW_SUSPEND_MS - 1)).toBe(false);
    expect(gate.shouldFollow(3000 + FOLLOW_SUSPEND_MS)).toBe(true);
  });

  it('selecting a segment resumes auto-follow immediately', () => {
    const gate = createFollowGate();
    gate.noteUserScroll(1000);
    expect(gate.shouldFollow(1001)).toBe(false);
    gate.noteSelect();
    expect(gate.shouldFollow(1001)).toBe(true);
  });

  it('honours a custom suspension window', () => {
    const gate = createFollowGate(100);
    gate.noteUserScroll(0);
    expect(gate.shouldFollow(99)).toBe(false);
    expect(gate.shouldFollow(100)).toBe(true);
  });
});
