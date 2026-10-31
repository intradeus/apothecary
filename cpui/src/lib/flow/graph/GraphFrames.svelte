<script lang="ts">
	// The frames over the viewport, behind the nodes, and the corner button in
	// front of them that folds a chain to its marker. A child of <SvelteFlow>:
	// its portals read that context.
	import { ViewportPortal } from '@xyflow/svelte';
	import { I } from '$lib/design/icons';
	import type { Frame } from '$lib/flow/graph/frames';
	import { t } from '$lib/i18n/index.svelte';

	let { frames, onfold }: { frames: Frame[]; onfold: (id: string) => void } = $props();
</script>

<ViewportPortal target="back">
	{#each frames as f (f.id)}
		<div
			class="frame tone-{f.hue}"
			class:folded={f.folded}
			style="transform: translate({f.x}px, {f.y}px); width: {f.w}px; height: {f.h}px"
		></div>
	{/each}
</ViewportPortal>
<ViewportPortal target="front">
	{#each frames as f (f.id)}
		<button
			class="btn btn-icon fold tone-{f.hue}"
			style="transform: translate({f.x + f.w - 30}px, {f.y + 6}px)"
			title={t(f.folded ? 'flow.graph.unfold' : 'flow.graph.fold', { phase: f.id })}
			aria-label={t(f.folded ? 'flow.graph.unfold' : 'flow.graph.fold', { phase: f.id })}
			aria-expanded={!f.folded}
			onclick={() => onfold(f.id)}>{f.folded ? I.caret : I.menu}</button
		>
	{/each}
</ViewportPortal>

<style>
	/* A watcher's or an outcome's frame: a faint wash of its marker's hue
	 * behind its chain, and the corner button that folds it. */
	.frame {
		position: absolute;
		top: 0;
		left: 0;
		border-radius: var(--r-card);
		border: 1px dashed color-mix(in oklch, var(--tone) 30%, transparent);
		background: color-mix(in oklch, var(--tone) 5%, transparent);
		pointer-events: none;
	}
	.frame.folded {
		border-style: solid;
	}
	/* A .btn-icon (svelte-flow keeps its own button defaults, so .btn opts
	 * in), pinned to the band's corner and tinted in its hue. */
	.fold {
		position: absolute;
		top: 0;
		left: 0;
		width: 22px;
		height: 22px;
		color: var(--tone);
		font-size: var(--fs-badge);
		pointer-events: all;
	}
	.fold:hover {
		color: var(--tone);
		background: color-mix(in oklch, var(--tone) 14%, transparent);
	}
</style>
