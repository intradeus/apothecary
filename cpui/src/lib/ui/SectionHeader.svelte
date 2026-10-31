<script lang="ts">
	// A section heading outside a pane (DESIGN §7): display title, a count, a
	// hairline to the right edge, and a link or actions at the end. Inside a
	// pane use .pane-head instead.
	import type { Snippet } from 'svelte';
	import { I } from '$lib/design/icons';

	let {
		title,
		count,
		hot = false,
		sub,
		rule = true,
		href,
		linkLabel = 'Open',
		actions
	}: {
		title: string;
		count?: number | string;
		/** Solid sky count: these need a human. */
		hot?: boolean;
		sub?: string;
		rule?: boolean;
		href?: string;
		linkLabel?: string;
		actions?: Snippet;
	} = $props();
</script>

<div class="sh glass">
	<h2>{title}</h2>
	{#if count !== undefined}<span class="count" class:hot>{count}</span>{/if}
	{#if sub}<span class="sub">{sub}</span>{/if}
	<span class="grow" class:rule></span>
	{#if actions}{@render actions()}{/if}
	{#if href}<a class="small" {href}>{linkLabel} {I.forward}</a>{/if}
</div>

<style>
	.sh {
		display: flex;
		align-items: center;
		gap: 10px;
		flex: none;
		min-height: 38px;
		padding: 5px 8px 5px 14px;
		border-radius: var(--r-btn);
		min-width: 0;
	}
	/* Inside a window it is only a heading. */
	:global(.glass) .sh {
		min-height: 0;
		padding: 0 4px;
		background: none;
		border: 0;
	}
	h2 {
		white-space: nowrap;
	}
	.sub {
		font-size: var(--fs-help);
		color: var(--text-muted);
		white-space: nowrap;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.rule {
		height: 1px;
		background: var(--glass-line);
		margin: 0 4px;
	}
	a {
		white-space: nowrap;
		color: var(--text-muted);
	}
	a:hover {
		color: var(--text);
	}
	/* A phone: the line wraps — title and actions first, the description
	   under them in full — instead of pushing the actions off the edge. */
	@media (max-width: 760px) {
		.sh {
			flex-wrap: wrap;
			row-gap: 4px;
		}
		.sub {
			order: 9;
			flex: 1 1 100%;
			white-space: normal;
		}
		.rule {
			background: none;
		}
	}
</style>
