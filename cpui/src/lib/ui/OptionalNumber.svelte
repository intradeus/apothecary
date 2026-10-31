<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A whole number the flow may leave unset (max turns: "flow default"; a
	// budget: "no cap"). Stepper has no empty state, so unset reads as a
	// quiet line with Set, and a set value gets a clear (×).
	import Stepper from './Stepper.svelte';
	import { I } from '$lib/design/icons';

	let {
		value = $bindable(),
		empty,
		fallback,
		min = 1,
		max = 100000,
		id,
		label,
		onchange
	}: {
		value: number | undefined;
		empty: string;
		fallback: number;
		min?: number;
		max?: number;
		id?: string;
		label?: string;
		onchange?: () => void;
	} = $props();

	function set(v: number | undefined) {
		value = v;
		onchange?.();
	}
</script>

{#if value !== undefined && value !== null}
	<div class="row nowrap on">
		<Stepper {id} {label} {min} {max} bind:value={() => value ?? fallback, set} />
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
	</div>
{/if}

<style>
	.on {
		gap: 6px;
	}
	.unset {
		gap: 8px;
		min-height: 30px;
		font-size: var(--fs-label);
	}
</style>
