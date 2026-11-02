<script lang="ts">
	// One dashboard: its panels on a 12-column grid, all over one time range
	// and narrowed by its filter bar. A built-in is read-only (duplicate it to
	// change it); one's own has an edit mode to add, change, reorder and remove
	// panels, saved as a whole.
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import type { Dashboard, Grain, Panel as PanelT } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { catalog } from './catalog.svelte';
	import FilterPick from './FilterPick.svelte';
	import Panel from './Panel.svelte';
	import PanelEditor from './PanelEditor.svelte';
	import { DEFAULT_RANGE, RANGES, rangeFrom, scoped, type Picks, type Range } from './scope';

	let { id }: { id: string } = $props();

	let dash = $state<Dashboard | null>(null);
	let error = $state('');
	let range = $state<Range>(DEFAULT_RANGE);
	let picks = $state<Picks>({});

	let editing = $state(false);
	let draft = $state<PanelT[]>([]);
	let busy = $state(false);
	let saveError = $state('');

	let editorOpen = $state(false);
	let editorAt = $state(-1);

	async function load() {
		dash = null;
		error = '';
		editing = false;
		picks = {};
		try {
			const [d] = await Promise.all([api.analytics.dashboards.get(id), catalog.load()]);
			dash = d;
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		void id;
		untrack(load);
	});

	const from = $derived(rangeFrom(range));
	// Read only once loaded: the page shows a skeleton until then.
	const panels = $derived(editing ? draft : dash!.panels);
	const has = (grain: Grain) => (dim: string) =>
		catalog.grain(grain)?.dimensions.some((d) => d.id === dim) ?? false;

	// A filter's values and label come from the first panel's grain that has
	// the dimension (runs when none does).
	const bar = $derived(
		dash!.filters.map((dim) => {
			const grain = panels.find((p) => has(p.query.grain)(dim))?.query.grain ?? 'runs';
			return { dim, grain, label: catalog.field(grain, dim)?.label ?? dim };
		})
	);

	// Both actions are only offered on a loaded dashboard.
	async function duplicate() {
		const d = dash!;
		busy = true;
		try {
			const copy = await api.analytics.dashboards.create({
				name: t('analytics.copyOf', { name: d.name }),
				filters: d.filters,
				panels: d.panels
			});
			toast(t('analytics.dashboard.duplicated'));
			goto(to.dashboard(copy.id));
		} catch (e) {
			saveError = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	function edit() {
		draft = $state.snapshot(dash!.panels) as PanelT[];
		saveError = '';
		editing = true;
	}

	async function save() {
		const d = dash!;
		busy = true;
		saveError = '';
		try {
			dash = await api.analytics.dashboards.update(d.id, {
				name: d.name,
				filters: d.filters,
				panels: $state.snapshot(draft) as PanelT[]
			});
			editing = false;
			toast(t('analytics.dashboard.saved'));
		} catch (e) {
			saveError = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	function move(i: number, by: number) {
		const [p] = draft.splice(i, 1);
		draft.splice(i + by, 0, p);
	}

	function openEditor(at: number) {
		editorAt = at;
		editorOpen = true;
	}

	function saved(p: PanelT) {
		if (editorAt < 0) draft.push(p);
		else draft[editorAt] = p;
	}
</script>

<svelte:head><title>{`${dash?.name ?? t('page.analytics.title')}`}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('page.analytics.title'), href: to.analytics() },
		{ label: dash?.name ?? '…' }
	]}
	title={dash?.name ?? ''}
>
	{#snippet chip()}
		{#if dash?.builtin}<span class="pill sm flat tint-muted">{t('analytics.builtin')}</span>{/if}
	{/snippet}
	{#snippet actions()}
		{#if dash?.builtin}
			<button class="btn-primary btn-lg" onclick={duplicate} disabled={busy} aria-busy={busy}
				>{`${I.copy} ${t('analytics.dashboard.duplicateToEdit')}`}</button
			>
		{:else if dash && editing}
			<button class="btn-lg" onclick={() => openEditor(-1)}
				>{`${I.add} ${t('analytics.dashboard.addPanel')}`}</button
			>
			<button class="btn-ghost btn-lg" onclick={() => (editing = false)} disabled={busy}
				>{t('common.cancel')}</button
			>
			<button class="btn-primary btn-lg" onclick={save} disabled={busy} aria-busy={busy}
				>{t('analytics.dashboard.save')}</button
			>
		{:else if dash}
			<button class="btn-lg" onclick={edit}>{t('analytics.edit')}</button>
		{/if}
	{/snippet}
</PageHeader>

<div class="page glass">
	{#if error}
		<ErrorCard cause={t('analytics.dashboard.loadError')} detail={error} onretry={load} />
	{:else if !dash}
		<Skeleton pane lines={8} />
	{:else}
		<div class="filters">
			<Segmented
				label={t('analytics.dashboard.range')}
				bind:value={range}
				options={RANGES.map((r) => ({ value: r, label: t(`analytics.range.${r}`) }))}
			/>
			{#each bar as f (f.dim)}
				<FilterPick
					label={f.label}
					grain={f.grain}
					field={f.dim}
					{from}
					bind:value={() => picks[f.dim] ?? [], (v) => (picks[f.dim] = v)}
				/>
			{/each}
		</div>

		{#if saveError}
			<ErrorCard
				cause={t('analytics.dashboard.saveError')}
				detail={saveError}
				ondismiss={() => (saveError = '')}
			/>
		{/if}

		{#if !panels.length}
			<EmptyState title={t('analytics.dashboard.empty')} text={t('analytics.dashboard.emptyText')}>
				{#if editing}
					<button class="btn-primary" onclick={() => openEditor(-1)}
						>{`${I.add} ${t('analytics.dashboard.addPanel')}`}</button
					>
				{:else if !dash.builtin}
					<button class="btn-primary" onclick={edit}>{t('analytics.edit')}</button>
				{/if}
			</EmptyState>
		{:else}
			<div class="grid">
				{#each panels as p, i (p.id)}
					<div class="cell" style:--span={p.width}>
						<Panel panel={p} query={scoped(p.query, from, picks, has(p.query.grain))}>
							{#snippet actions()}
								{#if editing}
									<button
										type="button"
										class="btn-icon btn-sm"
										aria-label={t('analytics.dashboard.moveUp', { title: p.title })}
										disabled={i === 0}
										onclick={() => move(i, -1)}>{I.up}</button
									>
									<button
										type="button"
										class="btn-icon btn-sm"
										aria-label={t('analytics.dashboard.moveDown', { title: p.title })}
										disabled={i === panels.length - 1}
										onclick={() => move(i, 1)}>{I.down}</button
									>
									<button
										type="button"
										class="btn-sm"
										aria-label={t('analytics.dashboard.editPanel', { title: p.title })}
										onclick={() => openEditor(i)}>{t('analytics.edit')}</button
									>
									<button
										type="button"
										class="btn-icon btn-sm"
										aria-label={t('analytics.dashboard.removePanel', { title: p.title })}
										onclick={() => draft.splice(i, 1)}>{I.close}</button
									>
								{/if}
							{/snippet}
						</Panel>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</div>

<PanelEditor
	bind:open={editorOpen}
	panel={editorAt < 0 ? null : draft[editorAt]}
	{from}
	onsave={saved}
/>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(12, minmax(0, 1fr));
		gap: 14px;
	}
	.cell {
		grid-column: span var(--span);
		min-width: 0;
	}
	@media (max-width: 900px) {
		.cell {
			grid-column: 1 / -1;
		}
	}
</style>
