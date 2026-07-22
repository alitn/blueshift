<script lang="ts">
  import { Tabs as TabsPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: TabsPrimitive.TriggerProps = $props();
</script>

<!-- Tab per DESIGN.md: active = text-primary + 2px accent underline; inactive
     text-muted, hover text-primary. Underline via a bottom border on the active
     data-state so no layout shift. Keyboard focus mirrors hover with accent. -->
<TabsPrimitive.Trigger
  class={cn(
    'relative -mb-px border-b-2 border-transparent px-1 pb-2 font-mono text-[10px] font-semibold uppercase tracking-[0.16em] text-text-muted outline-none transition-colors duration-hover ease-out hover:text-text-primary focus-visible:text-text-primary data-[state=active]:border-accent data-[state=active]:text-text-primary',
    className
  )}
  {...restProps}
/>
