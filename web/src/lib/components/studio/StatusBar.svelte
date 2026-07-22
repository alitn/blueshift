<script lang="ts">
  // Status bar per DESIGN.md: mono 11px text-muted, dot separators text-faintest.
  // Engine label is always Blueshift-neutral (never a provider name). In M0 the
  // live values are placeholders (em dashes); real telemetry lands later.
  let {
    queue = 0,
    storage = '—',
    engine = '—',
    version = 'v0.0.0'
  }: {
    queue?: number;
    storage?: string;
    engine?: string;
    version?: string;
  } = $props();
</script>

<footer
  class="flex h-statusbar flex-none items-center gap-3.5 border-t border-border-subtle bg-bg-3 px-4 font-mono text-[11px] tracking-[0.06em] text-text-muted"
>
  <span>QUEUE {queue}</span>
  <span class="text-text-faintest">·</span>
  <span>STORAGE {storage}</span>
  <span class="text-text-faintest">·</span>
  <span>ENGINE {engine} MS</span>
  <div class="flex-1"></div>
  <span class="text-text-faint">{version}</span>
</footer>
