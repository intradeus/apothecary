<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A search field with its glyph, for list filters and pickers.
	import { I } from './icons';

	let {
		value = $bindable(''),
		placeholder,
		width = 280,
		label,
		onenter
	}: {
		value: string;
		placeholder?: string;
		/** Max width in px; it shrinks below that on narrow screens. */
		width?: number;
		label?: string;
		onenter?: (v: string) => void;
	} = $props();
</script>

<label class="si" style:--w="{width}px">
	<span class="g" aria-hidden="true">{I.search}</span>
	<input
		class="bare"
		type="search"
		bind:value
		placeholder={placeholder ?? t('ui.search.placeholder')}
		aria-label={label ?? placeholder ?? t('ui.search.placeholder')}
		onkeydown={(e) => {
			// A search never submits the form around it.
			if (e.key === 'Enter') {
				e.preventDefault();
				onenter?.(value);
			}
		}}
	/>
</label>

<style>
	.si {
		display: flex;
		align-items: center;
		gap: 6px;
		/* Grows to its width, shrinks below it, never forces a wrap. */
		flex: 0 1 var(--w);
		width: var(--w);
		max-width: 100%;
		min-width: 120px;
		margin: 0;
		padding: 0 10px;
		border-radius: var(--r-row);
		background: var(--p1);
		border: 1px solid var(--ln2);
		cursor: text;
	}
	.si:focus-within {
		border-color: var(--brand);
		box-shadow: 0 0 0 3px var(--brandBg);
	}
	.g {
		color: var(--mut);
		font-size: var(--fs-lead);
	}
	input {
		padding: 6px 0;
		font-size: var(--fs-ui);
	}
	input::-webkit-search-cancel-button {
		filter: grayscale(1);
	}
</style>
