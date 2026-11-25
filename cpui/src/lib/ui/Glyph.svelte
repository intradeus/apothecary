<script lang="ts">
	// An interface icon ($lib/design/glyphs): a stroked SVG at the text's size
	// (1em by default) in the text's colour, decorative — the control it sits
	// in carries the name.
	import { GLYPHS, type GlyphName } from '$lib/design/glyphs';

	let { name, size = '1em' }: { name: GlyphName; size?: string } = $props();
</script>

<svg
	class="glyph"
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	stroke="currentColor"
	stroke-width="2"
	stroke-linecap="round"
	stroke-linejoin="round"
	aria-hidden="true"
	focusable="false"
	data-glyph={name}
>
	{#each GLYPHS[name] as [tag, attrs], i (i)}<svelte:element this={tag} {...attrs} />{/each}
</svg>

<style>
	.glyph {
		flex: none;
		display: inline-block;
		vertical-align: -0.125em;
	}
</style>
