<script lang="ts">
	// Workshop setup (DESIGN 7.9), create and edit: a stepped form with a left
	// step-rail and section anchors — 1 Repository → 2 Workshop → 3 Execution
	// → 4 Workspace — and a sticky footer (Cancel / Save, plus "Run envcheck
	// after saving").
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { ExtraRepo, Flow, Integration, Project, ProjectDraft, Repo } from '$lib/domain';
	import { to } from '$lib/guild';
	import { lore, loreLc } from '$lib/lore';
	import Segmented from '$lib/ui/Segmented.svelte';
	import TagInput from '$lib/ui/TagInput.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

	let {
		integrations,
		flows,
		existing = null,
		onsaved,
		oncancel
	}: {
		integrations: Integration[];
		flows: Flow[];
		existing?: Project | null;
		/** envcheck: a check was started after saving (the caller shows it). */
		onsaved: (p: Project, envcheck: boolean) => void;
		oncancel: () => void;
	} = $props();

	// Snapshot the props once: this form deliberately captures initial values
	// (it is remounted per workshop).
	const init = untrack(() => $state.snapshot(existing) as Project | null);
	const initFlows = untrack(() => flows);
	const byPort = (port: string) => untrack(() => integrations).filter((i) => i.port === port);
	const forges = byPort('forge');
	const provisioners = byPort('provisioner');
	const harnesses = byPort('harness');
	const flowNames = initFlows.map((f) => f.name);
	const ownFlows = initFlows.filter((f) => !f.builtin);
	const templateFlows = initFlows.filter((f) => f.builtin);

	let name = $state(init?.name ?? '');
	let forgeId = $state(init?.forgeId ?? forges[0]?.id ?? '');
	let provisionerId = $state(init?.provisionerId ?? provisioners[0]?.id ?? '');
	// The workshop's SET of available harness integrations — no default. The
	// FLOW names the harness kind (defaults.harness / a stage override); the
	// workshop just needs an integration of that kind (for auth + image
	// layers). At most one per kind so kind→integration is unambiguous.
	let harnessIds = $state<string[]>(
		init?.harnessIds?.length ? [...init.harnessIds] : harnesses[0] ? [harnesses[0].id] : []
	);
	// A kind is taken if a DIFFERENT checked harness already covers it.
	const kindTaken = (h: Integration) =>
		harnessIds.some((id) => id !== h.id && harnesses.find((x) => x.id === id)?.kind === h.kind);
	function toggleHarness(h: Integration) {
		harnessIds = harnessIds.includes(h.id)
			? harnessIds.filter((id) => id !== h.id)
			: [...harnessIds, h.id];
	}
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

	// Commands as editable key/value rows. `setup` is pinned first: it runs at
	// workspace bootstrap and stays optional (empty = skipped).
	const initCommands = Object.entries(init?.commands ?? {});
	let setupCmd = $state(init?.commands?.setup ?? '');
	let commandRows = $state<{ key: string; value: string }[]>(
		init
			? initCommands.filter(([k]) => k !== 'setup').map(([key, value]) => ({ key, value }))
			: [{ key: 'verify', value: '' }]
	);
	const dupKeys = $derived.by(() => {
		const seen = new Set<string>(['setup']);
		const dup = new Set<string>();
		for (const r of commandRows) {
			const k = r.key.trim();
			if (!k) continue;
			if (seen.has(k)) dup.add(k);
			seen.add(k);
		}
		return dup;
	});

	// Egress firewall. Allowlist (deny-all, permit these) vs denylist
	// (allow-all, block these). Both lists are kept so flipping the mode never
	// loses the other. blocked_domains is the live force-block managed from
	// the run page — carried through untouched.
	let egressMode = $state<'allow' | 'deny'>(
		init?.workspace.egressMode === 'deny' ? 'deny' : 'allow'
	);
	let allowDomains = $state<string[]>([...(init?.workspace.extraDomains ?? [])]);
	let denyDomains = $state<string[]>([...(init?.workspace.deniedDomains ?? [])]);
	const blockedDomains = init?.workspace.blockedDomains ?? [];
	let defaultsBusy = $state(false);
	let defaultsAdded = $state<number | null>(null);

	async function addDefaults() {
		defaultsBusy = true;
		defaultsAdded = null;
		try {
			const domains = await api.egress.defaults();
			const merged = [...allowDomains];
			for (const d of domains) if (!merged.includes(d)) merged.push(d);
			defaultsAdded = merged.length - allowDomains.length;
			allowDomains = merged;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			defaultsBusy = false;
		}
	}
	const domainCheck = (v: string) =>
		/^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/i.test(v)
			? null
			: 'not a host name (wildcards as *.example.com)';

	let codeServerExtensions = $state<string[]>([...(init?.workspace.codeServerExtensions ?? [])]);
	// code-server UI defaults (applied as VS Code user settings at editor open —
	// no rebuild). Theme names are the built-in VS Code themes; a themed
	// extension can be added to the extensions list and named here.
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
	let editorFontSize = $state<number | ''>(init?.workspace.editor?.fontSize || '');
	let editorTabSize = $state<number | ''>(init?.workspace.editor?.tabSize || '');
	let editorFormatOnSave = $state(init?.workspace.editor?.formatOnSave ?? false);
	let editorWordWrap = $state(init?.workspace.editor?.wordWrap ?? false);
	let editorTrustWorkspace = $state(init?.workspace.editor?.trustWorkspace ?? false);
	// Flows are user-defined — no built-in fallback exists.
	let defaultFlow = $state(init?.defaultFlow ?? flowNames[0] ?? '');

	// Additional repositories (multi-repo tasks): cloned beside the primary,
	// each published as its own merge request when a run changes it.
	let extraRepos = $state<ExtraRepo[]>(init?.repos?.map((r) => ({ ...r })) ?? []);
	function addExtra(r?: Repo) {
		extraRepos = [
			...extraRepos,
			{
				name: r ? (r.name.split('/').pop() ?? r.name).toLowerCase() : '',
				url: r?.url ?? '',
				locator: r?.locator ?? '',
				defaultBranch: r?.defaultBranch || 'main'
			}
		];
	}
	function removeExtra(i: number) {
		extraRepos = extraRepos.filter((_, j) => j !== i);
	}

	// Repo picker.
	let repos = $state<Repo[] | null>(null);
	let repoQuery = $state('');
	let loadingRepos = $state(false);
	let repoError = $state('');
	let busy = $state(false);
	let error = $state('');
	let runEnvcheck = $state(false);

	const shownRepos = $derived(
		(repos ?? []).filter((r) => {
			const q = repoQuery.trim().toLowerCase();
			return !q || r.name.toLowerCase().includes(q) || r.url.toLowerCase().includes(q);
		})
	);

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
		if (dupKeys.size) {
			error = `Command names must be unique: ${[...dupKeys].join(', ')}.`;
			jump('workshop');
			return;
		}
		busy = true;
		error = '';
		try {
			const commands: Record<string, string> = {};
			if (setupCmd.trim()) commands.setup = setupCmd;
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
				repos: extraRepos.map((r) => ({
					name: r.name.trim(),
					url: r.url.trim(),
					locator: r.locator?.trim() || undefined,
					defaultBranch: r.defaultBranch.trim() || 'main'
				})),
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
					codeServerExtensions: codeServerExtensions.map((e) => e.trim()).filter(Boolean),
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
			const saved = existing
				? await api.projects.update(existing.id, body)
				: await api.projects.create(body);
			let checked = false;
			if (runEnvcheck) {
				try {
					await api.projects.startEnvCheck(saved.id, false);
					checked = true;
				} catch {
					/* saved anyway; the Health tab offers to run it */
				}
			}
			onsaved(saved, checked);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	// --- step rail --------------------------------------------------------------

	type StepId = 'repository' | 'workshop' | 'execution' | 'workspace';
	const steps = $derived<{ id: StepId; n: number; label: string; done: boolean; sub: string }[]>([
		{
			id: 'repository',
			n: 1,
			label: 'Repository',
			done: !!forgeId && !!repoURL.trim() && !!locator.trim(),
			sub: repoURL ? repoURL.replace(/^https?:\/\//, '').replace(/\.git$/, '') : 'forge and repo'
		},
		{
			id: 'workshop',
			n: 2,
			label: lore('workshop'),
			done: !!name.trim() && !!defaultFlow,
			sub: name.trim() || 'name, commands, flow'
		},
		{
			id: 'execution',
			n: 3,
			label: 'Execution',
			done: !!provisionerId && harnessIds.length > 0,
			sub: `${harnessIds.length} harness${harnessIds.length === 1 ? '' : 'es'}`
		},
		{
			id: 'workspace',
			n: 4,
			label: 'Workspace',
			done: true,
			sub: egressMode === 'deny' ? 'denylist egress' : 'allowlist egress'
		}
	]);
	let active = $state<StepId>('repository');
	let formEl = $state<HTMLFormElement | null>(null);

	function jump(id: StepId) {
		active = id;
		formEl?.querySelector(`#${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
		history.replaceState(history.state, '', `#${id}`);
	}

	onMount(() => {
		const hash = location.hash.slice(1) as StepId;
		if (['repository', 'workshop', 'execution', 'workspace'].includes(hash)) {
			requestAnimationFrame(() => {
				formEl?.querySelector(`#${hash}`)?.scrollIntoView({ block: 'start' });
				active = hash;
			});
		}
		const io = new IntersectionObserver(
			(entries) => {
				for (const e of entries) if (e.isIntersecting) active = e.target.id as StepId;
			},
			{ rootMargin: '-15% 0px -70% 0px' }
		);
		formEl?.querySelectorAll('section[id]').forEach((s) => io.observe(s));
		return () => io.disconnect();
	});

	const intLabel = (i: Integration) => `${i.name} (${i.kind})`;
</script>

<form class="pf" bind:this={formEl} onsubmit={submit}>
	<aside class="rail">
		<nav class="steps lg" aria-label="Setup steps">
			{#each steps as s (s.id)}
				<a
					href="#{s.id}"
					class="step"
					class:on={active === s.id}
					onclick={(e) => {
						e.preventDefault();
						jump(s.id);
					}}
				>
					<span class="n" class:done={s.done}>{s.done && active !== s.id ? '✓' : s.n}</span>
					<span class="sl">
						<span class="lb">{s.label}</span>
						<span class="sb ell">{s.sub}</span>
					</span>
				</a>
			{/each}
		</nav>
	</aside>

	<div class="secs">
		<!-- 1 · Repository ----------------------------------------------------->
		<section id="repository" class="c pane">
			<div class="pane-head">
				<span class="num-b">1</span>
				<h2>Repository</h2>
				<span class="mut small">where the code lives, and the forge that hosts it</span>
			</div>
			<div class="pane-body col">
				<div class="grid2">
					<div class="field">
						<label for="p-forge">Forge account</label>
						<div class="row nowrap">
							<select id="p-forge" bind:value={forgeId} required>
								{#each forges as f (f.id)}<option value={f.id}>{intLabel(f)}</option>{/each}
							</select>
							<button
								type="button"
								class="btn-sel"
								onclick={loadRepos}
								disabled={!forgeId || loadingRepos}
								>{loadingRepos ? 'Loading…' : 'Browse repositories'}</button
							>
						</div>
						{#if forges.length === 0}
							<div class="err">
								No forge integration yet — add one under <a
									class="link"
									href={to.system('integrations')}>Integrations</a
								> first.
							</div>
						{/if}
					</div>
				</div>

				{#if repoError}<ErrorCard
						cause="Couldn’t list repositories"
						detail={repoError}
						onretry={loadRepos}
					/>{/if}
				{#if repos}
					<div class="browser card-in">
						<div class="bh">
							<input
								type="search"
								placeholder="⌕  Filter {repos.length} repositories"
								bind:value={repoQuery}
								aria-label="Filter repositories"
							/>
							<button type="button" class="btn-ghost btn-sm" onclick={() => (repos = null)}
								>Close</button
							>
						</div>
						<div class="blist">
							{#each shownRepos as r (r.locator)}
								<div class="br">
									<span class="ell">{r.name}</span>
									<span class="mono fnt">{r.defaultBranch}</span>
									<button type="button" class="btn-sm btn-sel" onclick={() => pickRepo(r)}
										>Use</button
									>
									<button
										type="button"
										class="btn-sm"
										title="Add as an additional repository of this {loreLc('workshop')}"
										onclick={() => addExtra(r)}>+ extra</button
									>
								</div>
							{:else}
								<div class="fnt small pad">
									{repos.length
										? 'No repository matches.'
										: 'No repositories visible to this token.'}
								</div>
							{/each}
						</div>
					</div>
				{/if}

				<div class="grid2">
					<div class="field wide">
						<label for="p-url">Repo URL</label>
						<input
							id="p-url"
							class="mono"
							bind:value={repoURL}
							required
							placeholder="https://gitlab.com/group/project.git"
						/>
					</div>
					<div class="field">
						<label for="p-locator">Locator</label>
						<input id="p-locator" bind:value={locator} required class="mono" />
						<div class="help">The forge’s own id for it (GitLab: the numeric project id).</div>
					</div>
					<div class="field">
						<label for="p-branch">Default branch</label>
						<input id="p-branch" class="mono" bind:value={defaultBranch} />
						<div class="help">What tickets fork off unless they pick another.</div>
					</div>
					<div class="field">
						<label for="p-agentsmd">AGENTS.md path</label>
						<input id="p-agentsmd" class="mono" bind:value={agentsMD} />
						<div class="help">The guide every agent reads first.</div>
					</div>
				</div>

				<div class="sub">
					<div class="subh">
						<span class="lbl">Additional repositories</span>
						<span class="count">{extraRepos.length}</span>
						<span class="spacer"></span>
						<button type="button" class="btn-sm" onclick={() => addExtra()}>+ Add repository</button
						>
					</div>
					<div class="help">
						For multi-repo tasks: cloned to <code>~/repos/&lt;name&gt;</code>, branched like the
						primary, one merge request each when a run changes it. Leave the locator empty for a
						repository the agent should only read.
					</div>
					{#if extraRepos.length}
						<div class="xt">
							<div class="xr head">
								<span>Name</span><span>URL</span><span>Locator</span><span>Branch</span><span
								></span>
							</div>
							{#each extraRepos as r, i (i)}
								<div class="xr">
									<input
										id={`extra-${i}-name`}
										class="mono"
										bind:value={r.name}
										placeholder="api"
										aria-label="checkout name"
										required
									/>
									<input
										class="mono"
										bind:value={r.url}
										placeholder="https://gitlab.com/group/api.git"
										aria-label="repo url"
										required
									/>
									<input
										class="mono"
										bind:value={r.locator}
										placeholder="read-only"
										aria-label="locator (empty = read-only)"
									/>
									<input
										class="mono"
										bind:value={r.defaultBranch}
										placeholder="main"
										aria-label="default branch"
									/>
									<button
										type="button"
										class="btn-icon"
										onclick={() => removeExtra(i)}
										aria-label="Remove {r.name || 'repository'}">×</button
									>
								</div>
							{/each}
						</div>
						<div class="help">
							The primary repository supplies the devcontainer; its image must carry the toolchains
							the extra repositories need.
						</div>
					{/if}
				</div>
			</div>
		</section>

		<!-- 2 · Workshop ------------------------------------------------------->
		<section id="workshop" class="c pane">
			<div class="pane-head">
				<span class="num-b">2</span>
				<h2>{lore('workshop')}</h2>
				<span class="mut small">its name, the commands its flows call, and where runs start</span>
			</div>
			<div class="pane-body col">
				<div class="grid2">
					<div class="field">
						<label for="p-name">{lore('workshop')} name</label>
						<input id="p-name" bind:value={name} required />
					</div>
					<div class="field">
						<label for="p-flow">Default flow</label>
						<select id="p-flow" class="mono" bind:value={defaultFlow} required>
							{#if ownFlows.length}
								<optgroup label="This {lore('workshop').toLowerCase()}’s flows">
									{#each ownFlows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
								</optgroup>
							{/if}
							{#if templateFlows.length}
								<optgroup label="Built-in templates">
									{#each templateFlows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
								</optgroup>
							{/if}
						</select>
						{#if flows.length === 0}
							<div class="err">No flows exist yet — the engine ships no templates.</div>
						{:else}
							<div class="help">Runs start on it unless they pick another.</div>
						{/if}
					</div>
				</div>

				<div class="sub">
					<div class="subh">
						<span class="lbl">Commands</span>
						<span class="count"
							>{commandRows.filter((r) => r.key.trim()).length + (setupCmd.trim() ? 1 : 0)}</span
						>
						<span class="spacer"></span>
						<button
							type="button"
							class="btn-sm"
							onclick={() => (commandRows = [...commandRows, { key: '', value: '' }])}
							>+ Add command</button
						>
					</div>
					<div class="help">
						Shell, run as <code>bash -eo pipefail</code>. Flows call them by name with
						<code>run_ref</code>.
					</div>
					<div class="ct">
						<div class="cr pinned">
							<span class="key mono">setup <span class="pill t-grn">bootstrap</span></span>
							<input
								class="mono"
								id="cmd-setup"
								bind:value={setupCmd}
								placeholder="runs when a workspace boots — leave empty to skip"
								aria-label="setup command"
							/>
							<span></span>
						</div>
						{#each commandRows as row, idx (idx)}
							<div class="cr">
								<input
									class="mono"
									class:invalid={dupKeys.has(row.key.trim())}
									bind:value={row.key}
									placeholder="name"
									aria-label="command name"
								/>
								<input
									class="mono"
									id={`cmd-${idx}-v`}
									bind:value={row.value}
									placeholder="make test"
									aria-label="command script"
								/>
								<button
									type="button"
									class="btn-icon"
									aria-label="Remove command {row.key}"
									onclick={() => (commandRows = commandRows.filter((_, i) => i !== idx))}>×</button
								>
							</div>
						{/each}
					</div>
					{#if dupKeys.size}<div class="err">Duplicate names: {[...dupKeys].join(', ')}</div>{/if}
				</div>
			</div>
		</section>

		<!-- 3 · Execution ------------------------------------------------------->
		<section id="execution" class="c pane">
			<div class="pane-head">
				<span class="num-b">3</span>
				<h2>Execution</h2>
				<span class="mut small">where workspaces run, and which coding agents may run in them</span>
			</div>
			<div class="pane-body col">
				<div class="grid2">
					<div class="field">
						<label for="p-prov">Provisioner</label>
						<select id="p-prov" bind:value={provisionerId} required>
							{#each provisioners as p (p.id)}<option value={p.id}>{intLabel(p)}</option>{/each}
						</select>
						{#if provisioners.length === 0}
							<div class="err">
								No provisioner yet — add one under <a class="link" href={to.system('integrations')}
									>Integrations</a
								>.
							</div>
						{/if}
					</div>
				</div>
				<div class="sub">
					<div class="subh">
						<span class="lbl">Harness integrations</span><span class="count"
							>{harnessIds.length}</span
						>
					</div>
					<div class="help">
						The harnesses this {lore('workshop').toLowerCase()} provides (auth and tooling). A flow names
						which kind it runs on; one integration per kind.
					</div>
					<div class="hs">
						{#each harnesses as h (h.id)}
							{@const on = harnessIds.includes(h.id)}
							{@const blocked = kindTaken(h) && !on}
							<button
								type="button"
								class="hopt"
								class:on
								disabled={blocked}
								role="checkbox"
								aria-checked={on}
								title={blocked ? `Another ${h.kind} integration is already picked` : ''}
								onclick={() => toggleHarness(h)}
							>
								<span class="ck">{on ? '✓' : ''}</span>
								<span class="hn ell">{h.name}</span>
								<span class="pill t-mut">{h.kind}</span>
								{#if blocked}<span class="fnt tiny">kind taken</span>{/if}
							</button>
						{:else}
							<div class="err">
								No harness integrations yet — add one under <a
									class="link"
									href={to.system('integrations')}>Integrations</a
								>.
							</div>
						{/each}
					</div>
				</div>
			</div>
		</section>

		<!-- 4 · Workspace ------------------------------------------------------->
		<section id="workspace" class="c pane">
			<div class="pane-head">
				<span class="num-b">4</span>
				<h2>Workspace</h2>
				<span class="mut small"
					>the repo’s own <code>.devcontainer</code>, behind this {loreLc('workshop')}’s egress
					gateway</span
				>
			</div>
			<div class="pane-body col">
				<div class="sub">
					<div class="subh"><span class="lbl">Egress firewall</span></div>
					<div class="modes">
						<Segmented
							glass={false}
							options={[
								{ value: 'allow', label: 'Allowlist' },
								{ value: 'deny', label: 'Denylist' }
							]}
							bind:value={egressMode}
						/>
						<div class="mexp">
							<div class:cur={egressMode === 'allow'}>
								<b>Allowlist</b> — deny everything, permit only the hosts below plus what the forge and
								harness need.
							</div>
							<div class:cur={egressMode === 'deny'}>
								<b>Denylist</b> — allow everything, block only the hosts below.
							</div>
						</div>
					</div>

					<div class="subh">
						<span class="lbl">{egressMode === 'deny' ? 'Blocked hosts' : 'Allowed hosts'}</span>
						<span class="count"
							>{egressMode === 'deny' ? denyDomains.length : allowDomains.length}</span
						>
						<span class="fnt tiny">the other list is kept for when you switch back</span>
						<span class="spacer"></span>
						{#if egressMode === 'allow'}
							{#if defaultsAdded !== null}<span class="fnt tiny"
									>{defaultsAdded ? `+${defaultsAdded} added` : 'all already present'}</span
								>{/if}
							<button type="button" class="btn-sm" onclick={addDefaults} disabled={defaultsBusy}
								>{defaultsBusy ? 'Loading…' : '+ Add sensible defaults'}</button
							>
						{/if}
					</div>
					{#if egressMode === 'allow'}
						<TagInput
							bind:values={allowDomains}
							placeholder="api.example.com, *.example.com"
							validate={domainCheck}
						/>
					{:else}
						<TagInput
							bind:values={denyDomains}
							placeholder="tracker.example.com, *.ads.net"
							validate={domainCheck}
						/>
					{/if}
					<div class="help">
						Paste a list — whitespace and commas split it. Sensible defaults are common registry,
						OS-mirror and dev-tool hosts most builds need. Force-blocked hosts are managed from a
						run’s Egress window.
					</div>
					{#if blockedDomains.length}
						<div class="forced">
							<span class="fnt tiny">Force-blocked ({blockedDomains.length}):</span>
							{#each blockedDomains as d (d)}<span class="pill t-ros mono">{d}</span>{/each}
						</div>
					{/if}
				</div>

				<div class="sub">
					<div class="subh">
						<span class="lbl">code-server extensions</span><span class="count"
							>{codeServerExtensions.length}</span
						>
					</div>
					<TagInput bind:values={codeServerExtensions} placeholder="golang.go, ms-python.python" />
					<div class="help">
						VS Code extension ids from Open VSX, baked into the browser editor's image (together
						with the repo's devcontainer customizations).
					</div>
				</div>

				<div class="sub">
					<div class="subh"><span class="lbl">Editor defaults</span></div>
					<div class="help">
						code-server settings, applied each time the browser editor opens — no rebuild.
					</div>
					<div class="ed">
						<div class="field">
							<label for="p-theme">Color theme</label>
							<select id="p-theme" bind:value={editorTheme}>
								<option value="">code-server default</option>
								{#each editorThemes as t (t)}<option value={t}>{t}</option>{/each}
							</select>
						</div>
						<div class="field">
							<label for="p-font">Font size</label>
							<input
								id="p-font"
								type="number"
								min="0"
								max="40"
								bind:value={editorFontSize}
								placeholder="default"
							/>
						</div>
						<div class="field">
							<label for="p-tab">Tab size</label>
							<input
								id="p-tab"
								type="number"
								min="0"
								max="16"
								bind:value={editorTabSize}
								placeholder="default"
							/>
						</div>
					</div>
					<div class="toggles">
						<Toggle bind:checked={editorFormatOnSave} label="Format on save" />
						<Toggle bind:checked={editorWordWrap} label="Word wrap" />
						<Toggle
							bind:checked={editorTrustWorkspace}
							label="Trust workspace"
							title="Disables VS Code's Workspace Trust prompts so the sandbox opens as trusted, with no dialogs"
						/>
					</div>
				</div>
			</div>
		</section>
	</div>

	<footer class="foot lg">
		{#if error}<span class="ferr err ell" title={error}>✕ {error}</span>{:else}<span
				class="fnt small"
				>{existing
					? 'Nothing changes until you save.'
					: `Creates the ${lore('workshop').toLowerCase()}; nothing runs until you start a run.`}</span
			>{/if}
		<span class="spacer"></span>
		<Toggle
			bind:checked={runEnvcheck}
			label="Run envcheck after saving"
			title="Provision a throwaway workspace right after saving to prove it builds"
		/>
		<button type="button" class="btn-ghost" onclick={oncancel}>Cancel</button>
		<button class="btn-primary btn-lg" type="submit" disabled={busy}>
			{busy ? 'Saving…' : existing ? 'Save setup' : `Create ${lore('workshop').toLowerCase()}`}
		</button>
	</footer>
</form>

<style>
	.pf {
		display: grid;
		grid-template-columns: 210px minmax(0, 1fr);
		gap: 14px 16px;
		align-items: start;
		max-width: 1180px;
		width: 100%;
	}
	.rail {
		position: sticky;
		top: 0;
	}
	.steps {
		border-radius: 16px;
		padding: 8px;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.step {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 7px 8px;
		border-radius: 10px;
		color: var(--tx);
	}
	.step:hover {
		background: var(--p3);
		text-decoration: none;
	}
	.step.on {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
	}
	.n {
		flex: none;
		width: 22px;
		height: 22px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--mono);
		font-size: 11px;
		font-weight: 600;
		border: 1px solid var(--ln2);
		color: var(--mut);
	}
	.n.done {
		background: var(--grnBg);
		border-color: var(--grnLn);
		color: var(--grn);
	}
	.on .n {
		background: var(--brand);
		color: var(--onBrand);
		border-color: transparent;
	}
	.sl {
		display: flex;
		flex-direction: column;
		min-width: 0;
		line-height: 1.3;
	}
	.lb {
		font-size: 12.5px;
		font-weight: 600;
	}
	.sb {
		font-size: 11px;
		color: var(--mut);
	}
	.secs {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-width: 0;
	}
	section {
		scroll-margin-top: 8px;
	}
	.num-b {
		flex: none;
		width: 22px;
		height: 22px;
		border-radius: 7px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--mono);
		font-size: 11px;
		font-weight: 600;
		background: var(--p2);
		border: 1px solid var(--ln);
		color: var(--mut);
	}
	.pane-head {
		flex-wrap: wrap;
	}
	.col {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.wide {
		grid-column: 1 / -1;
	}
	.sub {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 14px;
		border-top: 1px solid var(--cLn);
	}
	.col > .sub:first-child {
		border-top: 0;
		padding-top: 0;
	}
	.subh {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.lbl {
		font-size: 12.5px;
		font-weight: 600;
	}
	.browser {
		overflow: hidden;
	}
	.bh {
		display: flex;
		gap: 8px;
		padding: 8px;
		border-bottom: 1px solid var(--ln);
	}
	.blist {
		max-height: 250px;
		overflow: auto;
	}
	.br {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto auto auto;
		gap: 10px;
		align-items: center;
		padding: 6px 10px;
		border-top: 1px solid var(--ln);
		font-size: 12.5px;
	}
	.br:first-child {
		border-top: 0;
	}
	.br:hover {
		background: var(--p1);
	}
	.br .mono {
		font-size: 11px;
	}
	.pad {
		padding: 10px;
	}
	.xt,
	.ct {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 4px;
	}
	.xr {
		display: grid;
		grid-template-columns: 120px minmax(0, 1fr) 120px 110px 24px;
		gap: 6px;
		align-items: center;
	}
	.xr.head {
		font-size: 11px;
		color: var(--mut);
		font-weight: 500;
	}
	.cr {
		display: grid;
		grid-template-columns: 150px minmax(0, 1fr) 24px;
		gap: 6px;
		align-items: center;
	}
	.key {
		display: flex;
		align-items: center;
		gap: 6px;
		padding-left: 10px;
		font-weight: 600;
		font-size: 12px;
	}
	.key .pill {
		font-family: var(--sans);
		font-size: 9.5px;
		padding: 0 6px;
	}
	.cr.pinned {
		padding-bottom: 6px;
		border-bottom: 1px dashed var(--ln2);
	}
	.hs {
		display: flex;
		flex-direction: column;
		gap: 5px;
		margin-top: 4px;
	}
	.hopt {
		justify-content: flex-start;
		gap: 10px;
		padding: 7px 10px;
		border-radius: 10px;
		background: var(--p1);
		border: 1px solid var(--ln);
		box-shadow: none;
		max-width: 520px;
	}
	.hopt.on {
		background: var(--grnBg);
		border-color: var(--grnLn);
	}
	.hopt:disabled {
		opacity: 0.55;
		pointer-events: auto;
		cursor: not-allowed;
	}
	.ck {
		flex: none;
		width: 16px;
		height: 16px;
		border-radius: 5px;
		border: 1px solid var(--ln2);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: 700;
		color: var(--onBrand);
	}
	.on .ck {
		background: var(--grn);
		border-color: transparent;
	}
	.hn {
		font-weight: 500;
		font-size: 12.5px;
	}
	.modes {
		display: flex;
		align-items: flex-start;
		gap: 14px;
		flex-wrap: wrap;
		margin-bottom: 6px;
	}
	.mexp {
		flex: 1;
		min-width: 240px;
		display: flex;
		flex-direction: column;
		gap: 3px;
		font-size: 12px;
		color: var(--fnt);
	}
	.mexp .cur {
		color: var(--tx);
	}
	.mexp b {
		font-weight: 600;
	}
	.forced {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		align-items: center;
	}
	.forced .pill {
		font-size: 10.5px;
	}
	.ed {
		display: grid;
		grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
		gap: 12px 16px;
		max-width: 620px;
	}
	.toggles {
		display: flex;
		gap: 18px;
		flex-wrap: wrap;
		margin-top: 6px;
	}
	.foot {
		grid-column: 1 / -1;
		position: sticky;
		bottom: 0;
		z-index: 5;
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 8px 8px 8px 16px;
		border-radius: 16px;
	}
	.ferr {
		max-width: 50%;
		font-size: 12px;
	}
	@media (max-width: 900px) {
		.pf {
			grid-template-columns: minmax(0, 1fr);
		}
		.rail {
			position: static;
		}
		.steps {
			flex-direction: row;
			overflow-x: auto;
		}
		.xr {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 24px;
		}
		.ed {
			grid-template-columns: minmax(0, 1fr);
		}
		.foot {
			flex-wrap: wrap;
		}
	}
	/* app.css's input reset outranks its own input.mono; restate it. */
	input.mono,
	select.mono {
		font-family: var(--mono) !important;
		font-size: 12px !important;
	}
</style>
