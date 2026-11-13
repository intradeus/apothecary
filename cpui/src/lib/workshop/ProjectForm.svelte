<script lang="ts">
	// Workshop setup (DESIGN 7.9), create and edit: a stepped form with a left
	// step-rail and section anchors — 1 Repository → 2 Workshop → 3 Execution
	// → 4 Workspace → 5 Services → 6 Browser (→ 7 Secrets, once the workshop
	// exists) — and a sticky footer (Cancel / Save, plus "Run envcheck after
	// saving"). Secrets are saved one by one as they are set, not by Save.
	import { errorMessage } from '$lib/core/errors';
	import { onMount, untrack } from 'svelte';
	import { replaceState } from '$app/navigation';
	import { api } from '$lib/api';
	import type { ExtraRepo, Flow, Integration, Project, ProjectDraft } from '$lib/domain';
	import Toggle from '$lib/ui/Toggle.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import SaveBar from '$lib/ui/SaveBar.svelte';
	import type { KVRow } from '$lib/ui/KeyValueEditor.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import {
		serviceConfigs,
		serviceIssues,
		serviceRows,
		type ServiceRow
	} from '$lib/workshop/ServicesEditor.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import RepositorySection from './form/RepositorySection.svelte';
	import WorkshopSection from './form/WorkshopSection.svelte';
	import ExecutionSection from './form/ExecutionSection.svelte';
	import WorkspaceSection from './form/WorkspaceSection.svelte';
	import ServicesSection from './form/ServicesSection.svelte';
	import BrowserSection from './form/BrowserSection.svelte';
	import SecretsSection from './form/SecretsSection.svelte';
	import type { EditorFields } from './form/EditorDefaults.svelte';

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
	const repoAccounts = byPort('repository');
	const provisioners = byPort('provisioner');
	const harnesses = byPort('harness');
	const flowNames = initFlows.map((f) => f.name);
	const ownFlows = initFlows.filter((f) => !f.builtin);
	const templateFlows = initFlows.filter((f) => f.builtin);

	let name = $state(init?.name ?? '');
	let repoAccountId = $state(init?.repoAccountId ?? repoAccounts[0]?.id ?? '');
	let provisionerId = $state(init?.provisionerId ?? provisioners[0]?.id ?? '');
	// The workshop's SET of available harness integrations — no default. The
	// FLOW names the harness kind (defaults.harness / a stage override); the
	// workshop just needs an integration of that kind (for auth + image
	// layers). At most one per kind so kind→integration is unambiguous.
	let harnessIds = $state<string[]>(
		init?.harnessIds?.length ? [...init.harnessIds] : harnesses[0] ? [harnesses[0].id] : []
	);
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
	let agentsMD = $state(init?.repo.agentsMdPath ?? '');

	// Commands as editable key → script rows; flows run them by name.
	const initCommands = Object.entries(init?.commands ?? {});
	let commandRows = $state<KVRow[]>(
		initCommands.length
			? initCommands.map(([key, value]) => ({ key, value }))
			: [{ key: '', value: '' }]
	);
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

	let codeServerExtensions = $state<string[]>([...(init?.workspace.codeServerExtensions ?? [])]);
	// code-server UI defaults (see EditorDefaults).
	let editor = $state<EditorFields>({
		theme: init?.workspace.editor?.theme ?? '',
		fontSize: init?.workspace.editor?.fontSize || '',
		tabSize: init?.workspace.editor?.tabSize || '',
		formatOnSave: init?.workspace.editor?.formatOnSave ?? false,
		wordWrap: init?.workspace.editor?.wordWrap ?? false,
		trustWorkspace: init?.workspace.editor?.trustWorkspace ?? false
	});
	// The workshop's services (its app, a database), in the order the run
	// page lists them.
	let serviceList = $state<ServiceRow[]>(serviceRows(init?.workspace.services ?? []));
	const svcIssues = $derived(serviceIssues(serviceList));
	let svcStrict = $state(false);
	let browserEnabled = $state(init?.workspace.browserEnabled ?? false);
	// Docker in workspaces: the run's own daemon, added to the build.
	let docker = $state(init?.workspace.docker ?? false);
	// Flows are user-defined — no built-in fallback exists.
	let defaultFlow = $state(init?.defaultFlow ?? flowNames[0] ?? '');

	// Additional repositories (multi-repo tasks): cloned beside the primary,
	// each published as its own merge request when a run changes it.
	let extraRepos = $state<ExtraRepo[]>(init?.repos?.map((r) => ({ ...r })) ?? []);

	let busy = $state(false);
	let error = $state('');
	let runEnvcheck = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (busy) return;
		if (dupKeys.size) {
			error = t('form.project.dupNames', { names: [...dupKeys].join(', ') });
			jump('workshop');
			return;
		}
		if (svcIssues.size) {
			svcStrict = true;
			error = t('form.services.fixFirst');
			jump('services');
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
			error = errorMessage(err);
			// The engine names the service it refused: show where it is.
			if (/^project: service/.test(error)) jump('services');
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
			repoAccountId,
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
					theme: editor.theme,
					fontSize: Number(editor.fontSize) || 0,
					tabSize: Number(editor.tabSize) || 0,
					formatOnSave: editor.formatOnSave,
					wordWrap: editor.wordWrap,
					trustWorkspace: editor.trustWorkspace
				},
				services: serviceConfigs(serviceList),
				browserEnabled,
				docker
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

	type StepId =
		'repository' | 'workshop' | 'execution' | 'workspace' | 'services' | 'browser' | 'secrets';
	const STEP_IDS: StepId[] = [
		'repository',
		'workshop',
		'execution',
		'workspace',
		'services',
		'browser',
		'secrets'
	];
	const steps = $derived<{ id: StepId; n: number; label: string; done: boolean; sub: string }[]>([
		{
			id: 'repository',
			n: 1,
			label: t('form.project.step.repository'),
			done: !!repoAccountId && !!repoURL.trim() && !!locator.trim(),
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
		},
		{
			id: 'services',
			n: 5,
			label: t('form.project.step.services'),
			done: svcIssues.size === 0,
			sub: serviceList.length
				? t('form.project.step.servicesSub', { count: serviceList.length })
				: t('form.project.step.noServices')
		},
		{
			id: 'browser',
			n: 6,
			label: t('form.project.step.browser'),
			done: true,
			sub: browserEnabled ? t('form.project.step.browserOn') : t('form.project.step.browserOff')
		},
		...(existing
			? [
					{
						id: 'secrets' as const,
						n: 7,
						label: t('form.project.step.secrets'),
						done: true,
						sub: t('form.project.step.secretsSub')
					}
				]
			: [])
	]);
	let active = $state<StepId>('repository');
	let formEl = $state<HTMLFormElement | null>(null);

	function jump(id: StepId) {
		active = id;
		formEl?.querySelector(`#${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
		replaceState(`#${id}`, {});
	}

	onMount(() => {
		const hash = location.hash.slice(1) as StepId;
		if (STEP_IDS.includes(hash)) {
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
</script>

<form class="pf" bind:this={formEl} onsubmit={submit}>
	<SideNav
		label={t('form.project.stepsLabel')}
		{active}
		items={steps.map((s) => ({ id: s.id, label: s.label, sub: s.sub, step: s.n, done: s.done }))}
		onselect={(id) => jump(id as StepId)}
	/>

	<div class="secs">
		<RepositorySection
			{repoAccounts}
			bind:repoAccountId
			bind:repoURL
			bind:locator
			bind:defaultBranch
			bind:agentsMD
			bind:name
			bind:extraRepos
		/>
		<WorkshopSection
			{ownFlows}
			{templateFlows}
			noFlows={flows.length === 0}
			{dupKeys}
			bind:name
			bind:defaultFlow
			bind:commandRows
		/>
		<ExecutionSection {provisioners} {harnesses} bind:provisionerId bind:harnessIds />
		<WorkspaceSection
			{blockedDomains}
			bind:egressMode
			bind:allowDomains
			bind:denyDomains
			bind:docker
			bind:codeServerExtensions
			bind:editor
			onerror={(message) => (error = message)}
		/>
		<ServicesSection bind:rows={serviceList} issues={svcIssues} strict={svcStrict} />
		<BrowserSection bind:enabled={browserEnabled} projectId={existing?.id} {savedAt} />
		{#if existing}
			<SecretsSection projectId={existing.id} />
		{/if}

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
		width: 100%;
	}
	.secs {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-width: 0;
	}
	@media (max-width: 1000px) {
		.pf {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
