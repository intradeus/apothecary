<script lang="ts">
	// A group inside a FormPane's body, ruled off from what comes before it
	// (always a direct child of the pane's stack, so the first one needs no
	// rule). What a section puts in it is that section's markup, hence the
	// child combinators: its own heading and head row, nothing deeper, so the
	// rows inside an editor placed here keep their spacing.
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="sub">{@render children()}</div>

<style>
	.sub {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 14px;
		border-top: 1px solid var(--glass-line);
	}
	.sub:first-child {
		border-top: 0;
		padding-top: 0;
	}
	.sub > :global(h3),
	.sub > :global(.row) > :global(h3) {
		font-size: var(--fs-ui);
	}
	.sub > :global(.row) {
		gap: 8px;
	}
</style>
