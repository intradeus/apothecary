<script lang="ts">
  // Project wizard/editor (DESIGN2 §11): connect forge → pick repo →
  // commands → adapter picks → workspace → default flow.
  import { api, type FlowRow, type Integration, type Preset, type Project, type RepoInfo } from '$lib/api';
  import { onMount } from 'svelte';

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
  let harnessId = $state(init?.harness_id ?? harnesses[0]?.id ?? '');

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

  let template = $state(init?.workspace.template ?? '');
  let extraDomains = $state((init?.workspace.extra_domains ?? []).join(', '));
  let fragment = $state(init?.workspace.dockerfile_fragment ?? '');
  // Flows are user-defined — no built-in fallback exists.
  let defaultFlow = $state(init?.default_flow ?? flowNames[0] ?? '');

  // Repo picker.
  let repos = $state<RepoInfo[] | null>(null);
  let loadingRepos = $state(false);
  let repoError = $state('');
  let busy = $state(false);
  let error = $state('');

  // Toolchain presets (§8.2): inserting appends visible, editable lines +
  // egress domains — never a hidden dependency.
  let presets = $state<Preset[]>([]);
  onMount(() => {
    api.presets().then((p) => (presets = p)).catch(() => {});
  });

  function insertPreset(p: Preset) {
    const lines = p.lines.join('\n');
    fragment = fragment.trim() ? fragment.trimEnd() + '\n' + lines + '\n' : lines + '\n';
    const have = new Set(
      extraDomains.split(',').map((d) => d.trim()).filter(Boolean)
    );
    for (const d of p.domains) have.add(d);
    extraDomains = [...have].join(', ');
  }

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
        harness_id: harnessId,
        repo: {
          url: repoURL.trim(),
          locator: locator.trim(),
          default_branch: defaultBranch.trim(),
          agents_md_path: agentsMD.trim()
        },
        commands,
        workspace: {
          template: template.trim(),
          extra_domains: extraDomains
            .split(',')
            .map((d) => d.trim())
            .filter(Boolean),
          ...(fragment.trim() ? { dockerfile_fragment: fragment } : {})
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
    <label for="p-harness">Harness <span class="muted">— brings its own model auth (configured on the integration)</span></label>
    <select id="p-harness" bind:value={harnessId} required>
      {#each harnesses as h (h.id)}<option value={h.id}>{h.name} ({h.kind})</option>{/each}
    </select>
  </div>

  <div class="card">
    <h2>4 · Workspace</h2>
    <label for="p-fragment">Dockerfile fragment <span class="muted">— your toolchain layer, built on the apothecary base image
      (RUN/ENV/ARG only); the harness is installed automatically</span></label>
    <textarea id="p-fragment" bind:value={fragment} rows="5" class="mono"
      placeholder={'RUN curl -fsSL https://go.dev/dl/go1.24.5.linux-amd64.tar.gz | tar -C /usr/local -xz\nENV PATH=/usr/local/go/bin:$PATH'}></textarea>
    {#if presets.length > 0}
      <div class="row" style="margin-top:6px">
        <span class="muted small">Insert preset:</span>
        {#each presets as p (p.name)}
          <button type="button" onclick={() => insertPreset(p)}>+ {p.label}</button>
        {/each}
      </div>
    {/if}
    <label for="p-domains">Extra egress domains <span class="muted">— comma-separated; adapters contribute theirs automatically,
      presets add their registries</span></label>
    <input id="p-domains" bind:value={extraDomains} class="mono" placeholder="proxy.golang.org, registry.npmjs.org" />
    <label for="p-template">Custom provisioner template <span class="muted">— ADVANCED, usually empty: the apothecary manages
      the template and builds the image above. Set a template name only to bypass the pipeline with your own template.</span></label>
    <input id="p-template" bind:value={template} placeholder="(managed)" />
  </div>

  {#if error}<div class="error">{error}</div>{/if}
  <button class="primary" type="submit" disabled={busy}>
    {busy ? 'Saving…' : existing ? 'Save project' : 'Create project'}
  </button>
</form>
