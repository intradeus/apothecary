<script lang="ts">
	// A glass note strip inside a page (informational, not an alert): "this
	// flow is a built-in", "pings are always answered here". For problems that
	// need action use Banner.
	import type { Snippet } from 'svelte';
	import type { Hue } from './hues';
	import { I } from './icons';

	let {
		hue = 'brand',
		glyph = I.info,
		ondismiss,
		children
	}: { hue?: Hue; glyph?: string; ondismiss?: () => void; children: Snippet } = $props();
</script>

<div class="co lg h-{hue}">
	<span class="g">{glyph}</span>
	<div class="grow">{@render children()}</div>
	{#if ondismiss}<button class="btn-ghost btn-sm" onclick={ondismiss}>Dismiss</button>{/if}
</div>

<style>
	.co {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px 10px 8px 12px;
		border-radius: var(--r-btn);
		font-size: var(--fs-label);
		color: var(--mut);
		background:
			linear-gradient(90deg, color-mix(in oklch, var(--h) 10%, transparent), transparent 60%),
			var(--lgBg);
	}
	.g {
		color: var(--h);
		font-weight: 700;
	}
	.co :global(b),
	.co :global(strong) {
		color: var(--tx);
	}
</style>
