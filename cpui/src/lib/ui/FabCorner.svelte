<script lang="ts">
	// The corner: the bottom right of the viewport, over every page and every
	// window (a fullscreen one included), where the shell's buttons float — a
	// squircle each (.fab): the assistant on every page, in the corner itself;
	// above it, what a page adds (a run's Layout). Stacked upwards, so the
	// corner is one button wide whatever it holds: a run's window in the
	// corner steps aside by one button (--fab-clear), a bar at a page's foot
	// by --fab-clear-x. It lives at the app's root: a page's container query
	// would pin a fixed child to the page, and its stacking context would hold
	// it under the sidebar. On a phone it rides above the tab bar.
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="corner">
	{@render children()}
</div>

<style>
	.corner {
		position: fixed;
		right: var(--fab-inset);
		bottom: var(--fab-inset);
		z-index: var(--z-fab);
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: var(--fab-gap);
		/* The corner's own gaps let clicks through to the page. */
		pointer-events: none;
	}
	.corner > :global(*) {
		pointer-events: auto;
	}
	@media (max-width: 760px) {
		.corner {
			right: 8px;
			bottom: 76px;
		}
	}
</style>
