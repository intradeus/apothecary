<script lang="ts">
	// Build or change one panel: what a row is (grain), what to aggregate
	// (measures), how to split it (group-by, time bucket), what to leave out
	// (filters), how to draw it, how wide, and optionally which scorecard ranks
	// its rows. A live preview runs the query as it is being built.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import {
		BUCKETS,
		PANEL_WIDTHS,
		VIZES,
		type Agg,
		type AnalyticsFilter,
		type AnalyticsResult,
		type Bucket,
		type FilterOp,
		type Grain,
		type MeasureRef,
		type Panel as PanelT,
		type PanelWidth,
		type Scorecard,
		type Viz
	} from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import TagInput from '$lib/ui/TagInput.svelte';
	import { catalog } from './catalog.svelte';
	import Panel from './Panel.svelte';
	import { scoped } from './scope';
	import { rowLabel } from './viz/shape';

	let {
		open = $bindable(false),
		panel = null,
		from,
		onsave
	}: {
		open: boolean;
		/** The panel to change; none: a new one. */
		panel?: PanelT | null;
		/** The dashboard range's start, for the preview. */
		from?: string;
		onsave: (p: PanelT) => void;
	} = $props();

	const MAX_MEASURES = 8;
	const MAX_GROUP = 3;
	const MAX_FILTERS = 10;
	const BOX: Agg[] = ['p25', 'median', 'p75', 'min', 'max'];
	const OPS: FilterOp[] = ['eq', 'neq', 'in', 'not_in'];

	const blank = (): PanelT => ({
		id: `p-${crypto.randomUUID().slice(0, 8)}`,
		title: '',
		query: { grain: 'runs', measures: [], groupBy: [], filters: [], time: {} },
		viz: 'table',
		width: 6
	});

	let draft = $state<PanelT>(blank());
	let scorecards = $state<Scorecard[]>([]);
	let preview = $state<AnalyticsResult | null>(null);
	let tried = $state(false);

	// A fresh copy each time it opens: cancelling leaves the panel untouched.
	function reset() {
		draft = structuredClone($state.snapshot(panel) ?? blank());
		preview = null;
		tried = false;
		catalog.load();
		api.analytics.scorecards
			.list()
			.then((s) => (scorecards = s))
			.catch(() => (scorecards = []));
	}
	$effect(() => {
		if (open) untrack(reset);
	});

	const q = $derived(draft.query);
	const grain = $derived(catalog.grain(q.grain));
	const grainOpts = $derived<SelectOption<Grain>[]>(
		(catalog.data?.grains ?? []).map((g) => ({ value: g.id, label: g.label }))
	);
	const measureOpts = $derived<SelectOption[]>([
		...(grain?.measures ?? []).map((f) => ({
			value: f.id,
			label: f.label,
			group: t('analytics.editor.measuresGroup')
		})),
		...(grain?.derived ?? []).map((f) => ({
			value: f.id,
			label: f.label,
			group: t('analytics.editor.derivedGroup')
		}))
	]);
	const dimOpts = $derived<SelectOption[]>(
		(grain?.dimensions ?? []).map((f) => ({ value: f.id, label: f.label }))
	);
	const aggOpts = (field: string): SelectOption<Agg>[] =>
		(catalog.field(q.grain, field)?.aggs ?? []).map((a) => ({
			value: a,
			label: t(`analytics.agg.${a}`)
		}));
	const opOpts = $derived<SelectOption<FilterOp>[]>(
		OPS.map((o) => ({ value: o, label: t(`analytics.op.${o}`) }))
	);
	const scoreOpts = $derived<SelectOption[]>([
		{ value: '', label: t('analytics.editor.noScore') },
		...scorecards.map((s) => ({ value: s.id, label: s.name }))
	]);
	const baselineOpts = $derived<SelectOption[]>([
		{ value: '', label: t('analytics.editor.minMax') },
		...(preview?.rows ?? []).map((r) => ({
			value: JSON.stringify(r.dims),
			label: rowLabel(preview!, r)
		}))
	]);

	/** A measure ref on `field`, keeping `agg` when the field takes it. */
	function measureOn(field: string, agg?: Agg): MeasureRef {
		// Fields come from the catalog's own lists, so it knows them.
		const aggs = catalog.field(q.grain, field)!.aggs;
		if (!aggs.length) return { field };
		return { field, agg: agg && aggs.includes(agg) ? agg : aggs[0] };
	}

	function setGrain(g: Grain) {
		draft.query = { ...q, grain: g, measures: [], groupBy: [], filters: [], order: undefined };
	}

	function addMeasure() {
		const first = grain?.measures[0] ?? grain?.derived[0];
		if (first) q.measures.push(measureOn(first.id));
	}

	function addGroup() {
		const free = grain?.dimensions.find((d) => !q.groupBy.includes(d.id));
		if (free) q.groupBy.push(free.id);
	}

	function addFilter() {
		const first = grain?.dimensions[0];
		if (first) q.filters.push({ field: first.id, op: 'eq', values: [] } satisfies AnalyticsFilter);
	}

	// A box plot reads five aggregations of one field: put them in place of
	// the measures, on the first field that aggregates.
	function setViz(v: Viz) {
		draft.viz = v;
		if (v !== 'box') return;
		const field =
			q.measures.find((m) => m.agg)?.field ?? grain?.measures.find((f) => f.aggs.length)?.id;
		if (field) q.measures = BOX.map((agg) => ({ field, agg }));
	}

	function setBucket(b: Bucket | 'none') {
		q.time = b === 'none' ? {} : { bucket: b };
	}

	function setScore(id: string) {
		q.score = id ? { scorecard: id } : undefined;
		if (id && !q.order) q.order = { by: 'score', desc: true };
		if (!id && q.order?.by === 'score') q.order = undefined;
	}

	function setBaseline(v: string) {
		// Offered only while a scorecard is picked.
		q.score = { scorecard: q.score!.scorecard, ...(v ? { baseline: JSON.parse(v) } : {}) };
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		tried = true;
		if (!draft.title.trim()) return;
		onsave({ ...$state.snapshot(draft), title: draft.title.trim() });
		open = false;
	}

	const previewQuery = $derived(scoped(q, from, {}, () => true));
