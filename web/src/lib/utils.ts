/**
 * cn — minimal class-name joiner. Filters falsy values, flattens arrays, and
 * expands `{ 'class': condition }` records, so conditional class expressions
 * read cleanly in components. The value type matches the shape bits-ui passes
 * for its `class` prop (clsx-compatible) so the wrappers type-check without
 * pulling clsx/tailwind-merge into the dependency set — the token-based utility
 * set has no conflicting arbitrary values that would need merging away.
 */
export type ClassDictionary = Record<string, unknown>;
export type ClassArray = ClassValue[];
export type ClassValue =
  | ClassArray
  | ClassDictionary
  | string
  | number
  | bigint
  | null
  | boolean
  | undefined;

export function cn(...inputs: ClassValue[]): string {
  const out: string[] = [];
  for (const input of inputs) {
    if (input === null || input === undefined || input === false || input === true) continue;
    if (typeof input === 'string') {
      if (input) out.push(input);
    } else if (typeof input === 'number' || typeof input === 'bigint') {
      out.push(String(input));
    } else if (Array.isArray(input)) {
      const nested = cn(...input);
      if (nested) out.push(nested);
    } else {
      for (const [key, value] of Object.entries(input)) {
        if (value) out.push(key);
      }
    }
  }
  return out.join(' ');
}
