<script lang="ts">
	import { lore } from '$lib/lore';
	// Workshop runs tab: the runs list (DESIGN 7.4), filtered to this
	// workshop. Runs start from tickets, so there is no "New run" here.
	import type { Project, Run } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/guild';
	import { age, fmtAbs, shortId, stateTab } from '$lib/format';
	import Segmented from '$lib/ui/Segmented.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Updated from '$lib/ui/Updated.svelte';

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

<div class="bar">
	<Segmented
		options={[
			{ value: 'active', label: 'Active', count: active.length },
			{ value: 'finished', label: 'Finished', count: finished.length }
		]}
		bind:value={tab}
	/>
	<input class="q" type="search" placeholder="⌕  Search title, id, branch" bind:value={q} />
	<span class="spacer"></span>
	<Updated at={fleet.updatedAt} paused={fleet.paused} />
	<a class="btn btn-sm" href={to.runs({ project: project.id, tab })}>Open in Runs →</a>
</div>

{#if shown.length}
	<section class="c pane">
		<div class="rows">
			<div class="r head rr">
				<span>Run</span><span>State</span><span title="Workspace">WS</span><span>Stage</span><span
					>Flow</span
				><span>MR</span><span class="num">Updated</span>
			</div>
			{#each shown as r (r.id)}
				<div class="r rr">
					<span class="t">
						<a class="ell ttl stretch" href={to.run(r.id)}>{r.title || 'Untitled run'}</a>
						<span class="mono fnt"
							>{shortId(r.id)}{#if r.branch}{' · '}{r.branch}{/if}</span
						>
					</span>
					<span><StatusChip state={r.state} size="sm" /></span>
					<span
						><WorkspaceStatus
							state={r.state}
							envId={r.envId}
							envStatus={r.envStatus}
							compact
						/></span
					>
					<span class="mono ell stage">{r.currentStage ?? '—'}</span>
					<span class="ell"
						><span class="mono fl">{r.flowName}</span>
						<span class="mono fnt">@{r.flowHash.slice(0, 6)}</span></span
					>
					<span>
						{#if r.mrs.length}
							<span class="mrs">
								{#each r.mrs as m (m.url)}
									<a class="mr" href={m.url} target="_blank" rel="noreferrer" title={m.repo}
										>!{m.iid} ↗</a
									>
								{/each}
							</span>
						{:else}<span class="fnt">—</span>{/if}
					</span>
					<span class="num mono fnt" title={fmtAbs(r.updatedAt)}>{age(r.updatedAt)}</span>
				</div>
			{/each}
		</div>
	</section>
{:else if q}
	<EmptyState compact title="No match" text="No {tab} run matches “{q}”." />
{:else if tab === 'active'}
	<EmptyState
		title={lore('nothingRunning')}
		text="Runs start from tickets. Pick one and start a run."
	>
		<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>+ New ticket</a>
		<a class="btn" href={to.workshop(project.id, 'tickets')}>Tickets</a>
	</EmptyState>
{:else}
	<EmptyState
		compact
		title="No finished runs yet"
		text="Completed, failed and archived runs land here."
	/>
{/if}

<style>
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.q {
		max-width: 280px;
	}
	.rr {
		grid-template-columns: minmax(0, 1fr) 140px 34px 120px minmax(0, 170px) 80px 60px;
	}
	.t {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.ttl {
		font-weight: 500;
	}
	.t .mono {
		font-size: 10.5px;
	}
	.stage {
		font-size: 11px;
	}
	.fl {
		font-size: 11px;
	}
	.mrs {
		display: inline-flex;
		gap: 4px;
	}
	.mr {
		font-family: var(--mono);
		font-size: 11px;
		color: var(--brand);
	}
	.rr {
		position: relative;
	}
	.stretch {
		color: var(--tx);
	}
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.stretch:hover {
		text-decoration: none;
	}
	.mr {
		position: relative;
		z-index: 1;
	}
	@media (max-width: 1000px) {
		.rr {
			grid-template-columns: minmax(0, 1fr) 130px 30px 60px;
		}
		.rr > :nth-child(4),
		.rr > :nth-child(5),
		.rr > :nth-child(6) {
			display: none;
		}
	}
</style>
