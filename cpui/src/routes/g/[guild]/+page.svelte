<!-- Guild desk (DESIGN 7.3, following the all-guilds desk of 7.2): what needs
     you, what is brewing per workshop, what just finished, and how much room
     is left. Everything reads the shared fleet poll. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { flip } from 'svelte/animate';
	import { ME, currentGuild, to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { api } from '$lib/api';
	import { plural } from '$lib/format';
	import type { Run } from '$lib/domain';
	import Segmented from '$lib/ui/Segmented.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, materialize, ms, spring } from '$lib/ui/motion';
	import NeedsYou from '$lib/desk-home/NeedsYou.svelte';
	import WorkshopCard from '$lib/desk-home/WorkshopCard.svelte';
	import Capacity from '$lib/desk-home/Capacity.svelte';
	import RunTable from '$lib/run/RunTable.svelte';

	const guild = $derived(currentGuild());

	type Filter = 'all' | 'needs' | 'running';
	let filter = $state<Filter>('all');

	let now = $state(Date.now());
	let ticketCounts = $state<Map<string, number> | null>(null);

	onMount(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		api.tickets
			.list()
			.then((ts) => {
				const m = new Map<string, number>();
				for (const tk of ts) m.set(tk.projectId, (m.get(tk.projectId) ?? 0) + 1);
				ticketCounts = m;
			})
			.catch(() => (ticketCounts = new Map()));
		return () => clearInterval(t);
	});

	const pingRuns = $derived(new Set(fleet.pending.map((p) => p.runId)));
	const needsMe = (r: Run) => r.state === 'waiting_on_human' || pingRuns.has(r.id);
	const keep = (r: Run) =>
		filter === 'all' ? true : filter === 'needs' ? needsMe(r) : !needsMe(r);

	const counts = $derived({
		all: fleet.active.length,
		needs: fleet.active.filter(needsMe).length,
		running: fleet.active.filter((r) => !needsMe(r)).length
	});

	const hour = new Date().getHours();
	const greeting =
		hour < 5
			? 'Good evening'
			: hour < 12
				? 'Good morning'
				: hour < 18
					? 'Good afternoon'
					: 'Good evening';
	const headline = $derived.by(() => {
		const n = fleet.pending.length;
		const live = fleet.active.length;
		const needs =
			n === 0 ? 'Nothing needs you' : `${plural(n, 'thing')} ${n === 1 ? 'needs' : 'need'} you`;
		const fire = `${live} ${live === 1 ? 'brew' : 'brews'} on the fire.`;
		return `${needs}, ${fire}`;
	});

	// Last finished runs per workshop (completed / failed), newest first.
	const lastFinished = $derived.by(() => {
		const m = new Map<string, Run[]>();
		const fin = fleet.finished
			.filter((r) => r.state === 'completed' || r.state === 'failed')
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
		for (const r of fin) {
			if (!r.projectId) continue;
			const arr = m.get(r.projectId) ?? [];
			if (arr.length < 2) m.set(r.projectId, [...arr, r]);
		}
		return m;
	});

	const cards = $derived(
		fleet.projects
			.map((p) => {
				const all = fleet.liveByProject.get(p.id) ?? [];
				return { project: p, all, live: all.filter(keep) };
			})
			.filter((c) => c.all.length > 0 && c.live.length > 0)
	);
	const quiet = $derived(fleet.projects.filter((p) => !fleet.liveByProject.get(p.id)?.length));

	const move = { duration: ms('--dur-spring', 450), easing: spring };

	const DAY = 24 * 3600 * 1000;
	const recent = $derived(
		fleet.finished
			.filter((r) => now - new Date(r.updatedAt).getTime() < DAY)
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
	);
	const today = $derived.by(() => {
		const c = recent.filter((r) => r.state === 'completed').length;
		const f = recent.filter((r) => r.state === 'failed').length;
		return c || f
			? `last 24h · ${c} completed${f ? ` · ${f} failed` : ''}`
			: 'nothing finished in 24h';
	});
</script>

<svelte:head><title>{guild.name} · Desk</title></svelte:head>

