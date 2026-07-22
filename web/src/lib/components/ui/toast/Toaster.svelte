<script lang="ts">
  import { toast, toasts, type ToastVariant } from './toast-store';

  const dotClass: Record<ToastVariant, string> = {
    default: 'bg-text-faint',
    ok: 'bg-ok',
    warn: 'bg-warn',
    danger: 'bg-danger'
  };
</script>

<div
  class="pointer-events-none fixed bottom-4 right-4 z-50 flex w-[320px] flex-col gap-2"
  role="region"
  aria-label="Notifications"
  aria-live="polite"
>
  {#each $toasts as t (t.id)}
    <div
      class="animate-content-in pointer-events-auto flex items-start gap-2 rounded-4 border border-border-control bg-bg-4 p-3 text-text-primary"
      role="status"
    >
      <span class={`mt-1 h-1.5 w-1.5 flex-none rounded-full ${dotClass[t.variant]}`}></span>
      <div class="min-w-0 flex-1">
        <div class="text-[11px] font-semibold leading-tight">{t.title}</div>
        {#if t.description}
          <div class="mt-1 text-[11px] leading-snug text-text-muted">{t.description}</div>
        {/if}
      </div>
      <button
        type="button"
        class="flex-none font-mono text-[9px] uppercase tracking-wider text-text-faint transition-colors duration-hover ease-out hover:text-text-primary"
        aria-label="Dismiss notification"
        onclick={() => toast.dismiss(t.id)}
      >
        ✕
      </button>
    </div>
  {/each}
</div>
