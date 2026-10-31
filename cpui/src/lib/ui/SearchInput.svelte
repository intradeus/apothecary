<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A search field with its glyph, for list filters and pickers.
	import { I } from '$lib/design/icons';

	let {
		value = $bindable(''),
		placeholder,
		width = 280,
		label,
		/** sm: in a toolbar. */
		size = 'md',
		onenter
	}: {
		value: string;
		placeholder?: string;
		/** Max width in px; it shrinks below that on narrow screens. */
		width?: number;
		label?: string;
		size?: 'sm' | 'md';
		onenter?: (v: string) => void;
	} = $props();
</script>

<label class="fieldbox si" class:sm={size === 'sm'} style:--w="{width}px">
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
		cursor: text;
	}
	.g {
		color: var(--text-muted);
		font-size: var(--fs-lead);
	}
	input {
		padding: 6px 0;
		font-size: var(--fs-ui);
	}
	.sm {
		min-width: 100px;
		padding: 0 8px;
		border-radius: var(--r-tab);
	}
	.sm .g {
		font-size: var(--fs-body);
	}
	.sm input {
		padding: 2px 0;
		font-size: var(--fs-label);
	}
	input::-webkit-search-cancel-button {
		filter: grayscale(1);
	}
</style>
