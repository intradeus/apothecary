<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Workshop runs tab: the runs list (DESIGN 7.4), filtered to this workshop.
	import type { Integration, Project, Run } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { shortId, stateTab } from '$lib/core/format';
	import NewRunDialog from '$lib/run/NewRunDialog.svelte';
	import NewTicketLink from '$lib/tickets/NewTicketLink.svelte';
	import RunTable from '$lib/run/RunTable.svelte';
	import FinishedFilters from '$lib/run/FinishedFilters.svelte';
	import { ANY_FINISHED, finishedParams, matchesFinished } from '$lib/run/filter';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import { I } from '$lib/design/icons';

	let {
		project,
		runs,
		integrations
	}: { project: Project; runs: Run[]; integrations: Integration[] | null } = $props();

	let tab = $state<'active' | 'finished'>('active');
	let filter = $state(ANY_FINISHED);
	let q = $state('');
	let newRun = $state(false);

	const active = $derived(runs.filter((r) => stateTab[r.state] === 'active'));
	const allFinished = $derived(runs.filter((r) => stateTab[r.state] === 'finished'));
	const finished = $derived(allFinished.filter((r) => matchesFinished(r, filter)));
	const shown = $derived(
		(tab === 'active' ? active : finished)
			.filter((r) => {
				const s = q.trim().toLowerCase();
				return (
					!s ||
					r.title.toLowerCase().includes(s) ||
					r.id.toLowerCase().includes(s) ||
					shortId(r.id).includes(s) ||
					(r.branch ?? '').toLowerCase().includes(s)
				);
			})
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
	);
</script>

<HeaderAction>
	<button class="btn-primary btn-lg" onclick={() => (newRun = true)}
		>{I.add} {t('page.runs.new')}</button
	>
</HeaderAction>

<NewRunDialog bind:open={newRun} projectId={project.id} />

<div class="filters">
	<SearchInput bind:value={q} placeholder={t('workshop.runs.search')} />
	<Segmented
		options={[
			{ value: 'active', label: t('workshop.runs.active'), count: active.length },
			{ value: 'finished', label: t('workshop.runs.finished'), count: finished.length }
		]}
		bind:value={tab}
	/>
	{#if tab === 'finished'}
		<FinishedFilters value={filter} onchange={(f) => (filter = f)} />
	{/if}
	<span class="spacer"></span>
	<Updated at={fleet.updatedAt} paused={fleet.paused} />
	<a
		class="btn btn-sm"
		href={to.runs({
			project: project.id,
			tab,
			...(tab === 'finished' ? finishedParams(filter) : {})
		})}>{t('workshop.runs.openIn')} {I.forward}</a
	>
</div>

{#if shown.length}
	<section class="pane">
		<RunTable
			runs={shown}
			cols={['title', 'state', 'ws', 'stage', 'flow', 'mr', 'updated']}
			detail
		/>
	</section>
{:else if q}
	<EmptyState
		compact
		title={t('workshop.runs.noMatchTitle')}
		text={t(tab === 'active' ? 'workshop.runs.noMatchActive' : 'workshop.runs.noMatchFinished', {
			q
		})}
	/>
{:else if tab === 'active'}
	<EmptyState title={t('workshop.runs.shelfTitle')} text={t('workshop.runs.shelfText')}>
		<NewTicketLink
			{project}
			{integrations}
			label={t('workshop.runs.newTicket')}
			cls="btn-primary"
		/>
		<a class="btn" href={to.workshop(project.id, 'tickets')}>{t('workshop.runs.tickets')}</a>
	</EmptyState>
{:else if allFinished.length}
	<EmptyState compact title={t('run.filter.noMatch')} text={t('run.filter.noMatchText')}>
		<button onclick={() => (filter = ANY_FINISHED)}>{t('run.filter.clear')}</button>
	</EmptyState>
{:else}
	<EmptyState
		compact
		title={t('workshop.runs.noneFinishedTitle')}
		text={t('workshop.runs.noneFinishedText')}
	/>
{/if}
