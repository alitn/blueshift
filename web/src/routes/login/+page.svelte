<script lang="ts">
  import { goto } from '$app/navigation';
  import { postLogin, loginErrorMessage } from '$lib/auth';

  let email = $state('');
  let password = $state('');
  let error = $state('');
  let submitting = $state(false);

  async function submit(event: Event) {
    event.preventDefault();
    if (submitting) return;
    error = '';
    submitting = true;
    const result = await postLogin(email, password);
    submitting = false;
    if (result.ok) {
      await goto('/');
      return;
    }
    error = loginErrorMessage(result.error);
  }
</script>

<svelte:head>
  <title>Sign in · Blueshift Studio</title>
</svelte:head>

<main class="flex h-screen items-center justify-center bg-bg-2 p-6">
  <div class="w-[340px] max-w-full rounded-4 border border-border-default bg-bg-4 p-6">
    <!-- Wordmark: accent tick + BLUE SHIFT 13.5/700 + STUDIO 10.5/500 @0.26em -->
    <div class="mb-5 flex items-center gap-2.5">
      <div class="h-[15px] w-[2px] flex-none bg-accent"></div>
      <div class="flex items-baseline gap-1.5">
        <span class="text-[13.5px] font-bold leading-none tracking-[-0.01em] text-text-primary"
          >BLUE SHIFT</span
        >
        <span class="text-[10.5px] font-medium leading-none tracking-[0.26em] text-text-muted"
          >STUDIO</span
        >
      </div>
    </div>

    <form onsubmit={submit} novalidate>
      <label
        for="email"
        class="mb-1.5 block font-semibold text-[10.5px] uppercase tracking-[0.16em] text-text-faint"
        >Email</label
      >
      <input
        id="email"
        name="email"
        type="email"
        autocomplete="username"
        bind:value={email}
        class="mb-3 block w-full rounded-4 border border-border-strong bg-bg-2 px-2.5 py-2 text-[12px] text-text-primary outline-none transition-colors duration-hover ease-out placeholder:text-text-faint focus:border-accent-border focus:bg-accent-wash-12"
      />

      <label
        for="password"
        class="mb-1.5 block font-semibold text-[10.5px] uppercase tracking-[0.16em] text-text-faint"
        >Password</label
      >
      <input
        id="password"
        name="password"
        type="password"
        autocomplete="current-password"
        bind:value={password}
        class="mb-4 block w-full rounded-4 border border-border-strong bg-bg-2 px-2.5 py-2 text-[12px] text-text-primary outline-none transition-colors duration-hover ease-out placeholder:text-text-faint focus:border-accent-border focus:bg-accent-wash-12"
      />

      {#if error}
        <p role="alert" class="mb-3 text-[11px] leading-[1.5] text-danger">{error}</p>
      {/if}

      <button
        type="submit"
        disabled={submitting}
        class="block w-full rounded-4 bg-accent px-4 py-2 text-[10.5px] font-semibold uppercase tracking-[0.1em] text-text-on-accent transition-colors duration-hover ease-out hover:bg-accent-bright disabled:cursor-not-allowed disabled:opacity-[0.35]"
      >
        {submitting ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  </div>
</main>
