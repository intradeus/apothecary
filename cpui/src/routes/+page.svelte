<!-- The desk (DESIGN 7.3): what needs
     you, what is brewing per workshop, what just finished, and how much room
     is left. Everything reads the shared fleet poll. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { flip } from 'svelte/animate';
	import { to } from '$lib/urls';
	import { fleet } from '$lib/fleet.svelte';
	import { session } from '$lib/session.svelte';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';
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

	type Filter = 'all' | 'needs' | 'running';
	let filter = $state<Filter>('all');

	let now = $state(Date.now());
	let ticketCounts = $state<Map<string, number> | null>(null);

	onMount(() => {
		const tick = setInterval(() => (now = Date.now()), 1000);
		api.tickets
			.list()
			.then((ts) => {
				const m = new Map<string, number>();
				for (const tk of ts) m.set(tk.projectId, (m.get(tk.projectId) ?? 0) + 1);
				ticketCounts = m;
			})
			.catch(() => (ticketCounts = new Map()));
		return () => clearInterval(tick);
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
	const greeting = $derived(
		hour < 5
			? t('page.desk.goodEvening')
			: hour < 12
				? t('page.desk.goodMorning')
				: hour < 18
					? t('page.desk.goodAfternoon')
					: t('page.desk.goodEvening')
	);
	const headline = $derived.by(() => {
		const n = fleet.pending.length;
		const live = fleet.active.length;
		const needs = n === 0 ? t('page.desk.nothingNeeds') : t('page.desk.thingsNeed', { count: n });
		const fire = t('page.desk.brewsOnFire', { count: live });
		return t('page.desk.headline', { needs, fire });
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
			? t('page.desk.last24h', { completed: c }) +
					(f ? t('page.desk.last24hFailed', { failed: f }) : '')
			: t('page.desk.nothingFinished24h');
	});
</script>

<svelte:head><title>{t('page.desk.docTitle')}</title></svelte:head>

<header class="top c">
	<div class="tt">
		<div class="cap">
			<span>{t('nav.desk')}</span><span class="fnt">·</span>
			<span>{t('page.desk.workshopCount', { count: fleet.projects.length })}</span>
		</div>
		<h1>
			{greeting}, {session.user?.name ?? ''}. <span class="accent">{headline}</span>
		</h1>
	</div>
	<a class="btn btn-primary btn-lg" href={to.newTicket()}>{I.add} {t('page.desk.newTicket')}</a>
</header>

<div class="page c">
	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard
			cause={t('page.desk.engineDown')}
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
		<div class="sections desk">
			<div class="stack">
				{#if fleet.pending.length}
					<NeedsYou pings={fleet.pending} />
				{/if}

				<section class="sect">
					<SectionHeader
						title={t('page.desk.workshops')}
						href={to.runs()}
						linkLabel={t('page.desk.allRuns')}
					>
						{#snippet actions()}
							<span class="pill t-emb"
								>{fleet.active.length
									? t('page.desk.nLive', { count: fleet.active.length })
									: t('page.desk.idle')}</span
							>
							<span
								class="pill t-hot"
								class:gone={!fleet.pending.length}
								use:bump={fleet.pending.length}
								>{t('page.desk.nNeedYou', { count: fleet.pending.length })}</span
							>
							<span class="mut tiny today">{today}</span>
						{/snippet}
					</SectionHeader>

					<div class="filters">
						<Segmented
							bind:value={filter}
							label={t('page.desk.filterRuns')}
							options={[
								{ value: 'all', label: t('page.desk.filterAll'), count: counts.all },
								{ value: 'needs', label: t('page.desk.filterNeeds'), count: counts.needs },
								{ value: 'running', label: t('page.desk.filterRunning'), count: counts.running }
							]}
						/>
					</div>

					{#if fleet.projects.length === 0}
						<EmptyState title={t('page.desk.noWorkshops')} text={t('page.desk.noWorkshopsText')}>
							<a class="btn btn-primary" href={to.newWorkshop()}
								>{I.add} {t('page.desk.newWorkshop')}</a
							>
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
									? t('page.desk.quietWorkshops', { count: quiet.length })
									: t('page.desk.allOnShelf')}
								text={t('page.desk.nothingRunning')}
							>
								<span class="qn">
									{#each quiet as p, i (p.id)}{#if i}<span class="fnt"> · </span>{/if}<a
											href={to.workshop(p.id)}>{p.name}</a
										>{/each}
								</span>
								<a class="btn btn-sm" href={to.newTicket({ project: quiet[0].id })}
									>{I.add} {t('page.desk.newTicket')}</a
								>
							</EmptyState>
						{/if}
						{#if !cards.length && filter !== 'all' && !quiet.length}
							<EmptyState
								inline
								title={filter === 'needs'
									? t('page.desk.nothingNeeds')
									: t('page.desk.nothingBrewing')}
								text={t('page.desk.noLiveMatch')}
							/>
						{/if}
					{/if}
				</section>
			</div>

			<div class="stack">
				<section class="sect">
					<SectionHeader
						title={t('page.desk.recentlyFinished')}
						sub={t('page.desk.last24hSub')}
						href={to.runs({ tab: 'finished' })}
						linkLabel={t('page.desk.allFinished')}
					/>
					{#if recent.length}
						<div class="pane">
							<RunTable
								runs={recent}
								cols={['title', 'state', 'ws', 'workshop', 'flow', 'mr', 'updated']}
							/>
						</div>
					{:else}
						<EmptyState
							compact
							title={t('page.desk.nothingBottled')}
							text={t('page.desk.nothingBottledText')}
						/>
					{/if}
				</section>

				<Capacity />
			</div>
		</div>
	{/if}
</div>

<style>
	.top {
		flex: none;
		display: flex;
		align-items: center;
		gap: 16px;
		flex-wrap: wrap;
		margin: var(--shell-gap) var(--shell-gap) 0;
		padding: 12px 12px 12px 16px;
		border-radius: var(--r-pane);
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
	/* Two stacks, so a short section never waits for the height of the one
	 * beside it. With room for three columns, the first stack splits in two. */
	.desk {
		container: desk / inline-size;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
		min-width: 0;
	}
	.stack > :global(*) {
		min-width: 0;
	}
	@container desk (min-width: 1944px) {
		.stack:first-child {
			display: contents;
		}
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
