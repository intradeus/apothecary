<script lang="ts">
  import type { RunView } from '$lib/api';

  // Provision status is a SEPARATE axis from the run's lifecycle state: a run
  // holds a live workspace while env_id is set (env_ready … env_destroyed) —
  // true even for completed/abandoned runs until the reaper or an archive
  // reclaims it. `provisioning` is the transient before env_id exists.
  let { state, envId, compact = false }: { state: RunView['state']; envId?: string; compact?: boolean } =
    $props();

  const status = $derived(state === 'provisioning' ? 'provisioning' : envId ? 'live' : 'none');
</script>

{#if status === 'live'}
  <span class="chip ws-live" title={`Workspace live${envId ? ` (${envId})` : ''}`}>
    <span class="dot"></span>{#if !compact}workspace live{/if}
  </span>
{:else if status === 'provisioning'}
  <span class="chip ws-prov" title="Provisioning the workspace…">
    <span class="dot"></span>{#if !compact}provisioning…{/if}
  </span>
{:else}
  <span class="ws-none" title="No workspace (reclaimed or not yet provisioned)">
    <span class="dot"></span>{#if !compact}<span class="muted small">no workspace</span>{/if}
  </span>
{/if}

<style>
  .ws-live {
    color: var(--green);
    border-color: var(--green);
    background: transparent;
    background: color-mix(in srgb, var(--green) 14%, transparent);
  }
  .ws-live .dot {
    background: var(--green);
  }
  .ws-prov {
    color: var(--blue);
  }
  .ws-prov .dot {
    background: var(--blue);
    animation: pulse 1.2s ease-in-out infinite;
  }
  /* Non-provisioned: a subtle STATIC grey dot (no pill, no animation). */
  .ws-none {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .ws-none .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--muted);
    opacity: 0.6;
    flex: none;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.4;
    }
  }
</style>
