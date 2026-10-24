<script lang="ts">
	// Workshop setup (DESIGN 7.9), create and edit: a stepped form with a left
	// step-rail and section anchors — 1 Repository → 2 Workshop → 3 Execution
	// → 4 Workspace — and a sticky footer (Cancel / Save, plus "Run envcheck
	// after saving").
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { ExtraRepo, Flow, Integration, Project, ProjectDraft, Repo } from '$lib/domain';
	import { to } from '$lib/guild';
	import Segmented from '$lib/ui/Segmented.svelte';
	import TagInput from '$lib/ui/TagInput.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import SaveBar from '$lib/ui/SaveBar.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import KeyValueEditor, { type KVRow } from '$lib/ui/KeyValueEditor.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import { t, th } from '$lib/i18n/index.svelte';

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

	// Commands as editable key → script rows. `setup` is pinned first (its
	// name locked, never removed): it runs at workspace bootstrap and stays
	// optional (empty = skipped).
	const initCommands = Object.entries(init?.commands ?? {});
	let commandRows = $state<KVRow[]>([
		{ key: 'setup', value: init?.commands?.setup ?? '' },
		...(init
			? initCommands.filter(([k]) => k !== 'setup').map(([key, value]) => ({ key, value }))
			: [{ key: '', value: '' }])
	]);
	const dupKeys = $derived.by(() => {
		const seen = new Set<string>();
		const dup = new Set<string>();
		for (const r of commandRows) {
			const k = r.key.trim();
			if (!k) continue;
			if (seen.has(k)) dup.add(k);
			seen.add(k);
		}
		return dup;
	});
	const commandCount = $derived(
		commandRows.filter((r) => r.key.trim() && (r.key.trim() !== 'setup' || r.value.trim())).length
	);

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
			: t('form.project.notHost');

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
		if (busy) return;
		if (dupKeys.size) {
			error = t('form.project.dupNames', { names: [...dupKeys].join(', ') });
			jump('workshop');
			return;
		}
		busy = true;
		error = '';
		try {
			const body = draft();
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
			savedAt = Date.now();
			toast(
				existing
					? t('form.project.saved', { name: saved.name })
					: t('form.project.created', { name: saved.name })
			);
			onsaved(saved, checked);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	// The whole draft as the engine takes it (also what "unsaved" compares).
	function draft(): ProjectDraft {
		const commands: Record<string, string> = {};
		for (const row of commandRows) {
			const k = row.key.trim();
			if (k && row.value.trim()) commands[k] = row.value;
		}
		return {
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
	}
	// Editing: Save waits for a change (or an envcheck to run after it).
	// Creating: there is always something to create.
	const initialDraft = untrack(() => JSON.stringify(draft()));
	const changed = $derived(JSON.stringify(draft()) !== initialDraft);
	const dirty = $derived(!existing || changed || runEnvcheck);
	let savedAt = $state<number | null>(null);

	// --- step rail --------------------------------------------------------------

	type StepId = 'repository' | 'workshop' | 'execution' | 'workspace';
	const steps = $derived<{ id: StepId; n: number; label: string; done: boolean; sub: string }[]>([
		{
			id: 'repository',
			n: 1,
			label: t('form.project.step.repository'),
			done: !!forgeId && !!repoURL.trim() && !!locator.trim(),
			sub: repoURL
				? repoURL.replace(/^https?:\/\//, '').replace(/\.git$/, '')
				: t('form.project.step.repoSub')
		},
		{
			id: 'workshop',
			n: 2,
			label: t('form.project.step.workshop'),
			done: !!name.trim() && !!defaultFlow,
			sub: name.trim() || t('form.project.step.workshopSub')
		},
		{
			id: 'execution',
			n: 3,
			label: t('form.project.step.execution'),
			done: !!provisionerId && harnessIds.length > 0,
			sub: t('form.project.step.harnesses', { count: harnessIds.length })
		},
		{
			id: 'workspace',
			n: 4,
			label: t('form.project.step.workspace'),
			done: true,
			sub: egressMode === 'deny' ? t('form.project.step.denySub') : t('form.project.step.allowSub')
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
	<SideNav
		label={t('form.project.stepsLabel')}
		{active}
		items={steps.map((s) => ({ id: s.id, label: s.label, sub: s.sub, step: s.n, done: s.done }))}
		onselect={(id) => jump(id as StepId)}
	/>

	<div class="secs">
		<!-- 1 · Repository ----------------------------------------------------->
		<section id="repository" class="c pane">
			<div class="pane-head">
				<span class="tag">1</span>
				<h2>{t('form.project.repoHead')}</h2>
				<span class="mut small">{t('form.project.repoSub')}</span>
			</div>
			<div class="pane-body stack">
				<div class="grid2">
					<div class="field">
						<label for="p-forge">{t('form.project.forge')}</label>
						<div class="row nowrap">
							<select id="p-forge" bind:value={forgeId} required>
								{#each forges as f (f.id)}<option value={f.id}>{intLabel(f)}</option>{/each}
							</select>
							<button
								type="button"
								class="btn-sel"
								onclick={loadRepos}
								disabled={!forgeId || loadingRepos}
								aria-busy={loadingRepos}
								>{loadingRepos ? t('form.project.loading') : t('form.project.browse')}</button
							>
						</div>
						{#if forges.length === 0}
							<div class="err">
								{@html th('form.project.noForge', { href: to.system('integrations') })}
							</div>
						{/if}
					</div>
				</div>

				{#if repoError}<ErrorCard
						cause={t('form.project.listFail')}
						detail={repoError}
						onretry={loadRepos}
					/>{/if}
				{#if repos}
					<div class="browser card-in materialize">
						<div class="row nowrap bh">
							<SearchInput
								bind:value={repoQuery}
								width={480}
								placeholder={t('form.project.filterRepos', { count: repos.length })}
							/>
							<span class="spacer"></span>
							<button type="button" class="btn-ghost btn-sm" onclick={() => (repos = null)}
								>{t('form.project.close')}</button
							>
						</div>
						<div class="blist">
							{#each shownRepos as r (r.locator)}
								<div class="br">
									<span class="ell">{r.name}</span>
									<span class="mono fnt">{r.defaultBranch}</span>
									<button type="button" class="btn-sm btn-sel" onclick={() => pickRepo(r)}
										>{t('form.project.use')}</button
									>
									<button
										type="button"
										class="btn-sm"
										title={t('form.project.addExtraTitle')}
										onclick={() => addExtra(r)}>{I.add} {t('form.project.extra')}</button
									>
								</div>
							{:else}
								<div class="empty-line">
									{repos.length ? t('form.project.noMatch') : t('form.project.noneVisible')}
								</div>
							{/each}
						</div>
					</div>
				{/if}

				<div class="grid2">
					<div class="field wide">
						<label for="p-url">{t('form.project.repoUrl')}</label>
						<input
							id="p-url"
							class="mono"
							bind:value={repoURL}
							required
							placeholder="https://gitlab.com/group/project.git"
						/>
					</div>
					<div class="field">
						<label for="p-locator">{t('form.project.locator')}</label>
						<input id="p-locator" bind:value={locator} required class="mono" />
						<div class="help">{t('form.project.locatorHelp')}</div>
					</div>
					<div class="field">
						<label for="p-branch">{t('form.project.defaultBranch')}</label>
						<input id="p-branch" class="mono" bind:value={defaultBranch} />
						<div class="help">{t('form.project.defaultBranchHelp')}</div>
					</div>
					<div class="field">
						<label for="p-agentsmd">{t('form.project.agentsMd')}</label>
						<input id="p-agentsmd" class="mono" bind:value={agentsMD} />
						<div class="help">{t('form.project.agentsMdHelp')}</div>
					</div>
				</div>

				<div class="sub">
					<div class="row">
						<h3>{t('form.project.extraHead')}</h3>
						<span class="count">{extraRepos.length}</span>
						<span class="spacer"></span>
						<button type="button" class="btn-sm" onclick={() => addExtra()}
							>{I.add} {t('form.project.addRepo')}</button
						>
					</div>
					<div class="help">{@html th('form.project.extraHelp')}</div>
					{#if extraRepos.length}
						<table class="xt form-dense">
							<thead>
								<tr
									><th>{t('form.project.col.name')}</th><th>{t('form.project.col.url')}</th><th
										>{t('form.project.col.locator')}</th
									><th>{t('form.project.col.branch')}</th><th></th></tr
								>
							</thead>
							<tbody>
								{#each extraRepos as r, i (i)}
									<tr>
										<td
											><input
												id={`extra-${i}-name`}
												class="mono"
												bind:value={r.name}
												placeholder="api"
												aria-label={t('form.project.checkoutName')}
												required
											/></td
										>
										<td
											><input
												class="mono"
												bind:value={r.url}
												placeholder="https://gitlab.com/group/api.git"
												aria-label={t('form.project.repoUrlAria')}
												required
											/></td
										>
										<td
											><input
												class="mono"
												bind:value={r.locator}
												placeholder={t('form.project.readOnly')}
												aria-label={t('form.project.locatorAria')}
											/></td
										>
										<td
											><input
												class="mono"
												bind:value={r.defaultBranch}
												placeholder="main"
												aria-label={t('form.project.branchAria')}
											/></td
										>
										<td
											><button
												type="button"
												class="btn-icon"
												onclick={() => removeExtra(i)}
												aria-label={t('form.project.removeRepo', {
													name: r.name || t('form.project.repository')
												})}>{I.close}</button
											></td
										>
									</tr>
								{/each}
							</tbody>
						</table>
						<div class="help">{t('form.project.primaryHelp')}</div>
					{/if}
				</div>
			</div>
		</section>

		<!-- 2 · Workshop ------------------------------------------------------->
		<section id="workshop" class="c pane">
			<div class="pane-head">
				<span class="tag">2</span>
				<h2>{t('form.project.workshopHead')}</h2>
				<span class="mut small">{t('form.project.workshopSub')}</span>
			</div>
			<div class="pane-body stack">
				<div class="grid2">
					<div class="field">
						<label for="p-name">{t('form.project.workshopName')}</label>
						<input id="p-name" bind:value={name} required />
					</div>
					<div class="field">
						<label for="p-flow">{t('form.project.defaultFlow')}</label>
						<select id="p-flow" class="mono" bind:value={defaultFlow} required>
							{#if ownFlows.length}
								<optgroup label={t('form.project.ownFlows')}>
									{#each ownFlows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
								</optgroup>
							{/if}
							{#if templateFlows.length}
								<optgroup label={t('form.project.templates')}>
									{#each templateFlows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
								</optgroup>
							{/if}
						</select>
						{#if flows.length === 0}
							<div class="err">{t('form.project.noFlows')}</div>
						{:else}
							<div class="help">{t('form.project.flowHelp')}</div>
						{/if}
					</div>
				</div>

				<div class="sub">
					<div class="row">
						<h3>{t('form.project.commands')}</h3>
						<span class="count">{commandCount}</span>
					</div>
					<div class="help">{@html th('form.project.commandsHelp')}</div>
					<KeyValueEditor
						bind:rows={commandRows}
						multiline
						keyWidth={150}
						keyPlaceholder={t('form.project.cmdKey')}
						valuePlaceholder="make test"
						addLabel={t('form.project.addCommand')}
						locked={(k, i) => i === 0 && k === 'setup'}
						invalid={dupKeys}
					/>
					{#if dupKeys.size}<div class="err">
							{t('form.project.dupCommands', { names: [...dupKeys].join(', ') })}
						</div>{/if}
				</div>
			</div>
		</section>

		<!-- 3 · Execution ------------------------------------------------------->
		<section id="execution" class="c pane">
			<div class="pane-head">
				<span class="tag">3</span>
				<h2>{t('form.project.executionHead')}</h2>
				<span class="mut small">{t('form.project.executionSub')}</span>
			</div>
			<div class="pane-body stack">
				<div class="grid2">
					<div class="field">
						<label for="p-prov">{t('form.project.provisioner')}</label>
						<select id="p-prov" bind:value={provisionerId} required>
							{#each provisioners as p (p.id)}<option value={p.id}>{intLabel(p)}</option>{/each}
						</select>
						{#if provisioners.length === 0}
							<div class="err">
								{@html th('form.project.noProvisioner', { href: to.system('integrations') })}
							</div>
						{/if}
					</div>
				</div>
				<div class="sub">
					<div class="row">
						<h3>{t('form.project.harnesses')}</h3>
						<span class="count">{harnessIds.length}</span>
					</div>
					<div class="help">{t('form.project.harnessesHelp')}</div>
					<div class="hs">
						{#each harnesses as h (h.id)}
							{@const on = harnessIds.includes(h.id)}
							{@const blocked = kindTaken(h) && !on}
							<button
								type="button"
								class="rowbtn hopt"
								class:on
								disabled={blocked}
								role="checkbox"
								aria-checked={on}
								title={blocked ? t('form.project.kindTaken', { kind: h.kind }) : ''}
								onclick={() => toggleHarness(h)}
							>
								<StatusMark
									tone={on ? 'ok' : 'pending'}
									size="sm"
									title={on ? t('form.project.picked') : t('form.project.notPicked')}
								/>
								<span class="hn ell">{h.name}</span>
								<span class="pill sm t-mut">{h.kind}</span>
								{#if blocked}<span class="fnt tiny">{t('form.project.kindTakenTag')}</span>{/if}
							</button>
						{:else}
							<div class="err">
								{@html th('form.project.noHarness', { href: to.system('integrations') })}
							</div>
						{/each}
					</div>
				</div>
			</div>
		</section>

		<!-- 4 · Workspace ------------------------------------------------------->
		<section id="workspace" class="c pane">
			<div class="pane-head">
				<span class="tag">4</span>
				<h2>{t('form.project.workspaceHead')}</h2>
				<span class="mut small">{@html th('form.project.workspaceSub')}</span>
			</div>
			<div class="pane-body stack">
				<div class="sub">
					<h3>{t('form.project.egress')}</h3>
					<div class="row modes">
						<Segmented
							glass={false}
							label={t('form.project.egressMode')}
							options={[
								{ value: 'allow', label: t('form.project.allowlist') },
								{ value: 'deny', label: t('form.project.denylist') }
							]}
							bind:value={egressMode}
						/>
						<div class="mexp">
							<div class:cur={egressMode === 'allow'}>{@html th('form.project.allowExplain')}</div>
							<div class:cur={egressMode === 'deny'}>{@html th('form.project.denyExplain')}</div>
						</div>
					</div>

					<div class="row">
						<h3>
							{egressMode === 'deny'
								? t('form.project.blockedHosts')
								: t('form.project.allowedHosts')}
						</h3>
						<span class="count"
							>{egressMode === 'deny' ? denyDomains.length : allowDomains.length}</span
						>
						<span class="fnt tiny">{t('form.project.otherKept')}</span>
						<span class="spacer"></span>
						{#if egressMode === 'allow'}
							{#if defaultsAdded !== null}<span class="fnt tiny"
									>{defaultsAdded
										? t('form.project.defaultsAdded', { count: defaultsAdded })
										: t('form.project.defaultsPresent')}</span
								>{/if}
							<button
								type="button"
								class="btn-sm"
								onclick={addDefaults}
								disabled={defaultsBusy}
								aria-busy={defaultsBusy}
								>{defaultsBusy
									? t('form.project.loading')
									: `${I.add} ${t('form.project.addDefaults')}`}</button
							>
						{/if}
					</div>
					{#if egressMode === 'allow'}
						<TagInput
							bind:values={allowDomains}
							placeholder={t('form.project.allowPlaceholder')}
							validate={domainCheck}
						/>
					{:else}
						<TagInput
							bind:values={denyDomains}
							placeholder={t('form.project.denyPlaceholder')}
							validate={domainCheck}
						/>
					{/if}
					<div class="help">{t('form.project.hostsHelp')}</div>
					{#if blockedDomains.length}
						<div class="chips">
							<span class="fnt tiny"
								>{t('form.project.forceBlocked', { count: blockedDomains.length })}</span
							>
							{#each blockedDomains as d (d)}<span class="pill sm mono t-ros">{d}</span>{/each}
						</div>
					{/if}
				</div>

				<div class="sub">
					<div class="row">
						<h3>{t('form.project.extensions')}</h3>
						<span class="count">{codeServerExtensions.length}</span>
					</div>
					<TagInput bind:values={codeServerExtensions} placeholder="golang.go, ms-python.python" />
					<div class="help">{t('form.project.extensionsHelp')}</div>
				</div>

				<div class="sub">
					<h3>{t('form.project.editorDefaults')}</h3>
					<div class="help">{t('form.project.editorHelp')}</div>
					<div class="ed">
						<div class="field">
							<label for="p-theme">{t('form.project.colorTheme')}</label>
							<select id="p-theme" bind:value={editorTheme}>
								<option value="">{t('form.project.themeDefault')}</option>
								{#each editorThemes as theme (theme)}<option value={theme}>{theme}</option>{/each}
							</select>
						</div>
						<div class="field">
							<label for="p-font">{t('form.project.fontSize')}</label>
							<input
								id="p-font"
								type="number"
								min="0"
								max="40"
								bind:value={editorFontSize}
								placeholder={t('form.project.default')}
							/>
						</div>
						<div class="field">
							<label for="p-tab">{t('form.project.tabSize')}</label>
							<input
								id="p-tab"
								type="number"
								min="0"
								max="16"
								bind:value={editorTabSize}
								placeholder={t('form.project.default')}
							/>
						</div>
					</div>
					<div class="row toggles">
						<Toggle bind:checked={editorFormatOnSave} label={t('form.project.formatOnSave')} />
						<Toggle bind:checked={editorWordWrap} label={t('form.project.wordWrap')} />
						<Toggle
							bind:checked={editorTrustWorkspace}
							label={t('form.project.trustWorkspace')}
							title={t('form.project.trustTitle')}
						/>
					</div>
				</div>
			</div>
		</section>

		{#if error}<ErrorCard cause={t('form.project.saveFail')} detail={error} />{/if}

		<!-- The bar's Save is the form's submit button (SaveBar `submit`): the
		     native required-field checks run, then submit() saves. -->
		<SaveBar
			{dirty}
			{busy}
			{savedAt}
			note={existing ? t('form.project.noteEdit') : t('form.project.noteCreate')}
			saveLabel={existing ? t('form.project.saveSetup') : t('form.project.createWorkshop')}
			submit
		>
			{#snippet extra()}
				<Toggle
					bind:checked={runEnvcheck}
					label={t('form.project.envcheck')}
					title={t('form.project.envcheckTitle')}
				/>
				<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
			{/snippet}
		</SaveBar>
	</div>
</form>

<style>
	.pf {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		gap: 14px 16px;
		align-items: start;
		max-width: 1180px;
		width: 100%;
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
	.pane-head {
		flex-wrap: wrap;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.sub {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 14px;
		border-top: 1px solid var(--cLn);
	}
	.stack > .sub:first-child {
		border-top: 0;
		padding-top: 0;
	}
	.sub h3 {
		font-size: var(--fs-ui);
	}
	.sub > .row {
		gap: 8px;
	}
	.browser {
		overflow: hidden;
	}
	.bh {
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
		font-size: var(--fs-ui);
	}
	.br:first-child {
		border-top: 0;
	}
	.br:hover {
		background: var(--p1);
	}
	.br .mono {
		font-size: var(--fs-meta);
	}
	/* Extra repositories: a table of inputs; tight cells, no row rules. */
	.xt th,
	.xt td {
		padding: 3px 3px;
		border-top: 0;
	}
	.xt th {
		position: static;
	}
	.xt td:last-child {
		width: 28px;
	}
	.xt th:first-child {
		width: 130px;
	}
	.xt th:nth-child(3) {
		width: 120px;
	}
	.xt th:nth-child(4) {
		width: 110px;
	}
	.hs {
		display: flex;
		flex-direction: column;
		gap: 5px;
		margin-top: 4px;
		max-width: 520px;
	}
	.hopt {
		gap: 10px;
		border: 1px solid var(--ln);
		background: var(--p1);
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
	}
	.hopt.on {
		background: var(--grnBg);
		border-color: var(--grnLn);
		box-shadow: none;
	}
	.hopt:disabled {
		opacity: var(--o-dim);
		cursor: not-allowed;
	}
	.hn {
		flex: 1;
		font-weight: 500;
		font-size: var(--fs-ui);
	}
	.modes {
		align-items: flex-start;
		gap: 14px;
		margin-bottom: 6px;
	}
	.mexp {
		flex: 1;
		min-width: 240px;
		display: flex;
		flex-direction: column;
		gap: 3px;
		font-size: var(--fs-label);
		color: var(--fnt);
		transition: color var(--dur-fade) var(--ease-out);
	}
	.mexp > div {
		transition: color var(--dur-fade) var(--ease-out);
	}
	.mexp .cur {
		color: var(--tx);
	}
	.mexp :global(b) {
		font-weight: 600;
	}
	.ed {
		display: grid;
		grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
		gap: 12px 16px;
		max-width: 620px;
	}
	.toggles {
		gap: 18px;
		margin-top: 6px;
	}
	@media (max-width: 1000px) {
		.pf {
			grid-template-columns: minmax(0, 1fr);
		}
		.ed {
			grid-template-columns: minmax(0, 1fr);
		}
	}
	@media (max-width: 760px) {
		.xt thead {
			display: none;
		}
		.xt tr {
			display: grid;
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 28px;
		}
	}
</style>
