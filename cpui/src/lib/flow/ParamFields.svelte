<script lang="ts">
	import Checkbox from '$lib/ui/Checkbox.svelte';
	// Typed inputs for a flow's declared parameters, shared by the workshop's
	// Configure sheet and the start-run form. Each value is a string the
	// engine parses by the parameter's type; empty means "not set here", and
	// the label says what applies then — the layer underneath (the workshop's
	// value on the start form), else the flow's default. The engine-known
	// names get their pickers by name: the harness from the workshop's set,
	// the model from what that harness lists, the timeout as a duration.
	import type { Model, ParamDecl } from '$lib/domain';
	import DurationInput from '$lib/ui/DurationInput.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		params,
		values,
		under,
		underLabel = 'workshop',
		harnesses = [],
		models = [],
		idPrefix = 'pf',
		onchange
	}: {
		params: ParamDecl[];
		values: Record<string, string>;
		/** The layer underneath, per name: what applies when a value is left
		 *  empty here (the workshop's binding, on the start form). The flow's
		 *  default applies where it says nothing. */
		under?: Record<string, string>;
		/** Whose values `under` are, for the label. */
		underLabel?: 'workshop' | 'run';
		/** The harness kinds the workshop can run, for the harness picker. */
		harnesses?: { kind: string; label: string }[];
		/** What the effective harness lists, for the model field. */
		models?: Model[];
		idPrefix?: string;
		onchange: (name: string, value: string) => void;
	} = $props();

	type Kind =
		'bool' | 'int' | 'float' | 'enum' | 'duration' | 'harness' | 'model' | 'string' | 'list';
	function kindOf(p: ParamDecl): Kind {
		if (p.list) return 'list';
		if (p.name === 'harness') return 'harness';
		if (p.name === 'model') return 'model';
		if (p.type !== 'string') return p.type;
		if (p.enum?.length) return 'enum';
		return 'string';
	}
	// A list's value is a JSON array; it is shown, and typed, as its items.
	function items(json: string | undefined): string[] | undefined {
		if (!json) return undefined;
		try {
			const v = JSON.parse(json);
			return Array.isArray(v) ? v.map(String) : undefined;
		} catch {
			return undefined;
		}
	}
	const shown = (p: ParamDecl, v: string) => (p.list ? (items(v) ?? []).join(', ') : v);
	const applies = (p: ParamDecl) => shown(p, under?.[p.name] ?? p.default);
	function appliesLabel(p: ParamDecl): string {
		const u = under?.[p.name];
		if (u === undefined) return t('flow.fields.flowDefault', { value: shown(p, p.default) });
		return underLabel === 'run'
			? t('flow.fields.fromRun', { value: shown(p, u) })
			: t('flow.fields.fromWorkshop', { value: shown(p, u) });
	}
	/** Toggle one enum value of a list, starting from what applies. */
	function toggle(p: ParamDecl, v: string, value: string, on: boolean) {
		const cur = items(v) ?? items(under?.[p.name] ?? p.default) ?? [];
		const next = on ? [...cur.filter((x) => x !== value), value] : cur.filter((x) => x !== value);
		onchange(p.name, JSON.stringify((p.enum ?? []).filter((o) => next.includes(o))));
	}
	const id = (p: ParamDecl) => `${idPrefix}-${p.name}`;
</script>

{#each params as p (p.name)}
	{@const kind = kindOf(p)}
	{@const v = values[p.name] ?? ''}
	<div class="field pf">
		<label for={id(p)}>
			<span class="mono">{p.name}</span>
			<span class="fnt small">{appliesLabel(p)}</span>
		</label>
		{#if p.description}<div class="help">{p.description}</div>{/if}
		{#if kind === 'list' && p.enum?.length}
			{@const on = items(v) ?? items(under?.[p.name] ?? p.default) ?? []}
			<div class="checks" id={id(p)}>
				{#each p.enum as o (o)}
					<Checkbox checked={on.includes(o)} onchange={(c) => toggle(p, v, o, c)}
						><span class="mono">{o}</span></Checkbox
					>
				{/each}
				{#if v}<button type="button" class="btn-sm" onclick={() => onchange(p.name, '')}
						>{t('flow.fields.clear')}</button
					>{/if}
			</div>
		{:else if kind === 'list'}
			<input
				id={id(p)}
				class="mono"
				value={shown(p, v)}
				placeholder={applies(p)}
				oninput={(e) => {
					const list = e.currentTarget.value
						.split(',')
						.map((x) => x.trim())
						.filter(Boolean);
					onchange(p.name, e.currentTarget.value.trim() ? JSON.stringify(list) : '');
				}}
			/>
			<div class="help">{t('flow.fields.listHelp')}</div>
		{:else if kind === 'bool'}
			<Select
				id={id(p)}
				value={v}
				options={[
					{ value: '', label: t('flow.fields.unset') },
					{ value: 'true', label: 'true' },
					{ value: 'false', label: 'false' }
				]}
				onchange={(nv) => onchange(p.name, nv)}
			/>
		{:else if kind === 'enum'}
			<Select
				id={id(p)}
				value={v}
				options={[
					{ value: '', label: t('flow.fields.unset') },
					...(p.enum ?? []).map((o) => ({ value: o, label: o }))
				]}
				onchange={(nv) => onchange(p.name, nv)}
			/>
		{:else if kind === 'harness'}
			<Select
				id={id(p)}
				value={v}
				options={[
					{ value: '', label: t('flow.fields.unset') },
					...harnesses.map((h) => ({ value: h.kind, label: h.label })),
					...(v && !harnesses.some((h) => h.kind === v) ? [{ value: v, label: v }] : [])
				]}
				onchange={(nv) => onchange(p.name, nv)}
			/>
		{:else if kind === 'model'}
			<input
				id={id(p)}
				list="{id(p)}-list"
				value={v}
				placeholder={applies(p)}
				autocomplete="off"
				oninput={(e) => onchange(p.name, e.currentTarget.value.trim())}
			/>
			<datalist id="{id(p)}-list">
				{#each models as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
			</datalist>
		{:else if kind === 'duration'}
			<div class="dur">
				<DurationInput
					id={id(p)}
					label={p.name}
					bind:value={() => v, (nv) => onchange(p.name, nv)}
				/>
				{#if v}<button type="button" class="btn-sm" onclick={() => onchange(p.name, '')}
						>{t('flow.fields.clear')}</button
					>{/if}
			</div>
		{:else if kind === 'int' || kind === 'float'}
			<input
				id={id(p)}
				type="number"
				step={kind === 'float' ? 'any' : '1'}
				value={v}
				placeholder={applies(p)}
				oninput={(e) => onchange(p.name, e.currentTarget.value.trim())}
			/>
		{:else}
			<input
				id={id(p)}
				value={v}
				placeholder={applies(p)}
				oninput={(e) => onchange(p.name, e.currentTarget.value)}
			/>
		{/if}
	</div>
{/each}

<style>
	.pf label {
		display: flex;
		align-items: baseline;
		gap: 8px;
		flex-wrap: wrap;
	}
	.dur {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.checks {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 12px;
		align-items: center;
	}
</style>
