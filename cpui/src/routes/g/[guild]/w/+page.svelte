<script lang="ts">
	// Workshops index: a card per workshop — its vial, repository, live runs
	// and pings, and the integrations it runs on.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { currentGuild, to } from '$lib/guild';
	import { lore, loreLc, fl, isLore } from '$lib/lore';
	import { plural } from '$lib/format';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import WorkshopChips from '$lib/workshop/WorkshopChips.svelte';
	import LiveRunRow from '$lib/workshop/LiveRunRow.svelte';

	let integrations = $state<Integration[] | null>(null);
	onMount(() => {
		api.integrations
			.list()
			.then((l) => (integrations = l))
			.catch(() => (integrations = []));
	});

	const guild = $derived(currentGuild());
	const projects = $derived([...fleet.projects].sort((a, b) => a.name.localeCompare(b.name)));
	const liveTotal = $derived(fleet.active.length);
	const repoLabel = (url: string) => url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
	const newLabel = $derived(`+ New ${lore('workshop').toLowerCase()}`);
</script>

<PageHeader
	crumbs={[{ label: guild.name, href: to.desk() }, { label: lore('workshops') }]}
	title={lore('workshops')}
	accent={isLore() ? 'at the bench' : undefined}
>
	{#snippet meta()}
		<span>{plural(projects.length, lore('workshop').toLowerCase())}</span>
		<span><span class="hue-az mono">{liveTotal}</span> live</span>
		{#if fleet.pending.length}<span
				><span class="count hot">{fleet.pending.length}</span> awaiting you</span
			>{/if}
	{/snippet}
	{#snippet actions()}
		<a class="btn btn-primary" href={to.newWorkshop()}>{newLabel}</a>
	{/snippet}
</PageHeader>

<div class="page">
	{#if fleet.error && !fleet.updatedAt}
		<ErrorCard
			cause={`Couldn’t load ${loreLc('workshops')}`}
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<div class="grid">
			{#each [0, 1, 2] as i (i)}<div class="wcard c">
					<div class="sk"><Skeleton lines={4} /></div>
				</div>{/each}
		</div>
	{:else if projects.length === 0}
		<EmptyState
			title={fl('An empty workshop', 'No projects yet')}
			text="A {loreLc(
				'workshop'
			)} ties a repository to the forge, provisioner and harness its runs use."
		>
			<a class="btn btn-primary" href={to.newWorkshop()}>{newLabel}</a>
			{#if integrations && integrations.length === 0}
				<a class="btn" href={to.system('integrations')}>Set up integrations</a>
			{/if}
		</EmptyState>
	{:else}
		<div class="grid">
			{#each projects as p (p.id)}
				{@const live = fleet.liveByProject.get(p.id) ?? []}
				{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
				<article class="wcard c" class:quiet={!live.length}>
					<a class="head" href={to.workshop(p.id)}>
						<Vial projectId={p.id} idle={!live.length} />
						<span class="nm ell">{p.name}</span>
						<span class="sum">
							{#if live.length}<span class="hue-az mono">{live.length}</span> live{:else}quiet{/if}
							{#if pings}<span class="count hot" title="pings awaiting you">{pings}</span>{/if}
						</span>
					</a>
					<div class="body">
						<div class="repo mono ell" title={p.repo.url}>
							{repoLabel(p.repo.url)}{#if p.repos?.length}<span class="more">
									+{p.repos.length} repo{p.repos.length === 1 ? '' : 's'}</span
								>{/if}
						</div>
						<WorkshopChips project={p} {integrations} />
					</div>
					{#each live.slice(0, 3) as r (r.id)}<LiveRunRow run={r} />{/each}
					{#if live.length > 3}
						<a class="more-runs" href={to.workshop(p.id, 'runs')}>+{live.length - 3} more live</a>
					{/if}
					<div class="foot">
						<span class="fnt">{fl('formula', 'flow')}</span>
						<span class="tag">{p.defaultFlow || '—'}</span>
						<span class="spacer"></span>
						<a href={to.workshop(p.id, 'tickets')}>Tickets</a>
						<a href={to.workshop(p.id, 'flows')}>Flows</a>
						<a href={to.workshop(p.id)}>Open →</a>
					</div>
				</article>
			{/each}
		</div>
	{/if}
</div>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
		gap: 10px;
		align-items: start;
	}
	.wcard {
		border-radius: 16px;
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}
	.sk {
		padding: 14px;
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
		font-size: 14px;
	}
	.wcard.quiet .nm {
		color: var(--mut);
	}
	.sum {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 11px;
		color: var(--mut);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 2px 14px 12px 33px;
	}
	.repo {
		font-size: 11px;
		color: var(--mut);
	}
	.more {
		color: var(--fnt);
	}
	.more-runs {
		padding: 6px 14px;
		border-top: 1px solid var(--cLn);
		font-size: 11.5px;
		color: var(--mut);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px 14px;
		border-top: 1px solid var(--cLn);
		font-size: 11.5px;
	}
	.foot a {
		color: var(--mut);
	}
	.foot a:hover {
		color: var(--tx);
	}
</style>
