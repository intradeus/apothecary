<script lang="ts">
	// Empty state (DESIGN 4.17): dashed box, display title, one line, one action.
	// `inline` is the row form for a quiet slot inside a page ("2 quiet
	// workshops — nothing running"): left-aligned, smaller title.
	import type { Snippet } from 'svelte';

	let {
		title,
		text,
		children,
		compact = false,
		inline = false
	}: {
		title: string;
		text?: string;
		children?: Snippet;
		compact?: boolean;
		inline?: boolean;
	} = $props();
</script>

<div class="es" class:compact class:inline>
	<div class="t">{title}</div>
	{#if text}<div class="x">{text}</div>{/if}
	{#if children}<div class="a">{@render children()}</div>{/if}
</div>

<style>
	.es {
		border: 1.5px dashed var(--ln2);
		border-radius: var(--r-pane);
		padding: 34px 20px;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 8px;
		text-align: center;
		flex: 1;
	}
	.es.compact {
		padding: 18px 14px;
	}
	.t {
		font-family: var(--display);
		font-weight: 600;
		font-size: var(--fs-empty);
		line-height: 1.15;
	}
	.es.inline {
		flex-direction: row;
		align-items: baseline;
		flex-wrap: wrap;
		text-align: left;
		justify-content: flex-start;
		gap: 4px 12px;
		padding: 12px 16px;
		flex: none;
	}
	.inline .t {
		font-size: var(--fs-section);
	}
	.inline .a {
		margin: 0 0 0 auto;
	}
	.compact .t {
		font-size: var(--fs-section);
	}
	.x {
		color: var(--mut);
		max-width: 40ch;
		font-size: var(--fs-ui);
	}
	.a {
		margin-top: 6px;
		display: flex;
		gap: 8px;
	}
</style>
