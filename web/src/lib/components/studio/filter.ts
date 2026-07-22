/**
 * Client-side Library filtering: the status chips and the free-text search both
 * run in the browser (M0 orgs are small; no server-side paging). Kept as a pure
 * module so the logic is unit-tested without mounting the page.
 */
import { isTerminal, type Episode } from '$lib/episodes';

export type EpisodeFilter = 'all' | 'processing' | 'ready' | 'failed';

/** counts tallies episodes per chip. PROCESSING covers every non-terminal
 *  state (uploaded + processing), matching the pipeline the user is waiting on. */
export function counts(episodes: Episode[]): Record<EpisodeFilter, number> {
  const c: Record<EpisodeFilter, number> = { all: 0, processing: 0, ready: 0, failed: 0 };
  for (const e of episodes) {
    c.all += 1;
    if (e.status === 'ready') c.ready += 1;
    else if (e.status === 'failed') c.failed += 1;
    else c.processing += 1; // uploaded or processing
  }
  return c;
}

function matchesFilter(e: Episode, filter: EpisodeFilter): boolean {
  switch (filter) {
    case 'all':
      return true;
    case 'ready':
      return e.status === 'ready';
    case 'failed':
      return e.status === 'failed';
    case 'processing':
      return !isTerminal(e.status);
  }
}

function matchesQuery(e: Episode, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === '') return true;
  return (
    e.title.toLowerCase().includes(q) ||
    e.sourceFilename.toLowerCase().includes(q) ||
    e.id.toLowerCase().includes(q)
  );
}

/** applyFilter returns the episodes matching both the active chip and the
 *  search query (title / filename / id substring, case-insensitive). */
export function applyFilter(
  episodes: Episode[],
  filter: EpisodeFilter,
  query: string
): Episode[] {
  return episodes.filter((e) => matchesFilter(e, filter) && matchesQuery(e, query));
}
