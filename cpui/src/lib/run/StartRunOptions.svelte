<script lang="ts">
	// What else a run may choose at creation (flow v4): the entrypoint — one
	// of the flow's run_start phases, offered only when it has several — and
	// parameter overrides for this run only, collapsed under Advanced. The
	// component resolves the chosen flow's declaration and the layer
	// underneath (the workshop's binding, or the run a rerun comes from)
	// itself, so the forms that mount it only carry a flow name.
	import { api } from '$lib/api';
	import type { Flow, Integration, Model, OpenMergeRequest, ParamDecl } from '$lib/domain';
	import { KNOWN_PARAMS, MR_OPENED } from '$lib/flowdoc';
	import ParamFields from '$lib/flow/ParamFields.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		projectId,
		flowName,
		phase = $bindable(''),
		params = $bindable({}),
		mr = $bindable(0),
		under,
		underLabel = 'workshop',
		idPrefix = 'sro'
	}: {
		projectId: string;
		/** The flow the run will use (the project default when the picker is
		 *  empty). */
		flowName: string;
		/** The entrypoint to begin at: one of the flow's run_start phases. */
		phase: string;
		/** Overrides for this run only; empty values never travel. */
		params: Record<string, string>;
		/** The merge request to create the run on (0 = none), asked for when
		 *  the entrypoint begins runs on one (mr_opened). */
		mr?: number;
		/** What applies when a field is left empty: the workshop's binding
		 *  (loaded here when absent), or the run a rerun comes from. */
		under?: Record<string, string>;
		underLabel?: 'workshop' | 'run';
		idPrefix?: string;
	} = $props();

	let flow = $state<Flow | null>(null);
	let bound = $state<Record<string, string>>({});
	let harnesses = $state<{ kind: string; label: string; id: string }[]>([]);
	let models = $state<Model[]>([]);
	let loadError = $state('');

	const entrypoints = $derived((flow?.phases ?? []).filter((p) => p.entry));
	// An entrypoint on mr_opened begins the run on a merge request someone
	// opened: the form asks which.
	const onMR = $derived((flow?.phases ?? []).find((p) => p.id === phase)?.signal === MR_OPENED);
	let openMRs = $state<OpenMergeRequest[] | null>(null);
	let mrError = $state('');
	$effect(() => {
		if (!onMR || !projectId) {
			openMRs = null;
			mr = 0;
			return;
		}
		let live = true;
		api.projects.mergeRequests(projectId).then(
			(list) => {
				if (!live) return;
				openMRs = list;
				mrError = '';
				if (!list.some((m) => m.iid === mr)) mr = list[0]?.iid ?? 0;
			},
			(e: unknown) => {
				if (!live) return;
				openMRs = [];
				mrError = e instanceof Error ? e.message : String(e);
			}
		);
		return () => {
			live = false;
		};
	});
	const known = $derived(
		KNOWN_PARAMS.map((n) => flow?.parameters.find((p) => p.name === n)).filter(
			(p): p is ParamDecl => !!p
		)
	);
	const own = $derived((flow?.parameters ?? []).filter((p) => !p.known));
	const layer = $derived(under ?? bound);
	const overridden = $derived(Object.values(params).filter((v) => v.trim() !== '').length);
	const effectiveHarness = $derived(
		params.harness ||
			layer.harness ||
			flow?.parameters.find((p) => p.name === 'harness')?.default ||
			''
	);

	$effect(() => {
		const pid = projectId;
		const name = flowName;
		const loadBinding = under === undefined;
		if (!pid || !name) {
			flow = null;
			return;
		}
		let live = true;
		Promise.all([
			api.flows.get(pid, name),
			loadBinding ? api.flows.binding(pid, name).catch(() => null) : Promise.resolve(null),
			api.projects.get(pid).catch(() => null),
			api.integrations.list().catch(() => [] as Integration[])
		]).then(
			([f, b, p, ints]) => {
				if (!live) return;
				flow = f;
				bound = b?.params ?? {};
				const ids = new Set(p?.harnessIds ?? []);
				harnesses = ints
					.filter((i) => i.port === 'harness' && ids.has(i.id))
					.map((i) => ({ kind: i.kind, label: `${i.kind} · ${i.name}`, id: i.id }));
				loadError = '';
				// The entrypoint: a still-valid choice stays, else the flow's first.
				const entries = f.phases.filter((ph) => ph.entry);
				if (!entries.some((ph) => ph.id === phase)) phase = entries[0]?.id ?? '';
				// Overrides of parameters this flow does not declare are dropped.
				const declared = new Set(f.parameters.map((d) => d.name));
				const kept = Object.fromEntries(Object.entries(params).filter(([k]) => declared.has(k)));
				if (Object.keys(kept).length !== Object.keys(params).length) params = kept;
			},
			(e: unknown) => {
				if (!live) return;
				flow = null;
				loadError = e instanceof Error ? e.message : String(e);
			}
		);
		return () => {
			live = false;
		};
	});

	// The model field suggests what the effective harness lists — best
	// effort: a harness that cannot list leaves the field free text.
	$effect(() => {
		const h = harnesses.find((x) => x.kind === effectiveHarness);
		if (!h) {
			models = [];
			return;
		}
		let live = true;
		api.integrations.models(h.id).then(
			(m) => {
				if (live) models = m;
			},
			() => {
				if (live) models = [];
			}
		);
		return () => {
			live = false;
		};
	});

	function set(name: string, value: string) {
		const next = { ...params };
		if (value.trim()) next[name] = value;
		else delete next[name];
		params = next;
	}
