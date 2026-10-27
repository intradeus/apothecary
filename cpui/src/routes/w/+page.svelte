<script lang="ts">
	// Workshops index: a card per workshop — its vial, repository, live runs
	// and pings, and the integrations it runs on.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/urls';
	import { t } from '$lib/i18n/index.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { I } from '$lib/ui/icons';
	import { ms, spring } from '$lib/ui/motion';
	import RunRow from '$lib/run/RunRow.svelte';
	import WorkshopChips from '$lib/workshop/WorkshopChips.svelte';
	import { flip } from 'svelte/animate';

	let integrations = $state<Integration[] | null>(null);
	onMount(() => {
		api.integrations
			.list()
			.then((l) => (integrations = l))
			.catch(() => (integrations = []));
	});

	const projects = $derived([...fleet.projects].sort((a, b) => a.name.localeCompare(b.name)));
	const liveTotal = $derived(fleet.active.length);
	const repoLabel = (url: string) => url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
	const newLabel = $derived(`${I.add} ${t('page.workshops.new')}`);
	// Cards reorder (a rename, a new workshop) on the spring rather than jumping.
	const move = () => ({ duration: ms('--dur-spring', 450), easing: spring });
</script>

<PageHeader
	crumbs={[{ label: t('nav.desk'), href: to.desk() }, { label: t('page.workshops.title') }]}
	title={t('page.workshops.title')}
	accent={t('page.workshops.accent')}
>
	{#snippet meta()}
		<span>{t('page.desk.workshopCount', { count: projects.length })}</span>
		<span><span class="hue-az mono">{liveTotal}</span> {t('page.workshops.live')}</span>
		{#if fleet.pending.length}<span
				><span class="count hot">{fleet.pending.length}</span>
				{t('page.workshops.awaitingYou')}</span
			>{/if}
	{/snippet}
	{#snippet actions()}
		<a class="btn btn-primary" href={to.newWorkshop()}>{newLabel}</a>
	{/snippet}
</PageHeader>

<div class="page">
	{#if fleet.error && !fleet.updatedAt}
		<ErrorCard
			cause={t('page.workshops.loadError')}
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<div class="card-grid">
			{#each [0, 1, 2] as i (i)}<Skeleton pane lines={4} />{/each}
		</div>
	{:else if projects.length === 0}
		<EmptyState title={t('page.workshops.empty')} text={t('page.workshops.emptyText')}>
			<a class="btn btn-primary" href={to.newWorkshop()}>{newLabel}</a>
			{#if integrations && integrations.length === 0}
				<a class="btn" href={to.system('integrations')}>{t('page.workshops.setupIntegrations')}</a>
			{/if}
		</EmptyState>
	{:else}
		<div class="card-grid">
			{#each projects as p (p.id)}
				{@const live = fleet.liveByProject.get(p.id) ?? []}
				{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
				<article class="wcard c" class:quiet={!live.length} animate:flip={move()}>
					<a class="head" href={to.workshop(p.id)}>
						<Vial projectId={p.id} idle={!live.length} />
						<span class="nm ell">{p.name}</span>
						<span class="sum">
							{#if live.length}<span class="hue-az mono">{live.length}</span>
								{t('page.workshops.live')}{:else}{t('page.workshops.quiet')}{/if}
							{#if pings}<span class="count hot" title={t('page.workshops.pingsAwaiting')}
									>{pings}</span
								>{/if}
						</span>
					</a>
					<div class="body">
						<div class="repo mono ell" title={p.repo.url}>
							{repoLabel(p.repo.url)}{#if p.repos?.length}<span class="more">
									{t('page.workshops.moreRepos', { count: p.repos.length })}</span
								>{/if}
						</div>
						<WorkshopChips project={p} {integrations} />
					</div>
					{#each live.slice(0, 3) as r (r.id)}<RunRow run={r} />{/each}
					{#if live.length > 3}
						<a class="more-runs" href={to.workshop(p.id, 'runs')}
							>{t('page.workshops.moreLive', { count: live.length - 3 })}</a
						>
					{/if}
					<div class="foot">
						<span class="fnt">{t('page.workshops.formula')}</span>
						<span class="tag">{p.defaultFlow || '—'}</span>
						<span class="spacer"></span>
						<a href={to.workshop(p.id, 'tickets')}>{t('page.workshops.tickets')}</a>
						<a href={to.workshop(p.id, 'flows')}>{t('page.workshops.flows')}</a>
						<a href={to.workshop(p.id)}>{t('page.workshops.open')} {I.forward}</a>
					</div>
				</article>
			{/each}
		</div>
	{/if}
</div>

<style>
	.card-grid {
		--min: 340px;
	}
	.wcard {
		border-radius: var(--r-pane);
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 9px;
		padding: 11px 14px 4px;
		color: var(--tx);
	}
	.head:hover {
		text-decoration: none;
	}
	.head:hover .nm {
		text-decoration: underline;
	}
	.nm {
		flex: 1;
		font-weight: 600;
		font-size: var(--fs-aside);
	}
	.wcard.quiet .nm {
		color: var(--mut);
	}
	.sum {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 2px 14px 12px 33px;
	}
	.repo {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.more {
		color: var(--fnt);
	}
	.more-runs {
		padding: 6px 14px;
		border-top: 1px solid var(--cLn);
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px 14px;
		border-top: 1px solid var(--cLn);
		font-size: var(--fs-help);
	}
	.foot a {
		color: var(--mut);
	}
	.foot a:hover {
		color: var(--tx);
	}
</style>
