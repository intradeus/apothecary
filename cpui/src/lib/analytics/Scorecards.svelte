<script lang="ts">
	// Scorecards (TODO 23): weighted criteria a table panel ranks its rows by.
	// Built-ins are read-only and duplicate into an editable copy; one's own
	// edit in a modal: a name and terms, each a measure, which way is better,
	// and how much it counts.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import {
		DIRECTIONS,
		measureKey,
		type Direction,
		type MeasureRef,
		type Scorecard,
		type ScorecardTerm
	} from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { catalog } from './catalog.svelte';

	let scorecards = $state<Scorecard[] | null>(null);
	let error = $state('');
	let busy = $state(false);

	/** The scorecard being edited; id undefined while it is a new one. */
	let draft = $state<{ id?: string; name: string; terms: ScorecardTerm[] } | null>(null);
	let editorOpen = $state(false);
	let formError = $state('');

	const builtins = $derived((scorecards ?? []).filter((s) => s.builtin));
	const own = $derived((scorecards ?? []).filter((s) => !s.builtin));

	/** Every measure a term may name, across the grains: each aggregation of a
	 *  plain measure, then the derived rates. The same id on two grains is one
	 *  option. */
	const measures = $derived.by(() => {
		const out = new Map<string, SelectOption & { ref: MeasureRef }>();
		for (const g of catalog.data?.grains ?? []) {
			for (const f of g.measures)
				for (const agg of f.aggs) {
					const ref = { field: f.id, agg };
					out.set(measureKey(ref), {
						value: measureKey(ref),
						label: `${f.label} (${t(`analytics.agg.${agg}`)})`,
						group: t('analytics.editor.measuresGroup'),
						ref
					});
				}
			for (const f of g.derived)
				out.set(f.id, {
					value: f.id,
					label: f.label,
					group: t('analytics.editor.derivedGroup'),
					ref: { field: f.id }
				});
		}
		return out;
	});
	// Plain measures first, then the derived rates (stable: catalog order).
	const measureOptions = $derived(
		[...measures.values()].sort((a, b) => Number(!a.ref.agg) - Number(!b.ref.agg))
	);

	const directionOptions = DIRECTIONS.map((d) => ({
		value: d,
		get label() {
			return t(`analytics.direction.${d}`);
		}
	}));

	async function load() {
		error = '';
		try {
			[scorecards] = await Promise.all([api.analytics.scorecards.list(), catalog.load()]);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(load);

	function edit(s: Scorecard | null, copy = false) {
		draft = {
			id: s && !copy ? s.id : undefined,
			name: !s
				? t('page.analytics.scorecards.newName')
				: copy
					? t('analytics.copyOf', { name: s.name })
					: s.name,
			terms: (s?.terms ?? []).map((term) => ({ ...term, measure: { ...term.measure } }))
		};
		formError = '';
		editorOpen = true;
	}

	function addTerm() {
		const first = measureOptions[0];
		draft!.terms.push({
			measure: first ? { ...first.ref } : { field: '' },
			direction: 'lower',
			weight: 1
		});
	}

	async function save() {
		if (!draft || busy) return;
		const name = draft.name.trim();
		if (!name) return void (formError = t('page.analytics.scorecards.needName'));
		if (!draft.terms.length) return void (formError = t('page.analytics.scorecards.needTerm'));
		busy = true;
		formError = '';
		try {
			const body = { name, terms: draft.terms };
			const saved = draft.id
				? await api.analytics.scorecards.update(draft.id, body)
				: await api.analytics.scorecards.create(body);
			scorecards = draft.id
				? scorecards!.map((s) => (s.id === saved.id ? saved : s))
				: [...scorecards!, saved];
			editorOpen = false;
		} catch (e) {
			formError = `${t('page.analytics.scorecards.saveError')}: ${errorMessage(e)}`;
		} finally {
			busy = false;
		}
	}

	async function remove(s: Scorecard) {
		const ok = await confirmDialog({
			title: t('page.analytics.scorecards.deleteTitle'),
			body: t('page.analytics.scorecards.deleteBody', { name: s.name }),
			action: t('analytics.delete')
		});
		if (!ok) return;
		busy = true;
		try {
			await api.analytics.scorecards.remove(s.id);
			scorecards = scorecards!.filter((x) => x.id !== s.id);
			toast(t('page.analytics.scorecards.deleted'));
		} catch (e) {
			toast(`${t('page.analytics.scorecards.saveError')}: ${errorMessage(e)}`, { tone: 'bad' });
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>{t('page.analytics.scorecards.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.analytics.title'), href: to.analytics() },
		{ label: t('page.analytics.scorecards.title') }
	]}
	title={t('page.analytics.scorecards.title')}
	accent={t('page.analytics.scorecards.accent')}
>
	{#snippet meta()}<span>{t('page.analytics.scorecards.meta')}</span>{/snippet}
	{#snippet actions()}
		<button class="btn-primary btn-lg" onclick={() => edit(null)}
			>{I.add} {t('page.analytics.scorecards.new')}</button
		>
	{/snippet}
</PageHeader>

{#snippet card(s: Scorecard)}
	<article class="sc glass pane">
		<div class="top">
			<span class="name truncate">{s.name}</span>
			{#if s.builtin}<span class="pill sm flat tint-muted">{t('analytics.builtin')}</span>{/if}
		</div>
		<ul class="terms">
			{#each s.terms as term, i (i)}
				<li>
					<span class="truncate"
						>{measures.get(measureKey(term.measure))?.label ?? measureKey(term.measure)}</span
					>
					<span class="faint">{t(`analytics.direction.${term.direction}`)} · ×{term.weight}</span>
				</li>
			{/each}
		</ul>
		<div class="acts">
			{#if s.builtin}
				<button class="btn-sm" onclick={() => edit(s, true)}>{t('analytics.duplicate')}</button>
			{:else}
				<button class="btn-sm btn-ghost" onclick={() => edit(s)}>{t('analytics.edit')}</button>
				<button class="btn-sm btn-ghost" onclick={() => edit(s, true)}
					>{t('analytics.duplicate')}</button
				>
				<button class="btn-sm btn-danger" disabled={busy} onclick={() => remove(s)}
					>{t('analytics.delete')}</button
				>
			{/if}
		</div>
	</article>
{/snippet}

<div class="page glass">
	{#if error && !scorecards}
		<ErrorCard cause={t('page.analytics.scorecards.loadError')} detail={error} onretry={load} />
	{:else if !scorecards}
		<Skeleton pane lines={4} />
	{:else}
		<section class="col">
			<SectionHeader title={t('page.analytics.ownSection')} count={own.length} />
			{#if own.length}
				<div class="card-grid" style:--min="260px">
					{#each own as s (s.id)}{@render card(s)}{/each}
				</div>
			{:else}
				<EmptyState
					compact
					title={t('page.analytics.scorecards.noneYet')}
					text={t('page.analytics.scorecards.noneYetText')}
				/>
			{/if}
		</section>
		<section class="col">
			<SectionHeader title={t('page.analytics.builtinSection')} count={builtins.length} />
			<div class="card-grid" style:--min="260px">
				{#each builtins as s (s.id)}{@render card(s)}{/each}
			</div>
		</section>
	{/if}
</div>

<Modal
	bind:open={editorOpen}
	width={620}
	title={draft?.id
		? t('page.analytics.scorecards.editTitle')
		: t('page.analytics.scorecards.newTitle')}
>
	{#if draft}
		<div class="form">
			<div class="field">
				<label for="sc-name">{t('analytics.name')}</label>
				<input id="sc-name" bind:value={draft.name} maxlength="120" />
			</div>
			<fieldset class="field">
				<legend>{t('page.analytics.scorecards.terms')}</legend>
				<span class="help">{t('page.analytics.scorecards.termsHelp')}</span>
				{#if catalog.error}<div class="error">{catalog.error}</div>{/if}
				{#each draft.terms as term, i (i)}
					<div class="term">
						<span class="measure">
							<Select
								size="sm"
								label={`${t('page.analytics.scorecards.measure')} ${i + 1}`}
								options={measureOptions}
								value={measureKey(term.measure)}
								onchange={(v) => {
									const ref = measures.get(v)?.ref;
									if (ref) term.measure = { ...ref };
								}}
							/>
						</span>
						<Segmented
							size="sm"
							glass={false}
							label={`${t('page.analytics.scorecards.direction')} ${i + 1}`}
							options={directionOptions}
							value={term.direction}
							onchange={(v: Direction) => (term.direction = v)}
						/>
						<input
							class="weight"
							type="number"
							min="0"
							step="0.1"
							aria-label={`${t('page.analytics.scorecards.weight')} ${i + 1}`}
							bind:value={term.weight}
						/>
						<button
							class="btn-icon btn-sm btn-ghost"
							aria-label={`${t('page.analytics.scorecards.removeTerm')} ${i + 1}`}
							onclick={() => draft!.terms.splice(i, 1)}>{I.close}</button
						>
					</div>
				{/each}
				<div>
					<button class="btn-sm btn-dashed" onclick={addTerm}
						>{I.add} {t('page.analytics.scorecards.addTerm')}</button
					>
				</div>
			</fieldset>
			{#if formError}<div class="error" role="alert">{formError}</div>{/if}
		</div>
	{/if}
	{#snippet footer()}
		<button class="btn-ghost" onclick={() => (editorOpen = false)}>{t('common.cancel')}</button>
		<button class="btn-primary" disabled={busy} aria-busy={busy} onclick={save}
			>{t('common.save')}</button
		>
	{/snippet}
</Modal>

<style>
	.sc {
		display: flex;
		flex-direction: column;
		gap: 8px;
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
	}
	.terms {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 3px;
		font-size: var(--fs-label);
	}
	.terms li {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		min-width: 0;
	}
	.acts {
		display: flex;
		gap: 6px;
	}
	.form {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}
	fieldset {
		border: 0;
		margin: 0;
		padding: 0;
		gap: 8px;
	}
	.term {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.measure {
		flex: 1;
		min-width: 0;
	}
	.weight {
		width: 72px;
	}
</style>
