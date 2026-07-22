<script lang="ts">
  import type { Snippet } from 'svelte';
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import AppShell from '$lib/components/studio/AppShell.svelte';
  import { Toaster } from '$lib/components/ui/toast';
  import { ensureSession, postLogout } from '$lib/auth';

  let { children }: { children?: Snippet } = $props();

  // The login route renders bare (no app chrome, no guard). Every other route
  // is gated: the shell calls /api/auth/me on mount and redirects to /login on
  // 401 before revealing any content.
  const isLogin = $derived($page.url.pathname.startsWith('/login'));
  let ready = $state(false);

  onMount(async () => {
    if (isLogin) {
      ready = true;
      return;
    }
    ready = await ensureSession(goto);
  });

  async function signOut() {
    await postLogout();
    await goto('/login');
  }
</script>

{#if isLogin}
  {@render children?.()}
{:else if ready}
  <AppShell onSignOut={signOut}>
    {@render children?.()}
  </AppShell>
{/if}

<Toaster />
