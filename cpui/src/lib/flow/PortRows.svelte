<script lang="ts">
	// A stage's ports (FLOWLANG §4), input or output side: each a name, where
	// it comes from (`from:`) or goes (`to:`), and what it is — a type, a
	// schema, and for an input whether it may be empty. With `declared` (an
	// agent's or an action's contract) the rows are the contract's, one per
	// declared port, named by it; without, they are free (a command's labels,
	// what a gate shows) and can be added, renamed and removed. A gate's rows
	// are files and folders, and at most one link (`links`): once a row reads
	// a value the engine provides, the others are offered paths only, and a
	// second one typed anyway is marked.
	//
	// Rows live here while they are typed and are written to the document as
	// they change: only rows with a name and a value reach it, contract rows
	// first in declaration order. A value the engine provides fixes the type
	// (a running service's address, run.service.<name>, is a link); a path
	// ending in "/" is a directory, type files, and holds no schema.
	//
	// Mounted once per selection (the inspector keys its content on it), so
	// its rows always belong to one stage.
	import { untrack } from 'svelte';
	import {
		FILE_TYPES,
		VALUE_TYPES,
		isEdgeSource,
		isEngineSource,
		portValue,
		sourceType,
		takesSchema,
		type DocPort,
		type DocStage,
		type IODecl
	} from '$lib/domain/flowdoc';
	import { t } from '$lib/i18n/index.svelte';
	import Select from '$lib/ui/Select.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { I } from '$lib/design/icons';

	let {
		stage,
		field,
		declared,
		sources = [],
		paths = [],
		writtenTypes = {},
		schemas = [],
		types,
		links,
		valuePlaceholder = 'artifacts/…',
		onchange
	}: {
		stage: DocStage;
		field: 'input' | 'output';
		/** The contract's ports: fixed rows. Absent: free rows. */
		declared?: IODecl[];
		/** Engine sources the stage may read (inputs). */
		sources?: string[];
		/** Paths written elsewhere in the flow, offered as sources (inputs). */
		paths?: string[];
		/** What those paths hold, by path: picking one prefills its writer's
		 *  type, so a gate shown a review curates it rather than reading it
		 *  as a plain file. */
		writtenTypes?: Record<string, string>;
		/** The flow's schemas. */
		schemas?: string[];
		/** The types a row's file may be (default: every port type). */
		types?: string[];
		/** At most this many rows read a value the engine provides (a gate
		 *  opens one link). */
		links?: number;
		valuePlaceholder?: string;
		onchange: () => void;
	} = $props();

	type Row = DocPort & { key: string; value: string; decl?: IODecl };
	const isInput = $derived(field === 'input');

	function load(): Row[] {
		const ports = stage[field] ?? {};
		const rows: Row[] = (declared ?? []).map((d) => ({
			...(ports[d.name] ?? {}),
			key: d.name,
			value: portValue(ports[d.name]),
			decl: d
		}));
		for (const [key, p] of Object.entries(ports)) {
			if (!declared?.some((d) => d.name === key)) rows.push({ ...p, key, value: portValue(p) });
		}
		return rows;
	}
	let rows = $state<Row[]>(untrack(load));

	function write() {
		const out: Record<string, DocPort> = {};
		for (const r of rows) {
			const key = r.key.trim();
			const value = r.value.trim();
			if (!key || !value || key in out) continue;
			const p: DocPort = isInput ? { from: value } : { to: value };
			if (r.type) p.type = r.type;
			if (r.schema) p.schema = r.schema;
			if (r.description) p.description = r.description;
			if (isInput && r.optional) p.optional = true;
			out[key] = p;
		}
		stage[field] = Object.keys(out).length ? out : undefined;
		onchange();
	}

	// A value the engine provides is one type; a file falls back to what the
	// contract declares, else to what its writer declares, else a plain
	// file. A free row still typed as a plain file takes its writer's type
	// too — the type is the file's, not the row's.
	function setValue(r: Row, value: string) {
		r.value = value;
		const engine = sourceType(value.trim());
		const written = writtenTypes[value.trim()];
		if (engine) {
			r.type = engine;
			r.schema = undefined;
			if (isEdgeSource(value.trim())) r.optional = true;
		} else if (
			!r.type ||
			VALUE_TYPES.includes(r.type) ||
			(r.type === 'file' && !r.decl && !!written && written !== 'file')
		) {
			const dir = value.trim().endsWith('/');
			r.type = r.decl?.type ?? (r.decl?.schema ? undefined : (written ?? (dir ? 'files' : 'file')));
			r.schema = takesSchema(r.type) ? (r.schema ?? r.decl?.schema) : undefined;
		}
		write();
	}
	// A directory's path ends in "/", and only a directory's does.
	const dirMismatch = (r: Row) => {
		const v = r.value.trim();
		return !!v && !isEngineSource(v) && (r.type === 'files') !== v.endsWith('/');
	};

	const typeChoices = (r: Row) => {
		const all = types ?? (isInput ? [...FILE_TYPES, ...VALUE_TYPES] : FILE_TYPES);
		return r.type && !all.includes(r.type) ? [...all, r.type] : all;
	};
	// The rows reading an engine value, in order: past the budget, one is
	// refused (the first ones keep theirs).
	const engineRows = $derived(rows.filter((r) => isEngineSource(r.value.trim())));
	const overLinks = (r: Row) =>
		links != null && engineRows.includes(r) && engineRows.indexOf(r) >= links;
	// A row is offered engine values while the budget has room, or it holds one.
	const offersEngine = (r: Row) =>
		links == null || engineRows.length < links || engineRows.indexOf(r) >= 0;
	const schemaChoices = (r: Row) =>
		r.schema && !schemas.includes(r.schema) ? [...schemas, r.schema] : schemas;
	const invalid = (r: Row) =>
		!!r.key.trim() && rows.filter((x) => x.key.trim() === r.key.trim()).length > 1;
	const listId = $derived(`ports-${stage.id}-${field}`);
	const free = $derived(!declared);
	const canAdd = $derived(free);
