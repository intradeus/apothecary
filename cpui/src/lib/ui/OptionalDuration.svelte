<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A duration the flow may leave unset (a stage timeout: "flow default";
	// a wait: "waits indefinitely"). DurationInput has no empty state, so
	// unset reads as a quiet line with Set, and a set value gets a clear (×)
	// that returns it to unset. Where the site reads a parameter, the value
	// may instead be one of `params` ($name), picked from a list.
	import DurationInput from './DurationInput.svelte';
	import Select, { type SelectOption } from './Select.svelte';
	import { I } from '$lib/design/icons';

	let {
		value = $bindable(),
		empty,
		fallback,
		id,
		label,
		params = [],
		onchange
	}: {
		value: string | undefined;
		/** What unset means here ("flow default · 30m"). */
		empty: string;
		/** The value Set starts from. */
		fallback: string;
		id?: string;
		label?: string;
		/** The string parameters the site may read instead of a duration. */
		params?: string[];
		onchange?: () => void;
	} = $props();

	const isParam = $derived(!!value && value.startsWith('$'));
	const paramOptions = $derived<SelectOption[]>(
		params.map((p) => ({ value: '$' + p, label: '$' + p }))
	);
	// A parameter the site no longer offers stays listed while it is chosen.
	const chosenOptions = $derived<SelectOption[]>(
		value && !params.includes(value.slice(1))
			? [...paramOptions, { value, label: value }]
			: paramOptions
	);

	function set(v: string | undefined) {
		value = v || undefined;
		onchange?.();
	}
</script>

{#if value && isParam}
	<div class="od">
		<Select {id} mono options={chosenOptions} {value} onchange={(v) => set(v)} />
		<button
			type="button"
			class="btn-icon"
			aria-label={t('ui.optional.clear', { label: label ?? t('ui.optional.value') })}
			onclick={() => set(undefined)}>{I.close}</button
		>
	</div>
{:else if value}
	<div class="od">
		<DurationInput {id} {label} bind:value={() => value ?? '', set} />
		<button
			type="button"
			class="btn-icon"
			aria-label={t('ui.optional.clear', { label: label ?? t('ui.optional.value') })}
			onclick={() => set(undefined)}>{I.close}</button
		>
	</div>
{:else}
	<div class="row nowrap unset">
		<span class="faint grow">{empty}</span>
		<button type="button" {id} class="btn-dashed btn-sm" onclick={() => set(fallback)}
			>{t('ui.optional.set')}</button
		>
		{#if params.length}
			<!-- A menu, not a value: picking a parameter sets it (and this row
			     gives way to the chosen one), so it always reads its prompt. -->
			<Select
				size="sm"
				mono
				inline
				label={t('ui.optional.param', { label: label ?? t('ui.optional.value') })}
				placeholder={t('ui.optional.orParam')}
				options={paramOptions}
				value=""
				onchange={(v) => set(v)}
			/>
		{/if}
	</div>
{/if}

<style>
	.od {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 24px;
		gap: 6px;
		align-items: center;
	}
	.unset {
		gap: 8px;
		min-height: 30px;
		font-size: var(--fs-label);
	}
</style>