</script>

{#if flow}
	{#if entrypoints.length > 1}
		<div class="field">
			<label for="{idPrefix}-phase">{t('run.start.entrypoint')}</label>
			<select id="{idPrefix}-phase" class="mono" bind:value={phase}>
				{#each entrypoints as ph (ph.id)}
					<option value={ph.id}>{ph.id}{ph.description ? ` — ${ph.description}` : ''}</option>
				{/each}
			</select>
			<span class="help">{t('run.start.entrypointHelp')}</span>
		</div>
	{/if}
	{#if onMR}
		<div class="field">
			<label for="{idPrefix}-mr">{t('run.start.mr')}</label>
			{#if openMRs === null}
				<span class="help">{t('run.start.mrLoading')}</span>
			{:else if openMRs.length}
				<select id="{idPrefix}-mr" bind:value={mr}>
					{#each openMRs as m (m.iid)}
						<option value={m.iid}
							>!{m.iid} · {m.title} — {m.author}{m.draft
								? ` · ${t('run.start.mrDraft')}`
								: ''}{m.fork ? ` · ${t('run.start.mrFork')}` : ''}</option
						>
					{/each}
				</select>
				<span class="help">{t('run.start.mrHelp')}</span>
			{:else}
				<span class="help hue-amb">{mrError || t('run.start.mrNone')}</span>
			{/if}
		</div>
	{/if}
	<details class="adv">
		<summary>
			{t('run.start.advanced')}
			{#if overridden}<span class="pill sm t-brand"
					>{t('run.start.overrides', { count: overridden })}</span
				>{/if}
		</summary>
		<p class="help">
			{t(underLabel === 'run' ? 'run.start.advancedHelpRun' : 'run.start.advancedHelp')}
		</p>
		<ParamFields
			params={known}
			values={params}
			under={layer}
			{underLabel}
			{harnesses}
			{models}
			idPrefix={`${idPrefix}-p`}
			onchange={set}
		/>
		{#if own.length}
			<ParamFields
				params={own}
				values={params}
				under={layer}
				{underLabel}
				idPrefix={`${idPrefix}-p`}
				onchange={set}
			/>
		{/if}
	</details>
{:else if loadError}
	<div class="help hue-amb">{t('run.start.loadError', { error: loadError })}</div>
{/if}

<style>
	.adv {
		border: 1px solid var(--ln);
		border-radius: var(--r-sm, 8px);
		padding: 8px 12px;
	}
	.adv summary {
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: 8px;
		color: var(--mut);
		font-size: var(--fs-ui);
	}
	.adv[open] summary {
		margin-bottom: 8px;
	}
	.adv p {
		margin: 0 0 10px;
	}
</style>
