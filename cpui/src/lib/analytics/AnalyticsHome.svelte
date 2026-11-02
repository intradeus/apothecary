<script lang="ts">
	// The analytics home (TODO 23): every dashboard over the person's own runs.
	// Built-ins first, read-only — duplicating one is how it gets changed —
	// then their own, which open, rename and delete here.
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import type { Dashboard } from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';

	/** The filter bar a new dashboard starts with: the same as the built-ins'. */
	const DEFAULT_FILTERS = ['project_id', 'flow_name', 'ticket_change_type'];

	let dashboards = $state<Dashboard[] | null>(null);
	let error = $state('');
	let busy = $state(false);

	let renaming = $state<Dashboard | null>(null);
	let renameOpen = $state(false);
	let name = $state('');

	const builtins = $derived((dashboards ?? []).filter((d) => d.builtin));
	const own = $derived((dashboards ?? []).filter((d) => !d.builtin));

	async function load() {
		error = '';
		try {
			dashboards = await api.analytics.dashboards.list();
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(load);

	/** Run a save; a failure is a toast, the list stays as it was. */
	async function saving(work: () => Promise<void>) {
		if (busy) return;
		busy = true;
		try {
			await work();
		} catch (e) {
			toast(`${t('page.analytics.saveError')}: ${errorMessage(e)}`, { tone: 'bad' });
		} finally {
			busy = false;
		}
	}

	const create = () =>
		saving(async () => {
			const d = await api.analytics.dashboards.create({
				name: t('page.analytics.newName'),
				filters: DEFAULT_FILTERS,
				panels: []
			});
			await goto(to.dashboard(d.id));
		});

	const duplicate = (src: Dashboard) =>
		saving(async () => {
			const d = await api.analytics.dashboards.create({
				name: t('analytics.copyOf', { name: src.name }),
				filters: src.filters,
				panels: src.panels
			});
			await goto(to.dashboard(d.id));
		});

	function startRename(d: Dashboard) {
		renaming = d;
		name = d.name;
		renameOpen = true;
	}

	// Only reachable from a card, so the list and the dashboard being renamed
	// are there.
	const rename = () =>
		saving(async () => {
			const d = renaming!;
			if (!name.trim()) return;
			const saved = await api.analytics.dashboards.update(d.id, {
				name: name.trim(),
				filters: d.filters,
				panels: d.panels
			});
			dashboards = dashboards!.map((x) => (x.id === saved.id ? saved : x));
			renameOpen = false;
		});

	async function remove(d: Dashboard) {
		const ok = await confirmDialog({
			title: t('page.analytics.deleteTitle'),
			body: t('page.analytics.deleteBody', { name: d.name }),
			action: t('analytics.delete')
		});
		if (!ok) return;
		await saving(async () => {
			await api.analytics.dashboards.remove(d.id);
			dashboards = dashboards!.filter((x) => x.id !== d.id);
			toast(t('page.analytics.deleted'));
		});
	}
</script>

<svelte:head><title>{t('page.analytics.title')}</title></svelte:head>

<PageHeader
	crumbs={[{ label: t('nav.desk'), href: to.desk() }, { label: t('page.analytics.title') }]}
	title={t('page.analytics.title')}
	accent={t('page.analytics.accent')}
>
	{#snippet meta()}<span>{t('page.analytics.meta')}</span>{/snippet}
	{#snippet actions()}
		<a class="btn" href={to.scorecards()}>{t('page.analytics.toScorecards')}</a>
		<button class="btn-primary btn-lg" disabled={busy} onclick={create}
			>{I.add} {t('page.analytics.new')}</button
		>
	{/snippet}
</PageHeader>

{#snippet card(d: Dashboard)}
	<article class="dc glass pane">
		<div class="top">
			<a class="name stretch truncate" href={to.dashboard(d.id)}>{d.name}</a>
			{#if d.builtin}<span class="pill sm flat tint-muted">{t('analytics.builtin')}</span>{/if}
		</div>
		<span class="faint small">{t('page.analytics.panels', { count: d.panels.length })}</span>
		<div class="acts over-stretch">
			{#if d.builtin}
				<button class="btn-sm" disabled={busy} onclick={() => duplicate(d)}
					>{t('analytics.duplicate')}</button
				>
			{:else}
				<button class="btn-sm btn-ghost" disabled={busy} onclick={() => startRename(d)}
					>{t('analytics.rename')}</button
				>
				<button class="btn-sm btn-ghost" disabled={busy} onclick={() => duplicate(d)}
					>{t('analytics.duplicate')}</button
				>
				<button class="btn-sm btn-danger" disabled={busy} onclick={() => remove(d)}
					>{t('analytics.delete')}</button
				>
			{/if}
		</div>
	</article>
{/snippet}

<div class="page glass">
	{#if error && !dashboards}
		<ErrorCard cause={t('page.analytics.loadError')} detail={error} onretry={load} />
	{:else if !dashboards}
		<Skeleton pane lines={4} />
	{:else}
		<section class="col">
			<SectionHeader title={t('page.analytics.ownSection')} count={own.length} />
			{#if own.length}
				<div class="card-grid" style:--min="260px">
					{#each own as d (d.id)}{@render card(d)}{/each}
				</div>
			{:else}
				<EmptyState
					compact
					title={t('page.analytics.noneYet')}
					text={t('page.analytics.noneYetText')}
				/>
			{/if}
		</section>
		<section class="col">
			<SectionHeader title={t('page.analytics.builtinSection')} count={builtins.length} />
			<div class="card-grid" style:--min="260px">
				{#each builtins as d (d.id)}{@render card(d)}{/each}
			</div>
		</section>
	{/if}
</div>

<Modal bind:open={renameOpen} title={t('page.analytics.renameTitle')}>
	<div class="field">
		<label for="dash-name">{t('analytics.name')}</label>
		<input
			id="dash-name"
			bind:value={name}
			maxlength="120"
			onkeydown={(e) => e.key === 'Enter' && rename()}
		/>
	</div>
	{#snippet footer()}
		<button class="btn-ghost" onclick={() => (renameOpen = false)}>{t('common.cancel')}</button>
		<button class="btn-primary" disabled={busy || !name.trim()} onclick={rename}
			>{t('common.save')}</button
		>
	{/snippet}
</Modal>

<style>
	.dc {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 12px 14px;
		min-width: 0;
	}
	.top {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.name {
		flex: 1;
		min-width: 0;
		font-weight: 600;
		color: var(--text);
	}
	.acts {
		display: flex;
		gap: 6px;
		margin-top: 4px;
	}
</style>
