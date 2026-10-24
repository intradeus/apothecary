<script lang="ts">
	// A section heading outside a pane (DESIGN §7): serif title, a count, a
	// hairline to the right edge, and a link or actions at the end. Inside a
	// pane use .pane-head instead.
	import type { Snippet } from 'svelte';
	import { I } from './icons';

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
		/** Solid amber count: these need a human. */
		hot?: boolean;
		sub?: string;
		rule?: boolean;
		href?: string;
		linkLabel?: string;
		actions?: Snippet;
	} = $props();
</script>

<div class="sh">
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
		padding: 0 4px;
		min-width: 0;
	}
	h2 {
		white-space: nowrap;
	}
	.sub {
		font-size: var(--fs-help);
		color: var(--mut);
		white-space: nowrap;
	}
	.rule {
		height: 1px;
		background: var(--cLn);
		margin: 0 4px;
	}
	a {
		white-space: nowrap;
		color: var(--mut);
	}
	a:hover {
		color: var(--tx);
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
