<script lang="ts">
  import { Tabs as TabsPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: TabsPrimitive.ListProps = $props();
</script>

<TabsPrimitive.List
  class={cn('flex items-center gap-4 border-b border-border-subtle', className)}
  {...restProps}
/>
