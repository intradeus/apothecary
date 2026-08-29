<script lang="ts">
  // Project wizard/editor (DESIGN2 §11): connect forge → pick repo →
  // commands → adapter picks → workspace → default flow.
  import { api } from '$lib/api';
  import type { Flow, Integration, Project, ProjectDraft, Repo } from '$lib/domain';
  
  let {
    integrations,
    flows,
    existing = null,
    onsaved
  }: {
    integrations: Integration[];
    flows: Flow[];
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
  let forgeId = $state(init?.forgeId ?? forges[0]?.id ?? '');
  let provisionerId = $state(init?.provisionerId ?? provisioners[0]?.id ?? '');
  // The project's SET of available harness integrations — no default. The
  // FLOW names the harness kind (defaults.harness / a stage override); the
  // project just needs an integration of that kind (for auth + image layers).
  // At most one per kind so kind→integration is unambiguous.
  let harnessIds = $state<string[]>(
    init?.harnessIds?.length ? [...init.harnessIds] : harnesses[0] ? [harnesses[0].id] : []
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
  let defaultBranch = $state(init?.repo.defaultBranch ?? 'main');
  let agentsMD = $state(init?.repo.agentsMdPath ?? 'AGENTS.md');

  // commands as editable key/value rows; setup stays optional but prominent.
  let commandRows = $state<{ key: string; value: string }[]>(
    init
      ? Object.entries(init.commands).map(([key, value]) => ({ key, value }))
      : [
          { key: 'setup', value: '' },
          { key: 'verify', value: '' }
        ]
  );

  // Egress firewall. Allowlist (deny-all, permit these) vs denylist (allow-all,
  // block these); the domains table binds to whichever list the mode selects.
  // Both are kept so flipping the mode never loses the other list. blocked_domains
  // is the live force-block managed from the run page — carried through untouched.
  let egressMode = $state<'allow' | 'deny'>(init?.workspace.egressMode === 'deny' ? 'deny' : 'allow');
  let allowDomains = $state<string[]>([...(init?.workspace.extraDomains ?? [])]);
  let denyDomains = $state<string[]>([...(init?.workspace.deniedDomains ?? [])]);
  const blockedDomains = init?.workspace.blockedDomains ?? [];
  let newDomain = $state('');
  let defaultsBusy = $state(false);

  const activeDomains = $derived(egressMode === 'deny' ? denyDomains : allowDomains);
  const setActive = (list: string[]) =>
    egressMode === 'deny' ? (denyDomains = list) : (allowDomains = list);
  function addDomain(host: string) {
    const h = host.trim();
    if (h && !activeDomains.includes(h)) setActive([...activeDomains, h]);
  }
  function addNewDomain() {
    for (const h of newDomain.split(/[\s,]+/)) addDomain(h); // allow comma/space paste
    newDomain = '';
  }
  const removeDomain = (host: string) => setActive(activeDomains.filter((d) => d !== host));
  async function addDefaults() {
    defaultsBusy = true;
    try {
      const domains = await api.egress.defaults();
      const merged = [...allowDomains];
      for (const d of domains) if (!merged.includes(d)) merged.push(d);
      allowDomains = merged;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      defaultsBusy = false;
    }
  }

  let codeServerExtensions = $state((init?.workspace.codeServerExtensions ?? []).join(', '));
  // code-server UI defaults (applied as VS Code user settings at editor open —
  // no rebuild). Theme names are the built-in VS Code themes; a themed
  // extension can be added to the extensions list above and named here.
  const editorThemes = [
    'Default Dark Modern',
    'Default Light Modern',
    'Default Dark+',
    'Default Light+',
    'Default High Contrast',
    'Monokai',
    'Monokai Dimmed',
    'Solarized Dark',
    'Solarized Light',
    'Quiet Light',
    'Abyss',
    'Kimbie Dark',
    'Red',
    'Tomorrow Night Blue'
  ];
  let editorTheme = $state(init?.workspace.editor?.theme ?? '');
  let editorFontSize = $state(init?.workspace.editor?.fontSize ?? 0);
  let editorTabSize = $state(init?.workspace.editor?.tabSize ?? 0);
  let editorFormatOnSave = $state(init?.workspace.editor?.formatOnSave ?? false);
  let editorWordWrap = $state(init?.workspace.editor?.wordWrap ?? false);
  let editorTrustWorkspace = $state(init?.workspace.editor?.trustWorkspace ?? false);
  // Flows are user-defined — no built-in fallback exists.
  let defaultFlow = $state(init?.defaultFlow ?? flowNames[0] ?? '');

  // Repo picker.
  let repos = $state<Repo[] | null>(null);
  let loadingRepos = $state(false);
  let repoError = $state('');
  let busy = $state(false);
  let error = $state('');

  async function loadRepos() {
    loadingRepos = true;
    repoError = '';
    repos = null;
    try {
      repos = await api.integrations.repos(forgeId);
    } catch (e) {
      repoError = e instanceof Error ? e.message : String(e);
    } finally {
      loadingRepos = false;
    }
  }

  function pickRepo(r: Repo) {
    repoURL = r.url;
    locator = r.locator;
    if (r.defaultBranch) defaultBranch = r.defaultBranch;
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
      const body: ProjectDraft = {
        name: name.trim(),
        forgeId,
        provisionerId,
        harnessIds: harnessSet(),
        repo: {
          url: repoURL.trim(),
          locator: locator.trim(),
          defaultBranch: defaultBranch.trim(),
          agentsMdPath: agentsMD.trim()
        },
        commands,
        workspace: {
          egressMode,
          extraDomains: allowDomains,
          deniedDomains: denyDomains,
          blockedDomains,
          // Not edited here, but a save replaces the whole policy: carry the
          // raw-egress escape hatch through untouched.
          allowedCidrs: existing?.workspace.allowedCidrs ?? [],
          allowedPorts: existing?.workspace.allowedPorts ?? [],
          codeServerExtensions: codeServerExtensions
            .split(/[\s,]+/)
            .map((e) => e.trim())
            .filter(Boolean),
          editor: {
            theme: editorTheme,
            fontSize: Number(editorFontSize) || 0,
            tabSize: Number(editorTabSize) || 0,
            formatOnSave: editorFormatOnSave,
            wordWrap: editorWordWrap,
            trustWorkspace: editorTrustWorkspace
          }
        },
        defaultFlow
      };
      const saved = existing ? await api.projects.update(existing.id, body) : await api.projects.create(body);
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
                <td class="mono muted small">{r.defaultBranch}</td>
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
    <p class="muted small" style="margin:0 0 8px">The workspace is the repo's own <span class="mono">.devcontainer</span> — apothecary routes all its egress through the per-project firewall gateway.</p>

    <span class="lbl">Egress firewall</span>
    <div class="fw-mode" role="radiogroup" aria-label="egress mode">
      <label class="check"><input type="radio" bind:group={egressMode} value="allow" /> Allowlist <span class="muted small">— deny all, permit only the domains below</span></label>
      <label class="check"><input type="radio" bind:group={egressMode} value="deny" /> Denylist <span class="muted small">— allow all, block only the domains below</span></label>
    </div>

    <div class="fw-head">
      <span>{egressMode === 'deny' ? 'Blocked domains' : 'Allowed domains'} <span class="muted small">({activeDomains.length})</span></span>
      {#if egressMode === 'allow'}
        <button type="button" onclick={addDefaults} disabled={defaultsBusy}>
          {defaultsBusy ? 'Loading…' : '+ Add sensible defaults'}
        </button>
      {/if}
    </div>
    <div class="fw-scroll">
      <table class="fw-table">
        <tbody>
          {#each activeDomains as d (d)}
            <tr>
              <td class="mono">{d}</td>
              <td class="fw-x"><button type="button" class="danger" onclick={() => removeDomain(d)} aria-label={`remove ${d}`}>×</button></td>
            </tr>
          {:else}
            <tr><td class="muted small" colspan="2">
              {egressMode === 'deny'
                ? 'No domains blocked.'
                : 'No extra domains — the derived allowlist (repo host + harness/forge) still applies.'}
            </td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="row" style="margin-top:6px">
      <input
        class="mono"
        style="flex:1"
        bind:value={newDomain}
        placeholder="api.example.com or *.example.com"
        aria-label="add domain"
        onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addNewDomain(); } }}
      />
      <button type="button" onclick={addNewDomain} disabled={!newDomain.trim()}>+ add</button>
    </div>
    <p class="muted small" style="margin:6px 0 4px">Adapters (forge + harness) contribute their required domains automatically. "Sensible defaults" are common registry / OS-mirror / dev-tool hosts most builds need — added as editable rows you can curate.</p>
    <label for="p-extensions">code-server extensions <span class="muted">— VS Code extension ids, pre-installed in the browser editor (union with the repo's devcontainer customizations; from Open VSX)</span></label>
    <input id="p-extensions" bind:value={codeServerExtensions} class="mono" placeholder="golang.go, ms-python.python" />

    <label for="p-theme">editor defaults <span class="muted">— code-server UI settings, applied when the browser editor opens (no rebuild)</span></label>
    <div class="editor-defaults">
      <label class="ed-field">
        <span>Color theme</span>
        <select id="p-theme" bind:value={editorTheme}>
          <option value="">code-server default</option>
          {#each editorThemes as t (t)}
            <option value={t}>{t}</option>
          {/each}
        </select>
      </label>
      <label class="ed-field">
        <span>Font size</span>
        <input type="number" min="0" max="40" bind:value={editorFontSize} placeholder="default" />
      </label>
      <label class="ed-field">
        <span>Tab size</span>
        <input type="number" min="0" max="16" bind:value={editorTabSize} placeholder="default" />
      </label>
      <label class="ed-check">
        <input type="checkbox" bind:checked={editorFormatOnSave} />
        <span>Format on save</span>
      </label>
      <label class="ed-check">
        <input type="checkbox" bind:checked={editorWordWrap} />
        <span>Word wrap</span>
      </label>
      <label class="ed-check" title="Disables VS Code's Workspace Trust prompts (@tag:workspaceTrust) so the sandbox opens as trusted with no dialogs.">
        <input type="checkbox" bind:checked={editorTrustWorkspace} />
        <span>Trust workspace</span>
      </label>
    </div>
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

  /* code-server editor defaults */
  .editor-defaults {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 10px 16px;
    margin: 4px 0 8px;
  }
  .editor-defaults .ed-field {
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 13px;
    font-weight: normal;
    margin: 0;
  }
  .editor-defaults .ed-field input,
  .editor-defaults .ed-field select {
    width: auto;
    margin: 0;
  }
  .editor-defaults .ed-field input[type='number'] {
    width: 6em;
  }
  .editor-defaults .ed-check {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: normal;
    margin: 0;
  }
  .editor-defaults .ed-check input {
    width: auto;
    margin: 0;
  }

  /* Egress firewall table */
  .lbl {
    display: block;
    font-weight: 600;
    font-size: 13px;
    margin: 12px 0 4px;
  }
  .fw-mode {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 20px;
    margin-bottom: 10px;
  }
  .fw-mode .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: normal;
    margin: 0;
  }
  .fw-mode .check input {
    width: auto;
    margin: 0;
  }
  .fw-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-weight: 600;
    font-size: 13px;
    margin-bottom: 4px;
  }
  .fw-scroll {
    max-height: 240px;
    overflow-y: auto;
    border: 1px solid var(--border, rgba(128, 128, 128, 0.3));
    border-radius: 6px;
  }
  .fw-table {
    width: 100%;
    border-collapse: collapse;
  }
  .fw-table td {
    padding: 4px 10px;
    border-bottom: 1px solid var(--border, rgba(128, 128, 128, 0.15));
  }
  .fw-table tr:last-child td {
    border-bottom: none;
  }
  .fw-x {
    width: 1%;
    text-align: right;
    white-space: nowrap;
  }
  .fw-x .danger {
    padding: 0 8px;
    line-height: 1.6;
  }
</style>
