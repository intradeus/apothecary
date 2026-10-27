<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import { I } from './icons';
	// Integer stepper: − value + (DESIGN 7.10, limits).
	let {
		value = $bindable(1),
		min = 1,
		max = 999,
		id,
		label
	}: { value: number; min?: number; max?: number; id?: string; label?: string } = $props();

	const clamp = (n: number) => Math.min(max, Math.max(min, Math.round(n)));
</script>

<div class="fieldbox st">
	<button
		type="button"
		class="b"
		aria-label={t('ui.stepper.decrease')}
		disabled={value <= min}
		onclick={() => (value = clamp(Number(value) - 1))}>{I.remove}</button
	>
	<input
		class="bare"
		{id}
		type="number"
		{min}
		{max}
		aria-label={label}
		{value}
		oninput={(e) => {
			const n = Number(e.currentTarget.value);
			if (Number.isFinite(n) && e.currentTarget.value !== '') value = clamp(n);
		}}
	/>
	<button
		type="button"
		class="b"
		aria-label={t('ui.stepper.increase')}
		disabled={value >= max}
		onclick={() => (value = clamp(Number(value) + 1))}>{I.add}</button
	>
</div>

<style>
	.st {
		display: inline-flex;
		align-items: stretch;
		overflow: hidden;
	}
	.b {
		width: 32px;
		padding: 0;
		border: 0;
		border-radius: 0;
		background: transparent;
		box-shadow: none;
		color: var(--mut);
		font-size: var(--fs-lead);
	}
	.b:hover {
		background: var(--p3);
		color: var(--tx);
	}
	/* .bare (components.css) strips the field chrome; the stepper draws it. */
	input.bare {
		width: 64px;
		border-left: 1px solid var(--ln);
		border-right: 1px solid var(--ln);
		border-radius: 0;
		padding: 6px 0;
		text-align: center;
		font-family: var(--mono);
		font-weight: 600;
		appearance: textfield;
		-moz-appearance: textfield;
	}
	input::-webkit-outer-spin-button,
	input::-webkit-inner-spin-button {
		-webkit-appearance: none;
		margin: 0;
	}
</style>
