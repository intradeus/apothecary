<script lang="ts">
	// The review workbench's frame (DESIGN3 §3), shared by the review being
	// written and the one submitted: the files and the open one's outline on
	// the left, the file large in the middle, the comments on the right. It
	// answers to its own width, not the window's — the same frame sits full
	// page in the inbox and inside a run's pings view. Narrower, the file
	// list folds into a picker above the file (each piece hides itself by
	// the `review` container); narrowest, the comments stack below.
	//
	// With onstep, j/k (and ] [) move between files while the focus is not
	// in a text field.
	import type { Snippet } from 'svelte';

	let {
		nav,
		main,
		side,
		onstep,
		embedded = false
	}: {
		nav: Snippet;
		main: Snippet;
		side: Snippet;
		onstep?: (d: number) => void;
		/** Inside another view: a fixed height, no page gutters. */
		embedded?: boolean;
	} = $props();

	function onkey(e: KeyboardEvent) {
		if (!onstep || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
		const el = e.target instanceof Element ? e.target : null;
		if (el?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])'))
			return;
		const d = e.key === 'j' || e.key === ']' ? 1 : e.key === 'k' || e.key === '[' ? -1 : 0;
		if (!d) return;
		e.preventDefault();
		onstep(d);
	}
</script>

<svelte:window onkeydown={onkey} />

<div class="rv" class:embedded>
	<div class="wb">
		<div class="nav c pane">{@render nav()}</div>
		<section class="main c pane">{@render main()}</section>
		<aside class="side lg pane">{@render side()}</aside>
	</div>
</div>

<style>
	.rv {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
		container: review / inline-size;
	}
	.wb {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr) var(--aside-w);
		gap: 10px;
		padding: 2px 22px 16px;
	}
	.embedded {
		flex: none;
	}
	.embedded .wb {
		flex: none;
		height: min(72vh, 680px);
		padding: 0;
	}
	.nav,
	.main,
	.side {
		min-height: 0;
		display: flex;
		flex-direction: column;
	}

	@container review (max-width: 1100px) {
		.wb {
			grid-template-columns: minmax(0, 1fr) var(--aside-w);
		}
		.nav {
			display: none;
		}
	}
	@container review (max-width: 700px) {
		/* Stacked: each pane as tall as what it holds; the frame scrolls. */
		.wb {
			grid-template-columns: minmax(0, 1fr);
			grid-auto-rows: max-content;
			align-content: start;
			overflow: auto;
			padding: 2px 16px 16px;
		}
		.embedded .wb {
			height: auto;
			overflow: visible;
			padding: 0;
		}
		.main,
		.side {
			overflow: visible;
		}
	}
</style>
