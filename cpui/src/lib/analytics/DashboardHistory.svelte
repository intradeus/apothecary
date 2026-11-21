<script lang="ts">
	// A dashboard's history beside it: its saved versions newest first, the
	// one picked read against the current dashboard, and a restore of it
	// (saved as a new version). Offered outside the edit mode only.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import { session } from '$lib/app/session.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { ago } from '$lib/core/format';
	import {
		compareDashboard,
		sameDashboard,
		type Dashboard,
		type DashboardVersion,
		type DashboardVersionSummary,
		type VersionAuthor
	} from '$lib/domain';
	import { t, th } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';

	let { dashboard, onrestored }: { dashboard: Dashboard; onrestored: (d: Dashboard) => void } =
		$props();

	let versions = $state<DashboardVersionSummary[] | null>(null);
	let error = $state('');
	let picked = $state<DashboardVersion | null>(null);
	let pickError = $state('');
	let pickedSeq = $state(0);
	let restoring = $state(false);
	let restoreError = $state('');

	async function load() {
		versions = null;
		error = '';
		picked = null;
		pickedSeq = 0;
		try {
			versions = await api.analytics.dashboards.versions(dashboard.id);
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		void dashboard.id;
		untrack(load);
	});

	async function pick(seq: number) {
		pickedSeq = seq;
		picked = null;
		pickError = '';
		restoreError = '';
		try {
			const v = await api.analytics.dashboards.version(dashboard.id, seq);
			if (pickedSeq === seq) picked = v;
		} catch (e) {
			if (pickedSeq === seq) pickError = errorMessage(e);
		}
	}

	const who = (a: VersionAuthor) =>
		a.id === session.user?.id ? t('analytics.history.you') : (a.handle ?? a.id);

	function author(a: VersionAuthor): string {
		switch (a.kind) {
			case 'assistant':
				return t('analytics.history.byAssistant', { who: who(a) });
			case 'run':
				return t('analytics.history.byRun');
			case 'sync':
				return t('analytics.history.bySync');
			case 'backfill':
				return t('analytics.history.began');
			default:
				return who(a);
		}
	}

	function chips(v: DashboardVersionSummary): string[] {
		const out = v.changed.flatMap((c) =>
			c === 'name' || c === 'filters' ? [t(`analytics.history.changed.${c}`)] : []
		);
		const pc = v.panelChanges;
		if (pc?.added) out.push(t('analytics.history.panelsAdded', { n: pc.added }));
		if (pc?.removed) out.push(t('analytics.history.panelsRemoved', { n: pc.removed }));
		if (pc?.changed) out.push(t('analytics.history.panelsChanged', { n: pc.changed }));
		if (pc?.reordered) out.push(t('analytics.history.panelsReordered'));
		if (v.restoredFrom) out.push(t('analytics.history.restoredFrom', { seq: v.restoredFrom }));
		return out;
	}

	const cmp = $derived(picked && compareDashboard(picked, dashboard));
	const newest = $derived(versions?.[0]?.seq);
	const titles = (ps: { title: string }[]) => ps.map((p) => p.title).join(', ');

	async function restore() {
		const v = picked!;
		const ok = await confirmDialog({
			title: t('analytics.history.restoreTitle', { seq: v.seq }),
			body: th('analytics.history.restoreBody', { name: dashboard.name, seq: v.seq }),
			action: t('analytics.history.restore'),
			tone: 'warn'
		});
		if (!ok) return;
		restoring = true;
		restoreError = '';
		try {
			const d = await api.analytics.dashboards.restore(dashboard.id, v.seq);
			toast(t('analytics.history.restored'));
			onrestored(d);
		} catch (e) {
			restoreError = errorMessage(e);
		} finally {
			restoring = false;
		}
	}
</script>

<section class="history" aria-label={t('analytics.history.title')}>
	<h2>{t('analytics.history.title')}</h2>
	{#if error}
		<ErrorCard cause={t('analytics.history.loadError')} detail={error} onretry={load} />
	{:else if !versions}
		<Skeleton lines={4} />
	{:else if !versions.length}
		<EmptyState title={t('analytics.history.empty')} text={t('analytics.history.emptyText')} />
	{:else}
		<ul>
			{#each versions as v (v.seq)}
				<li>
					<button
						class="row"
						class:btn-sel={v.seq === pickedSeq}
						aria-pressed={v.seq === pickedSeq}
						onclick={() => pick(v.seq)}
					>
						<span class="seq">{t('analytics.history.version', { seq: v.seq })}</span>
						<span>{author(v.author)}</span>
						<span class="muted">{ago(v.createdAt)}</span>
						{#if v.seq === newest}<span class="pill sm flat tint-muted"
								>{t('analytics.history.current')}</span
							>{/if}
						{#each chips(v) as c (c)}<span class="pill sm flat">{c}</span>{/each}
					</button>
				</li>
			{/each}
		</ul>

		{#if pickError}
			<ErrorCard
				cause={t('analytics.history.versionError')}
				detail={pickError}
				onretry={() => pick(pickedSeq)}
			/>
		{:else if pickedSeq && !cmp}
			<Skeleton lines={3} />
		{:else if picked && cmp}
			<div class="compare">
				<h3>{t('analytics.history.compareTitle', { seq: picked.seq })}</h3>
				{#if sameDashboard(cmp)}
					<p class="muted">{t('analytics.history.same')}</p>
				{:else}
					<ul>
						{#if cmp.name}<li>{t('analytics.history.name', cmp.name)}</li>{/if}
						{#if cmp.filtersOnlyInVersion.length}<li>
								{t('analytics.history.filtersBack', { list: cmp.filtersOnlyInVersion.join(', ') })}
							</li>{/if}
						{#if cmp.filtersOnlyInCurrent.length}<li>
								{t('analytics.history.filtersGone', { list: cmp.filtersOnlyInCurrent.join(', ') })}
							</li>{/if}
						{#if cmp.filterOrder}<li>
								{t('analytics.history.filterOrder', { list: cmp.filterOrder.join(', ') })}
							</li>{/if}
						{#if cmp.panelsOnlyInVersion.length}<li>
								{t('analytics.history.panelsBack', { list: titles(cmp.panelsOnlyInVersion) })}
							</li>{/if}
						{#if cmp.panelsOnlyInCurrent.length}<li>
								{t('analytics.history.panelsGone', { list: titles(cmp.panelsOnlyInCurrent) })}
							</li>{/if}
						{#each cmp.panelsChanged as p (p.id)}<li>
								{t('analytics.history.panelChanged', {
									title: p.title,
									fields: p.fields.map((f) => t(`analytics.history.field.${f}`)).join(', ')
								})}
							</li>{/each}
						{#if cmp.panelOrder}<li>
								{t('analytics.history.panelOrder', { list: titles(cmp.panelOrder) })}
							</li>{/if}
					</ul>
				{/if}
				{#if picked.seq !== newest}
					<button class="btn-primary" onclick={restore} disabled={restoring} aria-busy={restoring}
						>{t('analytics.history.restore')}</button
					>
				{/if}
				{#if restoreError}
					<ErrorCard
						cause={t('analytics.history.restoreError')}
						detail={restoreError}
						ondismiss={() => (restoreError = '')}
					/>
				{/if}
			</div>
		{/if}
	{/if}
</section>

<style>
	.history {
		display: flex;
		flex-direction: column;
		gap: 10px;
		max-width: 720px;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
		width: 100%;
		text-align: left;
	}
	.seq {
		font-weight: 600;
	}
	.compare {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.compare ul {
		list-style: disc;
		padding-left: 18px;
	}
</style>