<div class="page desk">
	<header class="top">
		<div class="tt">
			<div class="cap">
				<span>{guild.name}</span><span class="fnt">·</span>
				<span>{plural(fleet.projects.length, 'workshop')}</span><span class="fnt">·</span>
				<span>{guild.role}</span>
			</div>
			<h1>{greeting}, {ME.name}. <span class="accent">{headline}</span></h1>
		</div>
		<Segmented
			bind:value={filter}
			label="Filter runs"
			options={[
				{ value: 'all', label: 'Everything', count: counts.all },
				{ value: 'needs', label: 'Needs me', count: counts.needs },
				{ value: 'running', label: 'Running', count: counts.running }
			]}
		/>
	</header>

	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard
			cause="Could not reach the engine"
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<Skeleton pane lines={4} />
		<div class="card-grid" style:--min="330px">
			<Skeleton pane lines={3} />
			<Skeleton pane lines={3} />
		</div>
	{:else}
		{#if fleet.pending.length}
			<NeedsYou pings={fleet.pending} />
		{/if}

		<section class="sect">
			<SectionHeader title="Workshops" href={to.runs()} linkLabel="All runs">
				{#snippet actions()}
					<span class="pill t-az"
						>{fleet.active.length ? `${fleet.active.length} live` : 'idle'}</span
					>
					<span
						class="pill t-hot"
						class:gone={!fleet.pending.length}
						use:bump={fleet.pending.length}>{fleet.pending.length} need you</span
					>
					<span class="mut tiny today">{today}</span>
				{/snippet}
			</SectionHeader>

			{#if fleet.projects.length === 0}
				<EmptyState
					title="No workshops yet"
					text="A workshop is a repository plus how to build it. Set one up to start taking tickets."
				>
					<a class="btn btn-primary" href={to.newWorkshop()}>{I.add} New workshop</a>
				</EmptyState>
			{:else}
				<div class="card-grid" style:--min="330px">
					{#each cards as c (c.project.id)}
						<div
							class="cell"
							animate:flip={move}
							transition:materialize={{ origin: 'top center', y: 6 }}
						>
							<WorkshopCard
								project={c.project}
								live={c.live}
								liveTotal={c.all.length}
								finished={lastFinished.get(c.project.id) ?? []}
								pings={fleet.pingsByProject.get(c.project.id) ?? 0}
								tickets={ticketCounts?.get(c.project.id) ?? (ticketCounts ? 0 : undefined)}
								{now}
							/>
						</div>
					{/each}
				</div>
				{#if quiet.length}
					<EmptyState
						inline
						title={cards.length || filter !== 'all'
							? `${quiet.length} quiet ${quiet.length === 1 ? 'workshop' : 'workshops'}`
							: 'All still on the shelf'}
						text="nothing running"
					>
						<span class="qn">
							{#each quiet as p, i (p.id)}{#if i}<span class="fnt"> · </span>{/if}<a
									href={to.workshop(p.id)}>{p.name}</a
								>{/each}
						</span>
						<a class="btn btn-sm" href={to.newTicket({ project: quiet[0].id })}
							>{I.add} New ticket</a
						>
					</EmptyState>
				{/if}
				{#if !cards.length && filter !== 'all' && !quiet.length}
					<EmptyState
						inline
						title={filter === 'needs' ? 'Nothing needs you' : 'Nothing brewing'}
						text="No live runs match this filter."
					/>
				{/if}
			{/if}
		</section>

		<section class="sect">
			<SectionHeader
				title="Recently finished"
				sub="last 24h"
				href={to.runs({ tab: 'finished' })}
				linkLabel="All finished"
			/>
			{#if recent.length}
				<div class="c pane">
					<RunTable
						runs={recent}
						cols={['title', 'state', 'ws', 'workshop', 'flow', 'mr', 'updated']}
					/>
				</div>
			{:else}
				<EmptyState
					compact
					title="Nothing bottled today"
					text="Runs that finish in the last 24 hours land here."
				/>
			{/if}
		</section>

		<Capacity />
	{/if}
</div>

<style>
	.desk {
		padding: 22px 22px 24px;
		gap: 18px;
	}
	.top {
		display: flex;
		align-items: flex-end;
		gap: 16px;
		flex-wrap: wrap;
	}
	.tt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.cap {
		font-size: var(--fs-label);
		color: var(--mut);
		display: flex;
		gap: 7px;
		white-space: nowrap;
		overflow: hidden;
	}
	.sect {
		display: flex;
		flex-direction: column;
		gap: 10px;
		flex: none;
	}
	.gone {
		display: none;
	}
	.today {
		white-space: nowrap;
	}
	.cell {
		min-width: 0;
		height: 100%;
	}
	.qn a {
		color: var(--mut);
	}
	@media (max-width: 760px) {
		.desk {
			padding: 16px 16px 20px;
		}
		.top {
			flex-direction: column;
			align-items: flex-start;
		}
		.tt {
			width: 100%;
		}
		h1 {
			font-size: var(--fs-mark);
		}
		h1 .accent {
			font-size: var(--fs-h1);
		}
		.today {
			display: none;
		}
	}
</style>
