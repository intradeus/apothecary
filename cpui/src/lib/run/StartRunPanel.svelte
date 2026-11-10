<script lang="ts">
	// "Start a run" beside a work item (DESIGN 7.6): the flow, its options for
	// this run (parameters, skillsets, the merge request for a flow started on
	// one), the run in the way when the item already has one going — with a
	// way to stop it — and Start. Every work item's page mounts it; what is
	// that kind's own (a ticket's direction and source branch, a merge
	// request's state) comes in through `before` and `after`.
	import { errorMessage } from '$lib/core/errors';
	import type { Snippet } from 'svelte';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { shortId } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import type { SkillsetRef } from '$lib/domain';
	import type { OperatesOn } from '$lib/domain/flowdoc';
	import { t, th } from '$lib/i18n/index.svelte';
	import FlowSelect from '$lib/flow/FlowSelect.svelte';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StartRunOptions from './StartRunOptions.svelte';

	let {
		projectId,
		operatesOn,
		flow = $bindable(''),
		params = $bindable({}),
		skillsets = $bindable([]),
		mr = $bindable(0),
		busyRun = $bindable(''),
		busyText,
		starting,
		disabled = false,
		onstart,
		onerror,
		idPrefix,
		before,
		after
	}: {
		projectId: string;
		/** Only the flows whose runs start on this; any flow when left out. */
		operatesOn?: OperatesOn;
		/** The flow picked; '' = the workshop's default. */
		flow?: string;
		params?: Record<string, string>;
		skillsets?: SkillsetRef[];
		/** The merge request a flow started on one runs on (0 = none). */
		mr?: number;
		/** The run in the way, when a start was refused for it ('' = none). */
		busyRun?: string;
		/** What the item says of that run. */
		busyText: (id: string) => string;
		starting: boolean;
		disabled?: boolean;
		onstart: () => void;
		/** A stop that failed. */
		onerror: (message: string) => void;
		idPrefix: string;
		before?: Snippet;
		after?: Snippet;
	} = $props();

	const project = $derived(fleet.projectById.get(projectId));

	async function abortBusy() {
		const id = busyRun;
		const ok = await confirmDialog({
			title: t('run.stop.title', { id: shortId(id) }),
			body: `${th('run.stop.lead')} ${t('run.stop.kept')}`,
			action: t('run.stop.action')
		});
		if (!ok) return;
		try {
			await api.runs.abort(id);
			busyRun = '';
			void fleet.refresh();
		} catch (e) {
			onerror(errorMessage(e));
		}
	}
</script>

<aside class="start glass pane">
	<div class="pane-head"><h2>{t('page.ticket.startRun')}</h2></div>
	<div class="pane-body sb">
		{@render before?.()}
		<div class="field">
			<label for="{idPrefix}-flow">{t('page.ticket.flow')}</label>
			<FlowSelect
				id="{idPrefix}-flow"
				{projectId}
				{operatesOn}
				fallbackFlow={operatesOn ? project?.defaultFlow : undefined}
				bind:value={flow}
				fallbackLabel={t('page.ticket.defaultFlow', {
					flow: project?.defaultFlow || t('page.ticket.projectDefault')
				})}
			/>
		</div>
		{#if project}
			<StartRunOptions
				{projectId}
				flowName={flow.trim() || project.defaultFlow}
				bind:params
				bind:mr
				bind:skillsets
				askMR={operatesOn !== 'merge_request'}
				{idPrefix}
			/>
		{/if}
		{@render after?.()}
		{#if busyRun}
			<div class="busy tint-warning" role="alert">
				<span>{busyText(shortId(busyRun))}</span>
				<a class="btn btn-sm" href={to.run(busyRun)}>{t('page.ping.openRun')}</a>
				<button class="btn-sm btn-danger" onclick={abortBusy}>{t('run.stop.action')}</button>
			</div>
		{/if}
		<button class="btn-primary btn-lg go" onclick={onstart} {disabled} aria-busy={starting}>
			<TransportIcon kind="play" />
			{t('page.ticket.startRunBtn')}
		</button>
	</div>
</aside>

<style>
	.start {
		border-radius: var(--r-group);
	}
	.start .pane-head {
		border-bottom-color: var(--glass-line);
	}
	.sb {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.go {
		align-self: stretch;
		margin-top: 2px;
	}
	.busy {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
		padding: 8px 10px;
		border-width: 1px;
		border-style: solid;
		border-radius: var(--r-sm);
	}
</style>
