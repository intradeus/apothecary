<script lang="ts">
	// Workshop runs tab: the runs list (DESIGN 7.4), filtered to this
	// workshop. Runs start from tickets, so there is no "New run" here.
	import type { Project, Run } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/guild';
	import { shortId, stateTab } from '$lib/format';
	import RunTable from '$lib/run/RunTable.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import { I } from '$lib/ui/icons';

	let { project, runs }: { project: Project; runs: Run[] } = $props();

	let tab = $state<'active' | 'finished'>('active');
	let q = $state('');

	const active = $derived(runs.filter((r) => stateTab[r.state] === 'active'));
	const finished = $derived(runs.filter((r) => stateTab[r.state] === 'finished'));
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

<div class="row">
	<Segmented
		options={[
			{ value: 'active', label: 'Active', count: active.length },
			{ value: 'finished', label: 'Finished', count: finished.length }
		]}
		bind:value={tab}
	/>
	<SearchInput bind:value={q} placeholder="Search title, id, branch" />
	<span class="spacer"></span>
	<Updated at={fleet.updatedAt} paused={fleet.paused} />
	<a class="btn btn-sm" href={to.runs({ project: project.id, tab })}>Open in Runs {I.forward}</a>
</div>

{#if shown.length}
	<section class="c pane">
		<RunTable
			runs={shown}
			cols={['title', 'state', 'ws', 'stage', 'flow', 'mr', 'updated']}
			detail
		/>
	</section>
{:else if q}
	<EmptyState compact title="No match" text="No {tab} run matches “{q}”." />
{:else if tab === 'active'}
	<EmptyState
		title="All still on the shelf"
		text="Runs start from tickets. Pick one and start a run."
	>
		<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>{I.add} New ticket</a>
		<a class="btn" href={to.workshop(project.id, 'tickets')}>Tickets</a>
	</EmptyState>
{:else}
	<EmptyState
		compact
		title="No finished runs yet"
		text="Completed, failed and archived runs land here."
	/>
{/if}
