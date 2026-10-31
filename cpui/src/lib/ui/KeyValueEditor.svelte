<script lang="ts" module>
	export interface KVRow {
		key: string;
		value: string;
	}
</script>

<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// An editable list of key → value rows (input bindings, commands, extra
	// headers). Mono keys, one remove per row, an add at the foot.
	import { I } from '$lib/design/icons';

	let {
		rows = $bindable([]),
		keyPlaceholder = 'name',
		valuePlaceholder = 'value',
		keyWidth = 140,
		addLabel = 'Add',
		/** Values to suggest while typing (`$WORKSPACE`, `$MR_URL`). */
		suggestions = [],
		/** Keys to mark invalid (duplicates, bad names). */
		invalid = new Set<string>(),
		multiline = false
	}: {
		rows: KVRow[];
		keyPlaceholder?: string;
		valuePlaceholder?: string;
		keyWidth?: number;
		addLabel?: string;
		suggestions?: string[];
		invalid?: Set<string>;
		/** Values are scripts: a textarea instead of an input. */
		multiline?: boolean;
	} = $props();

	const listId = `kv-${Math.random().toString(36).slice(2, 8)}`;
	const update = (i: number, patch: Partial<KVRow>) =>
		(rows = rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
</script>

{#if suggestions.length}
	<datalist id={listId}>
		{#each suggestions as s (s)}<option value={s}></option>{/each}
	</datalist>
{/if}
<div class="kve" style:--kw="{keyWidth}px">
	{#each rows as r, i (i)}
		<div class="r">
			<input
				class="mono"
				class:invalid={invalid.has(r.key)}
				value={r.key}
				placeholder={keyPlaceholder}
				aria-label={t('ui.kv.key')}
				oninput={(e) => update(i, { key: e.currentTarget.value })}
			/>
			{#if multiline}
				<textarea
					class="mono"
					rows="2"
					value={r.value}
					placeholder={valuePlaceholder}
					aria-label={t('ui.kv.valueFor', { key: r.key })}
					oninput={(e) => update(i, { value: e.currentTarget.value })}></textarea>
			{:else}
				<input
					class="mono"
					list={suggestions.length ? listId : undefined}
					value={r.value}
					placeholder={valuePlaceholder}
					aria-label={t('ui.kv.valueFor', { key: r.key })}
					oninput={(e) => update(i, { value: e.currentTarget.value })}
				/>
			{/if}
			<button
				type="button"
				class="btn-icon"
				aria-label={t('ui.kv.remove', { key: r.key || t('ui.kv.row') })}
				onclick={() => (rows = rows.filter((_, j) => j !== i))}>{I.close}</button
			>
		</div>
	{/each}
	<div>
		<button
			type="button"
			class="btn-ghost btn-sm"
			onclick={() => (rows = [...rows, { key: '', value: '' }])}>{I.add} {addLabel}</button
		>
	</div>
</div>

<style>
	.kve {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.r {
		display: grid;
		grid-template-columns: var(--kw) minmax(0, 1fr) 24px;
		gap: 6px;
		align-items: start;
	}
	.r .btn-icon {
		margin-top: 5px;
	}
	textarea {
		min-height: 36px;
	}
</style>
