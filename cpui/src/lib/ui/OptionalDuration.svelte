<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A duration the flow may leave unset (a stage timeout: "flow default";
	// a wait: "waits indefinitely"). DurationInput has no empty state, so
	// unset reads as a quiet line with Set, and a set value gets a clear (×)
	// that returns it to unset.
	import DurationInput from './DurationInput.svelte';
	import { I } from './icons';

	let {
		value = $bindable(),
		empty,
		fallback,
		id,
		label,
		onchange
	}: {
		value: string | undefined;
		/** What unset means here ("flow default · 30m"). */
		empty: string;
		/** The value Set starts from. */
		fallback: string;
		id?: string;
		label?: string;
		onchange?: () => void;
	} = $props();

	function set(v: string | undefined) {
		value = v || undefined;
		onchange?.();
	}
</script>

{#if value}
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
		<span class="fnt grow">{empty}</span>
		<button type="button" {id} class="btn-dashed btn-sm" onclick={() => set(fallback)}
			>{t('ui.optional.set')}</button
		>
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
