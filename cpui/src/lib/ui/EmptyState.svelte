<script lang="ts">
	// Empty state (DESIGN 4.17): dashed box, serif title, one line, one action.
	import type { Snippet } from 'svelte';

	let {
		title,
		text,
		children,
		compact = false
	}: { title: string; text?: string; children?: Snippet; compact?: boolean } = $props();
</script>

<div class="es" class:compact>
	<div class="t">{title}</div>
	{#if text}<div class="x">{text}</div>{/if}
	{#if children}<div class="a">{@render children()}</div>{/if}
</div>

<style>
	.es {
		border: 1.5px dashed var(--ln2);
		border-radius: 16px;
		padding: 34px 20px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 8px;
		text-align: center;
	}
	.es.compact {
		padding: 18px 14px;
	}
	.t {
		font-family: var(--serif);
		font-style: italic;
		font-size: 24px;
		line-height: 1.15;
	}
	.compact .t {
		font-size: 20px;
	}
	.x {
		color: var(--mut);
		max-width: 40ch;
		font-size: 12.5px;
	}
	.a {
		margin-top: 6px;
		display: flex;
		gap: 8px;
	}
</style>
