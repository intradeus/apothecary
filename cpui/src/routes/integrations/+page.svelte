<script lang="ts">
  import { api, type Integration, type KindDescriptor, type Port } from '$lib/api';
  import IntegrationForm from '$lib/IntegrationForm.svelte';
  import { onMount } from 'svelte';

  // Harness-owned auth: the harness's auth_json carries model credentials,
  // so "Model providers" is off the paved road (the provider port remains
  // usable via the API for advanced setups).
  const portOrder: Port[] = ['forge', 'provisioner', 'harness'];
  const portTitles: Partial<Record<Port, string>> = {
    forge: 'Forge (code hosting)',
    provisioner: 'Provisioner (workspaces)',
    harness: 'Harness (coding agent & model auth)'
  };

  let descriptors = $state<KindDescriptor[]>([]);
  let integrations = $state<Integration[]>([]);
  let loaded = $state(false);
  let error = $state('');
  // UI state per port/row.
  let adding = $state<Partial<Record<Port, string>>>({}); // port -> kind being added
  let editing = $state<string | null>(null); // integration id
  let testResults = $state<Record<string, { ok: boolean; error?: string } | 'running'>>({});

  const refresh = async () => {
    try {
      [descriptors, integrations] = await Promise.all([api.kinds(), api.integrations()]);
      loaded = true;
      error = '';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };
  onMount(refresh);

  const kindsFor = (port: Port) => descriptors.filter((d) => d.port === port);
  const rowsFor = (port: Port) => integrations.filter((i) => i.port === port);
  const descriptorOf = (i: Integration) =>
    descriptors.find((d) => d.port === i.port && d.kind === i.kind);

  function configSummary(i: Integration): string {
    const parts = Object.entries(i.config).map(([k, v]) => `${k}=${v}`);
    for (const k of Object.keys(i.secrets)) parts.push(`${k}=${i.secrets[k]}`);
    return parts.join(' · ');
  }

  async function test(id: string) {
    testResults = { ...testResults, [id]: 'running' };
    try {
      const r = await api.testIntegration(id);
      testResults = { ...testResults, [id]: r };
    } catch (e) {
      testResults = { ...testResults, [id]: { ok: false, error: e instanceof Error ? e.message : String(e) } };
    }
  }

  async function remove(i: Integration) {
    if (!confirm(`Delete integration "${i.name}"?`)) return;
    error = '';
    try {
      await api.deleteIntegration(i.id);
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<h1>Integrations</h1>
<p class="muted small" style="margin-top:-8px">
  Credentialed connections the factory works with. Secrets are write-only: they are stored
  encrypted and this page only ever shows a mask.
</p>

{#if error}<div class="error">{error}</div>{/if}

{#each portOrder as port (port)}
  <h2 style="margin-top:24px">{portTitles[port]}</h2>
  {#each rowsFor(port) as i (i.id)}
    <div class="card">
      <div class="row spread">
        <div>
          <strong>{i.name}</strong> <span class="chip">{i.kind}</span>
          <div class="mono small muted">{configSummary(i)}</div>
        </div>
        <div class="row">
          {#if testResults[i.id] === 'running'}
            <span class="muted small">testing…</span>
          {:else if testResults[i.id]}
            {@const r = testResults[i.id] as { ok: boolean; error?: string }}
            <span class="small" style="color:{r.ok ? 'var(--green)' : 'var(--red)'}">
              {r.ok ? '✓ ok' : `✗ ${r.error}`}
            </span>
          {/if}
          <button onclick={() => test(i.id)}>Test</button>
          <button onclick={() => (editing = editing === i.id ? null : i.id)}>
            {editing === i.id ? 'Close' : 'Edit'}
          </button>
          <button class="danger" onclick={() => remove(i)}>Delete</button>
        </div>
      </div>
      {#if editing === i.id}
        {@const d = descriptorOf(i)}
        {#if d}
          <div style="margin-top:12px; border-top:1px solid var(--border); padding-top:4px">
            <IntegrationForm
              descriptor={d}
              existing={i}
              onsaved={() => {
                editing = null;
                refresh();
              }}
              oncancel={() => (editing = null)}
            />
          </div>
        {:else}
          <div class="muted small">Kind "{i.kind}" is not registered on this server.</div>
        {/if}
      {/if}
    </div>
  {:else}
    <div class="muted small" style="margin:6px 0 10px">None configured.</div>
  {/each}

  {#if adding[port]}
    {@const d = kindsFor(port).find((k) => k.kind === adding[port])}
    {#if d}
      <div class="card">
        <h2>Add {d.label}</h2>
        <IntegrationForm
          descriptor={d}
          onsaved={() => {
            adding = { ...adding, [port]: undefined };
            refresh();
          }}
          oncancel={() => (adding = { ...adding, [port]: undefined })}
        />
      </div>
    {/if}
  {:else}
    <div class="row">
      {#each kindsFor(port) as d (d.kind)}
        <button onclick={() => (adding = { ...adding, [port]: d.kind })}>+ {d.label}</button>
      {/each}
    </div>
  {/if}
{/each}

{#if loaded && descriptors.length === 0}
  <div class="empty">The engine reports no registered kinds.</div>
{/if}
