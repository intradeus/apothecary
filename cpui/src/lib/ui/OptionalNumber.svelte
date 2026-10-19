<script lang="ts">
	// A whole number the flow may leave unset (max turns: "flow default"; a
	// budget: "no cap"). Stepper has no empty state, so unset reads as a
	// quiet line with Set, and a set value gets a clear (×).
	import Stepper from './Stepper.svelte';
	import { I } from './icons';

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
			aria-label="Clear {label ?? 'value'}"
			onclick={() => set(undefined)}>{I.close}</button
		>
	</div>
{:else}
	<div class="row nowrap unset">
		<span class="fnt grow">{empty}</span>
		<button type="button" {id} class="btn-dashed btn-sm" onclick={() => set(fallback)}>Set</button>
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
