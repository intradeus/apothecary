<script lang="ts">
	// One filter of a dashboard's filter bar: a button naming the dimension
	// and what is picked, opening the dimension's values (fetched on open, for
	// the current range) to tick any number of. None ticked: no filter.
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import type { AnalyticsValue, Grain } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import CheckboxGroup from '$lib/ui/CheckboxGroup.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Popover from '$lib/ui/Popover.svelte';

	let {
		label,
		grain,
		field,
		from,
		value = $bindable([]),
		onchange
	}: {
		label: string;
		/** A grain that has the dimension: where its values come from. */
		grain: Grain;
		field: string;
		/** The dashboard range's start. */
		from?: string;
		value?: string[];
		onchange?: (v: string[]) => void;
	} = $props();

	let anchor = $state<HTMLButtonElement | null>(null);
	let open = $state(false);
	let values = $state<AnalyticsValue[] | null>(null);
	let error = $state('');

	async function load() {
		values = null;
		error = '';
		try {
			values = await api.analytics.values(grain, field, from ? { from } : {});
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		if (open) load();
	});

	const text = (v: AnalyticsValue) => v.label || v.value || t('analytics.none');

	// The button names one pick by its label, more by their count.
	const summary = $derived(
		value.length === 0
			? t('analytics.filter.any')
			: value.length === 1
				? text(values?.find((v) => v.value === value[0]) ?? { value: value[0], label: '' })
				: t('analytics.filter.picked', { count: value.length })
	);

	function set(v: string[]) {
		value = v;
		onchange?.(v);
	}
</script>

<button
	bind:this={anchor}
	type="button"
	class="btn-sm"
	class:btn-sel={value.length > 0}
	aria-haspopup="dialog"
	aria-expanded={open}
	onclick={() => (open = !open)}><span class="muted">{label}:</span> {summary}</button
>

<Popover {anchor} bind:open placement="below-start" width={280} title={label} portal flip>
	<div class="fp">
		{#if error}
			<p class="error">{error}</p>
		{:else if !values}
			<Loading />
		{:else if !values.length}
			<p class="faint">{t('analytics.filter.noValues')}</p>
		{:else}
			<CheckboxGroup
				{label}
				options={values.map((v) => ({ value: v.value, label: text(v) }))}
				{value}
				onchange={set}
			/>
		{/if}
		{#if value.length}
			<button type="button" class="btn-ghost btn-sm" onclick={() => set([])}
				>{t('analytics.filter.clear')}</button
			>
		{/if}
	</div>
</Popover>

<style>
	.fp {
		display: flex;
		flex-direction: column;
		gap: 8px;
		max-height: 320px;
		overflow: auto;
	}
</style>
