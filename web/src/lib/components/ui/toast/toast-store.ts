import { writable } from 'svelte/store';

/**
 * Toast queue — a small framework-idiomatic store (writable, no external deps).
 * It is a studio primitive (bits-ui ships no toast), so it lives here in
 * components/ui and is consumed only through the local wrappers. Styling is
 * applied by Toaster.svelte from tokens; this module owns queue + lifecycle.
 */

export type ToastVariant = 'default' | 'ok' | 'warn' | 'danger';

export interface ToastOptions {
  title: string;
  description?: string;
  variant?: ToastVariant;
  /** Auto-dismiss delay in ms. 0 (or negative) keeps the toast until dismissed. */
  duration?: number;
}

export interface Toast {
  id: string;
  title: string;
  description?: string;
  variant: ToastVariant;
  duration: number;
}

const DEFAULT_DURATION = 4000;

const store = writable<Toast[]>([]);
const timers = new Map<string, ReturnType<typeof setTimeout>>();
let counter = 0;

function clearTimer(id: string): void {
  const t = timers.get(id);
  if (t !== undefined) {
    clearTimeout(t);
    timers.delete(id);
  }
}

function dismiss(id: string): void {
  clearTimer(id);
  store.update((list) => list.filter((t) => t.id !== id));
}

function push(options: ToastOptions): string {
  const id = `toast-${++counter}`;
  const toastItem: Toast = {
    id,
    title: options.title,
    description: options.description,
    variant: options.variant ?? 'default',
    duration: options.duration ?? DEFAULT_DURATION
  };
  store.update((list) => [...list, toastItem]);
  if (toastItem.duration > 0) {
    timers.set(
      id,
      setTimeout(() => dismiss(id), toastItem.duration)
    );
  }
  return id;
}

function clear(): void {
  for (const id of timers.keys()) clearTimer(id);
  store.set([]);
}

function variant(v: ToastVariant) {
  return (options: Omit<ToastOptions, 'variant'>) => push({ ...options, variant: v });
}

/** Reactive list of active toasts, consumed by Toaster.svelte. */
export const toasts = { subscribe: store.subscribe };

/** Imperative API used by the rest of the app to raise toasts. */
export const toast = {
  push,
  dismiss,
  clear,
  ok: variant('ok'),
  warn: variant('warn'),
  danger: variant('danger')
};
