<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { onMount } from 'svelte';

  let { children } = $props();
  let pendingCount = $state(0);

  onMount(() => {
    const poll = async () => {
      try {
        pendingCount = (await api.pings.pending()).length;
      } catch {
        /* engine unreachable; badge just goes stale */
      }
    };
    poll();
    const t = setInterval(poll, 5000);
    return () => clearInterval(t);
  });

  const isActive = (path: string) =>
    path === '/' ? page.url.pathname === '/' || page.url.pathname.startsWith('/runs') : page.url.pathname.startsWith(path);
</script>

<nav class="top">
  <span class="brand">⚗ Apothecary</span>
  <a href="/" class:active={isActive('/')}>Runs</a>
  <a href="/tickets" class:active={isActive('/tickets')}>Tickets</a>
  <a href="/inbox" class:active={isActive('/inbox')}>
    Inbox{#if pendingCount > 0}<span class="badge">{pendingCount}</span>{/if}
  </a>
  <a href="/projects" class:active={isActive('/projects')}>Projects</a>
  <a href="/flows" class:active={isActive('/flows')}>Flows</a>
  <a href="/integrations" class:active={isActive('/integrations')}>Integrations</a>
  <a href="/settings" class:active={isActive('/settings')}>Settings</a>
  <a href="/channels" class:active={isActive('/channels')}>Channels</a>
</nav>

<div class="container">
  {@render children()}
</div>