</script>

{#if isInput}
	<datalist id={listId}>
		{#each sources as v (v)}<option value={v}></option>{/each}
		{#each paths as v (v)}<option value={v}></option>{/each}
	</datalist>
	{#if links != null}
		<datalist id="{listId}-paths">
			{#each paths as v (v)}<option value={v}></option>{/each}
		</datalist>
	{/if}
{/if}
{#each rows as r, i (i)}
	{@const engine = isEngineSource(r.value.trim())}
	<div class="io-bind card-in port">
		<div class="row nowrap">
			{#if free}
				<input
					class="mono grow"
					class:invalid={invalid(r)}
					value={r.key}
					placeholder={t('flow.binding.keyPlaceholder')}
					aria-label={t('flow.insp.portName')}
					oninput={(e) => {
						r.key = e.currentTarget.value;
						write();
					}}
				/>
				<button
					class="btn-sm btn-ghost"
					onclick={() => {
						rows = rows.filter((_, j) => j !== i);
						write();
					}}>{t('flow.insp.removePort')}</button
				>
			{:else}
				<span class="mono grow">{r.key}</span>
				<!-- An agent's input says optional; an action's says whether it is
				     required (attachments: not). -->
				{#if r.decl?.optional || r.decl?.required === false || (r.decl && !r.decl.required && !isInput)}
					<span class="faint">{isInput ? t('flow.insp.optional') : ''}</span>
				{:else if isInput}
					<span class="hue-warning">{t('flow.insp.required')}</span>
				{/if}
			{/if}
		</div>
		{#if r.decl?.description}<div class="help">{r.decl.description}</div>{/if}
		<input
			class="mono"
			class:invalid={overLinks(r)}
			list={isInput ? (offersEngine(r) ? listId : `${listId}-paths`) : undefined}
			value={r.value}
			placeholder={r.decl?.defaultPath ??
				((r.type ?? r.decl?.type) === 'files' ? 'artifacts/…/' : valuePlaceholder)}
			aria-label={isInput ? t('flow.insp.portFrom') : t('flow.insp.portTo')}
			oninput={(e) => setValue(r, e.currentTarget.value)}
		/>
		<div class="row nowrap">
			<span class="grow">
				<Select
					value={r.type ?? ''}
					disabled={engine}
					label={t('flow.insp.portType')}
					options={[
						{ value: '', label: t('flow.insp.noType') },
						...typeChoices(r).map((ty) => ({ value: ty, label: ty }))
					]}
					onchange={(v) => {
						r.type = v || undefined;
						if (!takesSchema(r.type)) r.schema = undefined;
						write();
					}}
				/>
			</span>
			{#if !engine && r.type !== 'file' && r.type !== 'files' && (schemas.length || r.schema)}
				<span class="grow">
					<Select
						value={r.schema ?? ''}
						label={t('flow.insp.portSchema')}
						options={[
							{ value: '', label: t('flow.insp.noSchema') },
							...schemaChoices(r).map((n) => ({ value: n, label: n }))
						]}
						onchange={(v) => {
							r.schema = v || undefined;
							write();
						}}
					/>
				</span>
			{/if}
		</div>
		{#if overLinks(r)}
			<div class="error">{t('flow.insp.oneLink', { count: links ?? 0 })}</div>
		{/if}
		{#if dirMismatch(r)}
			<div class="help hue-warning">
				{r.type === 'files' ? t('flow.insp.filesPath') : t('flow.insp.dirNotFiles')}
			</div>
		{/if}
		{#if isInput}
			<Toggle
				checked={!!r.optional}
				label={t('flow.insp.optional')}
				title={t('flow.insp.portOptionalTitle')}
				onchange={(v) => {
					r.optional = v || undefined;
					write();
				}}
			/>
			{#if isEdgeSource(r.value.trim())}<div class="help">{t('flow.insp.feedHelp')}</div>{/if}
		{/if}
	</div>
{/each}
{#if canAdd}
	<div class="chips">
		<button
			class="btn-sm"
			onclick={() => {
				rows = [...rows, { key: '', value: '', type: 'file' }];
			}}>{I.add} {t('flow.binding.add')}</button
		>
	</div>
{/if}

<style>
	/* One port: its name, where it comes from or goes, what it is. */
	.port {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.io-bind {
		padding: 7px 9px;
	}
</style>
