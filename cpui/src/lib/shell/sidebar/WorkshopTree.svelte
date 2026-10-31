<script lang="ts">
	// The workshops of the full sidebar, the current one expanded into its live
	// runs. The bead slides between workshops, which stay direct children of
	// the list; a run under one is marked on its own.
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import { shortId, stateLabel } from '$lib/core/format';
	import type { Project } from '$lib/domain';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import { I } from '$lib/design/icons';
	import { bump, indicator } from '$lib/ui/motion';

	let {
		workshops,
		currentWorkshop,
		currentRunId,
		onnavigate
	}: {
		workshops: Project[];
		currentWorkshop?: string;
		currentRunId?: string;
		onnavigate?: () => void;
	} = $props();
</script>

<div class="wh">
	<span class="whl">{t('shell.sidebar.workshops')}</span>
	<span class="legend">
		<span title={t('shell.sidebar.liveRuns')}>{I.runs}</span>
		<span title={t('shell.sidebar.pendingPings')}>{I.inbox}</span>
		<a class="plus" href={to.newWorkshop()} title={t('shell.sidebar.newWorkshop')}>{I.add}</a>
	</span>
</div>
<div class="wl on-rail" use:indicator={{ selector: '.wi.on' }}>
	{#each workshops as p (p.id)}
		{@const live = fleet.liveByProject.get(p.id) ?? []}
		{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
		{@const cur = currentWorkshop === p.id}
		<a
			class="wi"
			class:on={cur}
			class:quiet={!live.length && !cur}
			href={to.workshop(p.id)}
			onclick={onnavigate}
		>
			<Vial projectId={p.id} idle={!live.length} />
			<span class="grow truncate">{fleet.projectName(p.id)}</span>
			<span class="lc mono">{live.length || ''}</span>
			<span class="count" class:hot={pings > 0} use:bump={pings}>{pings || ''}</span>
		</a>
		{#if cur && live.length}
			{#each live as r (r.id)}
				<a
					class="ri"
					class:cur={currentRunId === r.id}
					href={to.run(r.id)}
					title="{r.title} — {stateLabel[r.state]}"
					onclick={onnavigate}
				>
					<StatusChip state={r.state} compact />
					<span class="grow truncate">{r.title}</span>
					<span class="rid mono">{shortId(r.id)}</span>
				</a>
			{/each}
		{/if}
	{:else}
		<a class="wi quiet" href={to.newWorkshop()}
			><span class="grow">{t('shell.sidebar.firstWorkshop', { arrow: I.forward })}</span></a
		>
	{/each}
</div>

<style>
	.wi {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		color: var(--text);
	}
	.wi:hover,
	.ri:hover {
		background: var(--fill-1);
		text-decoration: none;
	}
	.wi:active {
		opacity: 0.75;
	}
	.wi.on {
		font-weight: 600;
	}
	.count {
		color: var(--text-muted);
	}
	.count.hot {
		color: var(--on-brand);
	}

	.wh {
		flex: none;
		padding: 14px 18px 6px;
		font-size: var(--fs-meta);
		color: var(--text-faint);
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}
	.whl {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-aside);
		color: var(--text-muted);
	}
	.legend {
		display: flex;
		gap: 10px;
		align-items: center;
	}
	.plus {
		color: var(--text-muted);
		font-size: var(--fs-aside);
		line-height: 1;
	}
	.plus:hover {
		color: var(--text);
		text-decoration: none;
	}
	.wl {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 0 8px 8px;
	}
	.wi.quiet {
		color: var(--text-muted);
	}
	.lc {
		font-size: var(--fs-id);
		color: var(--running-text);
		min-width: 12px;
		text-align: right;
	}
	.ri {
		margin-left: 15px;
		padding: 4px 10px 4px 14px;
		border-left: 1px solid var(--line);
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: var(--fs-label);
		color: var(--text-muted);
		border-radius: 0 var(--r-tab) var(--r-tab) 0;
	}
	.ri.cur {
		background: var(--fill-3);
		color: var(--text);
	}
	.rid {
		font-size: var(--fs-id);
		color: var(--text-faint);
	}
</style>
