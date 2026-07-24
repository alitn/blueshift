<script lang="ts">
  // Pipeline hover-card BODY (presentational; the tooltip wiring and lazy fetch
  // live in PipelineHoverCard). Five stage rows — display name (mono 11px,
  // SPEAKERS/MOMENTS product terms), a status dot colored from tokens
  // (done=step-done, active=accent w/ subtle pulse, failed=danger,
  // pending/unreached=border-default), a right-aligned mono duration for
  // finished stages, the public engine label faint under the name, and the
  // per-stage cost when known — plus the QUEUED / TOTAL footer. Loading =
  // skeleton lines; error = neutral copy. All colors/spacing from tokens.
  import {
    cardRows,
    engineDisplay,
    formatCents,
    formatStageDuration,
    totalCostCents,
    STAGE_DISPLAY,
    type PipelineDetails,
    type PipelineStageStatus
  } from '$lib/pipelineDetails';

  let {
    details,
    loading = false,
    error = false
  }: {
    details?: PipelineDetails;
    loading?: boolean;
    error?: boolean;
  } = $props();

  const rows = $derived(cardRows(details));
  const totalCost = $derived(totalCostCents(details));

  // Token-backed dot fills per stage status (mirrors the pipeline bars).
  const DOT_BG: Record<PipelineStageStatus, string> = {
    done: 'bg-step-done',
    active: 'animate-pulse bg-accent',
    failed: 'bg-danger',
    pending: 'bg-border-default',
    unreached: 'bg-border-default'
  };
</script>

<div class="w-[218px]" data-testid="pipeline-card">
  {#if loading}
    <!-- Skeleton: stand-in rows while the lazy fetch resolves. -->
    <div class="flex flex-col gap-2 py-1" data-testid="pipeline-card-loading" aria-hidden="true">
      <div class="h-2 w-3/4 animate-pulse rounded-2 bg-bg-5"></div>
      <div class="h-2 w-full animate-pulse rounded-2 bg-bg-5"></div>
      <div class="h-2 w-2/3 animate-pulse rounded-2 bg-bg-5"></div>
    </div>
  {:else if error}
    <!-- Neutral failure copy: no cause, no stack detail. -->
    <div class="py-1 font-mono text-[10.5px] tracking-[0.08em] text-text-faint" data-testid="pipeline-card-error">
      DETAILS UNAVAILABLE
    </div>
  {:else}
    {#each rows as row (row.name)}
      <div class="flex items-center gap-2.5 py-[3px]" data-testid="pipeline-card-row" data-stage={row.name} data-status={row.status}>
        <span class="h-1.5 w-1.5 flex-none rounded-full {DOT_BG[row.status]}" data-testid="pipeline-card-dot" aria-hidden="true"></span>
        <div class="min-w-0 flex-1">
          <div
            class="font-mono text-[11px] tracking-[0.08em] {row.status === 'unreached' || row.status === 'pending'
              ? 'text-text-faint'
              : 'text-text-primary'}"
          >
            {STAGE_DISPLAY[row.name] ?? row.name.toUpperCase()}
          </div>
          {#if row.engine}
            <div class="font-mono text-[9.5px] tracking-[0.08em] text-text-faint" data-testid="pipeline-card-engine">
              {engineDisplay(row.engine)}
            </div>
          {/if}
        </div>
        <div class="flex-none text-right">
          {#if row.durationMs !== undefined}
            <div class="font-mono text-[11px] tabular-nums text-text-muted" data-testid="pipeline-card-duration">
              {formatStageDuration(row.durationMs)}
            </div>
          {/if}
          {#if row.costCents !== undefined}
            <div class="font-mono text-[9.5px] tabular-nums text-text-faint" data-testid="pipeline-card-cost">
              {formatCents(row.costCents)}
            </div>
          {/if}
        </div>
      </div>
    {/each}
    <div class="mt-2 border-t border-border-hairline pt-2">
      <div class="flex items-center justify-between font-mono text-[10px] tracking-[0.08em]">
        <span class="text-text-faint">QUEUED</span>
        <span class="tabular-nums text-text-muted" data-testid="pipeline-card-queued">
          {formatStageDuration(details?.queuedMs)}
        </span>
      </div>
      <div class="mt-1 flex items-center justify-between font-mono text-[10px] tracking-[0.08em]">
        <span class="text-text-faint">TOTAL</span>
        <span class="tabular-nums text-text-muted" data-testid="pipeline-card-total">
          {formatStageDuration(details?.totalMs)}{#if totalCost !== undefined}
            <span class="text-text-faint">&nbsp;·&nbsp;{formatCents(totalCost)}</span>
          {/if}
        </span>
      </div>
    </div>
  {/if}
</div>
