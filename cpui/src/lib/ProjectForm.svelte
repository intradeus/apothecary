<script lang="ts">
  // Project wizard/editor (DESIGN2 §11): connect forge → pick repo →
  // commands → adapter picks → workspace → default flow.
  import { api, type FlowRow, type Integration, type Project, type RepoInfo } from '$lib/api';
  
  let {
    integrations,
    flows,
    existing = null,
    onsaved
  }: {
    integrations: Integration[];
    flows: FlowRow[];
    existing?: Project | null;
    onsaved: (p: Project) => void;
  } = $props();

  const byPort = (port: string) => integrations.filter((i) => i.port === port);
  const forges = byPort('forge');
  const provisioners = byPort('provisioner');
  const harnesses = byPort('harness');

  // Snapshot the props once: this form deliberately captures initial values
  // (it is remounted per project) — one reference keeps svelte-check quiet.
  const init = $state.snapshot(existing) as Project | null;
  const flowNames = flows.map((f) => f.name);

  let name = $state(init?.name ?? '');
  let forgeId = $state(init?.forge_id ?? forges[0]?.id ?? '');
  let provisionerId = $state(init?.provisioner_id ?? provisioners[0]?.id ?? '');
  // The project's SET of available harness integrations — no default. The
  // FLOW names the harness kind (defaults.harness / a stage override); the
  // project just needs an integration of that kind (for auth + image layers).
  // At most one per kind so kind→integration is unambiguous.
  let harnessIds = $state<string[]>(
    init?.harness_ids?.length ? [...init.harness_ids] : harnesses[0] ? [harnesses[0].id] : []
  );
  // A kind is taken if a DIFFERENT checked harness already covers it.
  const kindTaken = (h: Integration) =>
    harnessIds.some((id) => id !== h.id && harnesses.find((x) => x.id === id)?.kind === h.kind);
  // Dedup by kind (one per kind), preserving selection order.
  function harnessSet(): string[] {
    const ids: string[] = [];
    const seen = new Set<string>();
    for (const id of harnessIds) {
      const k = harnesses.find((h) => h.id === id)?.kind;
      if (!k || seen.has(k) || ids.includes(id)) continue;
      seen.add(k);
      ids.push(id);
    }
    return ids;
  }

  let repoURL = $state(init?.repo.url ?? '');
  let locator = $state(init?.repo.locator ?? '');
  let defaultBranch = $state(init?.repo.default_branch ?? 'main');
  let agentsMD = $state(init?.repo.agents_md_path ?? 'AGENTS.md');

  // commands as editable key/value rows; setup stays optional but prominent.
  let commandRows = $state<{ key: string; value: string }[]>(
    init
      ? Object.entries(init.commands).map(([key, value]) => ({ key, value }))
      : [
          { key: 'setup', value: '' },
          { key: 'verify', value: '' }
        ]
  );

  let extraDomains = $state((init?.workspace.extra_domains ?? []).join(', '));
  let codeServerExtensions = $state((init?.workspace.code_server_extensions ?? []).join(', '));
  // Flows are user-defined — no built-in fallback exists.
  let defaultFlow = $state(init?.default_flow ?? flowNames[0] ?? '');

  // Repo picker.
  let repos = $state<RepoInfo[] | null>(null);
  let loadingRepos = $state(false);
  let repoError = $state('');
  let busy = $state(false);
  let error = $state('');

  async function loadRepos() {
    loadingRepos = true;
    repoError = '';
    repos = null;
    try {
      repos = await api.repos(forgeId);
    } catch (e) {
      repoError = e instanceof Error ? e.message : String(e);
    } finally {
      loadingRepos = false;
    }
  }

  function pickRepo(r: RepoInfo) {
    repoURL = r.url;
    locator = r.locator;
    if (r.default_branch) defaultBranch = r.default_branch;
    if (!name) name = r.name.split('/').pop() ?? r.name;
    repos = null;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const commands: Record<string, string> = {};
      for (const row of commandRows) {
        if (row.key.trim() && row.value.trim()) commands[row.key.trim()] = row.value;
      }
      const body = {
        name: name.trim(),
        forge_id: forgeId,
        provisioner_id: provisionerId,
        harness_ids: harnessSet(),
        repo: {
          url: repoURL.trim(),
          locator: locator.trim(),
          default_branch: defaultBranch.trim(),
          agents_md_path: agentsMD.trim()
        },
        commands,
        workspace: {
          extra_domains: extraDomains
            .split(',')
            .map((d) => d.trim())
            .filter(Boolean),
          code_server_extensions: codeServerExtensions
            .split(/[\s,]+/)
            .map((e) => e.trim())
            .filter(Boolean)
        },
        default_flow: defaultFlow
      };
      const saved = existing ? await api.updateProject(existing.id, body) : await api.createProject(body);
      onsaved(saved);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

</script>

<form onsubmit={submit}>
  <div class="card">
    <h2>1 · Repository</h2>
    <label for="p-forge">Forge account</label>
    <select id="p-forge" bind:value={forgeId} required>
      {#each forges as f (f.id)}<option value={f.id}>{f.name} ({f.kind})</option>{/each}
    </select>
    {#if forges.length === 0}
      <div class="error">No forge integration yet — add one under <a href="/integrations">Integrations</a> first.</div>
    {/if}
    <div style="margin-top:10px">
      <button type="button" onclick={loadRepos} disabled={!forgeId || loadingRepos}>
        {loadingRepos ? 'Loading…' : 'Browse repositories'}
      </button>
      {#if repoError}<span class="error"> {repoError}</span>{/if}
    </div>
    {#if repos}
      <div style="max-height:220px; overflow-y:auto; margin-top:10px; border:1px solid var(--border); border-radius:8px">
        <table>
          <tbody>
            {#each repos as r (r.locator)}
              <tr class="click" onclick={() => pickRepo(r)}>
                <td>{r.name}</td>
                <td class="mono muted small">{r.default_branch}</td>
              </tr>
            {:else}
              <tr><td class="muted">No repositories visible to this token.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    <label for="p-url">Repo URL</label>
    <input id="p-url" bind:value={repoURL} required placeholder="https://gitlab.com/group/project.git" />
    <label for="p-locator">Locator <span class="muted">— forge-specific id (GitLab: numeric project id)</span></label>
    <input id="p-locator" bind:value={locator} required class="mono" />
    <div class="row">
      <div style="flex:1">
        <label for="p-branch">Default branch</label>
        <input id="p-branch" bind:value={defaultBranch} />
      </div>
      <div style="flex:1">
        <label for="p-agentsmd">AGENTS.md path</label>
        <input id="p-agentsmd" bind:value={agentsMD} />
      </div>
    </div>
  </div>

  <div class="card">
    <h2>2 · Project</h2>
    <label for="p-name">Project name</label>
    <input id="p-name" bind:value={name} required />
    <label for="cmd-0-v">Commands
      <span class="muted">— <code>setup</code> runs at workspace bootstrap (optional: leave empty to skip);
      other names are referenced by flows via <code>run_ref</code></span></label>
    {#each commandRows as row, idx (idx)}
      <div class="row" style="margin-bottom:6px">
        <input style="flex:0 0 140px" class="mono" bind:value={row.key} placeholder="name" aria-label="command name" />
        <input style="flex:1" class="mono" id={`cmd-${idx}-v`} bind:value={row.value} placeholder="shell command (bash -eo pipefail)" />
        <button type="button" class="danger" onclick={() => (commandRows = commandRows.filter((_, i) => i !== idx))}>×</button>
      </div>
    {/each}
    <button type="button" onclick={() => (commandRows = [...commandRows, { key: '', value: '' }])}>+ command</button>
    <label for="p-flow">Default flow</label>
    <select id="p-flow" bind:value={defaultFlow} required>
      {#each flows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
    </select>
    {#if flows.length === 0}
      <div class="error">
        No flows exist yet — flows are user-defined: <a href="/flows">create one</a> first.
      </div>
    {/if}
  </div>

  <div class="card">
    <h2>3 · Execution</h2>
    <label for="p-prov">Provisioner</label>
    <select id="p-prov" bind:value={provisionerId} required>
      {#each provisioners as p (p.id)}<option value={p.id}>{p.name} ({p.kind})</option>{/each}
    </select>
    <fieldset class="harness-extras">
      <legend>Harness integrations <span class="muted">— the harnesses this project provides (auth + tooling); a flow names which kind it runs on. One per kind.</span></legend>
      {#each harnesses as h (h.id)}
        <label class="check">
          <input
            type="checkbox"
            bind:group={harnessIds}
            value={h.id}
            disabled={kindTaken(h) && !harnessIds.includes(h.id)}
          />
          {h.name} <span class="muted">({h.kind})</span>
        </label>
      {:else}
        <span class="muted small">No harness integrations yet — add one first.</span>
      {/each}
    </fieldset>
  </div>

  <div class="card">
    <h2>4 · Workspace</h2>
    <p class="muted small" style="margin:0 0 8px">The workspace is the repo's own <span class="mono">.devcontainer</span> — apothecary injects the firewall and harness as features.</p>
    <label for="p-domains">Extra egress domains <span class="muted">— comma-separated; adapters contribute theirs automatically</span></label>
    <input id="p-domains" bind:value={extraDomains} class="mono" placeholder="proxy.golang.org, registry.npmjs.org" />
    <label for="p-extensions">code-server extensions <span class="muted">— VS Code extension ids, pre-installed in the browser editor (union with the repo's devcontainer customizations; from Open VSX)</span></label>
    <input id="p-extensions" bind:value={codeServerExtensions} class="mono" placeholder="golang.go, ms-python.python" />
  </div>

  {#if error}<div class="error">{error}</div>{/if}
  <button class="primary" type="submit" disabled={busy}>
    {busy ? 'Saving…' : existing ? 'Save project' : 'Create project'}
  </button>
</form>

<style>
  .harness-extras {
    border: 1px solid var(--border, rgba(128, 128, 128, 0.3));
    border-radius: 6px;
    padding: 6px 12px 10px;
    margin: 4px 0 8px;
  }
  .harness-extras legend {
    padding: 0 6px;
    font-size: 13px;
  }
  .harness-extras .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: normal;
    margin: 3px 0;
  }
  .harness-extras .check input {
    width: auto;
    margin: 0;
  }
</style>