</script>

<Modal
	bind:open
	title={panel ? t('analytics.editor.editTitle') : t('analytics.editor.newTitle')}
	width={920}
>
	<div class="pe">
		<form id="panel-form" class="form-dense fields" onsubmit={save} novalidate>
			<div class="field">
				<label for="pe-title">{t('analytics.editor.title')}</label>
				<input
					id="pe-title"
					bind:value={draft.title}
					class:invalid={tried && !draft.title.trim()}
					required
				/>
				{#if tried && !draft.title.trim()}<div class="error">
						{t('analytics.editor.needTitle')}
					</div>{/if}
			</div>

			<div class="field">
				<label for="pe-grain">{t('analytics.editor.grain')}</label>
				<Select id="pe-grain" options={grainOpts} value={q.grain} onchange={setGrain} />
			</div>

			<fieldset class="field">
				<legend>{t('analytics.editor.measures')}</legend>
				{#if !q.measures.length}<p class="help">{t('analytics.editor.countOnly')}</p>{/if}
				{#each q.measures as m, i (i)}
					<div class="line">
						<Select
							size="sm"
							label={t('analytics.editor.measureN', { n: i + 1 })}
							options={measureOpts}
							value={m.field}
							onchange={(f) => (q.measures[i] = measureOn(f, m.agg))}
						/>
						{#if m.agg}
							<Select
								size="sm"
								inline
								label={t('analytics.editor.aggN', { n: i + 1 })}
								options={aggOpts(m.field)}
								value={m.agg}
								onchange={(a) => (q.measures[i] = { field: m.field, agg: a })}
							/>
						{/if}
						<button
							type="button"
							class="btn-icon btn-sm"
							aria-label={t('analytics.editor.removeMeasure', { n: i + 1 })}
							onclick={() => q.measures.splice(i, 1)}>{I.remove}</button
						>
					</div>
				{/each}
				{#if q.measures.length < MAX_MEASURES}
					<button type="button" class="btn-dashed btn-sm" onclick={addMeasure}
						>{`${I.add} ${t('analytics.editor.addMeasure')}`}</button
					>
				{/if}
			</fieldset>

			<fieldset class="field">
				<legend>{t('analytics.editor.groupBy')}</legend>
				{#each q.groupBy as g, i (i)}
					<div class="line">
						<Select
							size="sm"
							label={t('analytics.editor.groupN', { n: i + 1 })}
							options={dimOpts}
							value={g}
							onchange={(d) => (q.groupBy[i] = d)}
						/>
						<button
							type="button"
							class="btn-icon btn-sm"
							aria-label={t('analytics.editor.removeGroup', { n: i + 1 })}
							onclick={() => q.groupBy.splice(i, 1)}>{I.remove}</button
						>
					</div>
				{/each}
				{#if q.groupBy.length < MAX_GROUP}
					<button type="button" class="btn-dashed btn-sm" onclick={addGroup}
						>{`${I.add} ${t('analytics.editor.addGroup')}`}</button
					>
				{/if}
			</fieldset>

			<div class="field">
				<span class="field-label">{t('analytics.editor.bucket')}</span>
				<Segmented
					size="sm"
					glass={false}
					label={t('analytics.editor.bucket')}
					value={q.time.bucket ?? 'none'}
					options={[
						{ value: 'none', label: t('analytics.bucket.none') },
						...BUCKETS.map((b) => ({ value: b, label: t(`analytics.bucket.${b}`) }))
					]}
					onchange={setBucket}
				/>
			</div>

			<fieldset class="field">
				<legend>{t('analytics.editor.filters')}</legend>
				{#each q.filters as f, i (i)}
					<div class="line">
						<Select
							size="sm"
							label={t('analytics.editor.filterField', { n: i + 1 })}
							options={dimOpts}
							value={f.field}
							onchange={(d) => (q.filters[i].field = d)}
						/>
						<Select
							size="sm"
							inline
							label={t('analytics.editor.filterOp', { n: i + 1 })}
							options={opOpts}
							value={f.op}
							onchange={(o) => (q.filters[i].op = o)}
						/>
						<div class="grow">
							<TagInput
								bind:values={q.filters[i].values}
								placeholder={t('analytics.editor.filterValues')}
							/>
						</div>
						<button
							type="button"
							class="btn-icon btn-sm"
							aria-label={t('analytics.editor.removeFilter', { n: i + 1 })}
							onclick={() => q.filters.splice(i, 1)}>{I.remove}</button
						>
					</div>
				{/each}
				{#if q.filters.length < MAX_FILTERS}
					<button type="button" class="btn-dashed btn-sm" onclick={addFilter}
						>{`${I.add} ${t('analytics.editor.addFilter')}`}</button
					>
				{/if}
			</fieldset>

			<div class="field">
				<span class="field-label">{t('analytics.editor.viz')}</span>
				<Segmented
					size="sm"
					glass={false}
					label={t('analytics.editor.viz')}
					value={draft.viz}
					options={VIZES.map((v) => ({ value: v, label: t(`analytics.vizKind.${v}`) }))}
					onchange={setViz}
				/>
			</div>

			<div class="field">
				<span class="field-label">{t('analytics.editor.width')}</span>
				<Segmented
					size="sm"
					glass={false}
					label={t('analytics.editor.width')}
					value={String(draft.width)}
					options={PANEL_WIDTHS.map((w) => ({ value: String(w), label: `${w}/12` }))}
					onchange={(w) => (draft.width = Number(w) as PanelWidth)}
				/>
			</div>

			<div class="field">
				<label for="pe-score">{t('analytics.editor.scorecard')}</label>
				<Select
					id="pe-score"
					options={scoreOpts}
					value={q.score?.scorecard ?? ''}
					onchange={setScore}
				/>
			</div>
			{#if q.score}
				<div class="field">
					<label for="pe-baseline">{t('analytics.editor.baseline')}</label>
					<Select
						id="pe-baseline"
						options={baselineOpts}
						value={q.score.baseline ? JSON.stringify(q.score.baseline) : ''}
						onchange={setBaseline}
					/>
					<div class="help">{t('analytics.editor.baselineHelp')}</div>
				</div>
			{/if}
		</form>

		<div class="preview">
			<span class="field-label">{t('analytics.editor.preview')}</span>
			<Panel
				panel={{ ...draft, title: draft.title || t('analytics.editor.untitled') }}
				query={previewQuery}
				onresult={(r) => (preview = r)}
			/>
		</div>
	</div>

	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={() => (open = false)}
			>{t('common.cancel')}</button
		>
		<button type="submit" form="panel-form" class="btn-primary">{t('analytics.editor.save')}</button
		>
	{/snippet}
</Modal>

<style>
	.pe {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 18px;
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	fieldset {
		border: 0;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.line {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	/* The aggregation reads in full ("median", not "me…"); the measure gives way. */
	.line > :global(.select.inline) {
		flex: none;
	}
	.grow {
		flex: 1;
		min-width: 0;
	}
	.preview {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	@media (max-width: 760px) {
		.pe {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
