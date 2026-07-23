<script lang="ts">
  import type { RunView } from '$lib/api';

  // Workspace provisioning is a SEPARATE axis from the run's lifecycle state.
  // env_id being set means the event log provisioned an env and hasn't
  // destroyed it — but NOT that the container is actually up (it may have been
  // stopped/removed out-of-band). env_status is the engine's reconciled live
  // container status; when present it's authoritative. When it's absent (not
  // yet reconciled) we fall back to the env_id guess so the badge still shows
  // something immediately after a run starts.
  let {
    state,
    envId,
    envStatus,
    compact = false,
  }: {
    state: RunView['state'];
    envId?: string;
    envStatus?: RunView['env_status'];
    compact?: boolean;
  } = $props();

  const status = $derived.by(() => {
    if (state === 'provisioning') return 'provisioning';
    if (!envId) return 'none';
    switch (envStatus) {
      case 'running':
        return 'live';
      case 'starting':
        return 'starting';
      case 'stopped':
        return 'stopped';
      case 'absent':
        // The container is gone (removed out-of-band); env_id is stale.
        return 'none';
      case 'unknown':
        // The provisioner couldn't be reached (e.g. expired coder session);
        // we genuinely can't confirm liveness — never claim "live".
        return 'unknown';
      default:
        // Not reconciled yet: assume live rather than flash "no workspace".
        return 'live';
    }
  });
</script>

{#if status === 'live'}
  <span class="chip ws-live" title={`Workspace live${envId ? ` (${envId})` : ''}`}>
    <span class="dot"></span>{#if !compact}workspace live{/if}
  </span>
{:else if status === 'provisioning' || status === 'starting'}
  <span class="chip ws-prov" title={status === 'starting' ? 'Workspace container starting…' : 'Provisioning the workspace…'}>
    <span class="dot"></span>{#if !compact}{status === 'starting' ? 'starting…' : 'provisioning…'}{/if}
  </span>
{:else if status === 'stopped'}
  <span class="chip ws-stopped" title={`Workspace stopped${envId ? ` (${envId})` : ''} — container is not running`}>
    <span class="dot"></span>{#if !compact}workspace stopped{/if}
  </span>
{:else if status === 'unknown'}
  <span class="chip ws-unknown" title={`Workspace status unknown${envId ? ` (${envId})` : ''} — provisioner unreachable (e.g. expired session)`}>
    <span class="dot"></span>{#if !compact}workspace?{/if}
  </span>
{:else}
  <span class="ws-none" title="No workspace (reclaimed, removed, or not yet provisioned)">
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
  .ws-stopped {
    color: var(--amber);
    border-color: var(--amber);
    background: color-mix(in srgb, var(--amber) 14%, transparent);
  }
  .ws-stopped .dot {
    background: var(--amber);
  }
  /* Unknown: muted pill with a HOLLOW dot — deliberately unlike "live" so a
     stale/unreachable workspace never reads as green. */
  .ws-unknown {
    color: var(--muted);
    border-color: var(--muted);
    border-style: dashed;
    background: transparent;
  }
  .ws-unknown .dot {
    background: transparent;
    border: 1.5px solid var(--muted);
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
