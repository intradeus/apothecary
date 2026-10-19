<script lang="ts">
	// The repo picker of the workspace views (DESIGN §6): one chip per
	// checkout of a multi-repo run, the primary starred, plus a hint.
	import type { Snippet } from 'svelte';
	import type { RunCheckout } from '$lib/domain';

	let {
		checkouts,
		value = $bindable(''),
		hint,
		children
	}: {
		checkouts: RunCheckout[];
		/** '' = the primary. */
		value?: string;
		hint?: string;
		children?: Snippet;
	} = $props();
</script>

<div class="rb">
	{#if checkouts.length > 1}
		<span class="lbl">Repo</span>
		{#each checkouts as c (c.name)}
			{@const v = c.primary ? '' : c.name}
			<button class="rchip" class:on={value === v} title={c.url} onclick={() => (value = v)}
				>{c.name}{#if c.primary}<span class="star">★</span>{/if}</button
			>
		{/each}
	{/if}
	{#if children}{@render children()}{/if}
	{#if hint}<span class="hint">{hint}</span>{/if}
</div>

<style>
	.rb {
		flex: none;
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 8px 12px;
		font-size: 12px;
		min-width: 0;
		flex-wrap: wrap;
	}
	.lbl {
		color: var(--fnt);
		font-size: 11.5px;
	}
	.rchip {
		padding: 1px 9px;
		border-radius: 8px;
		background: transparent;
		border: 1px solid transparent;
		box-shadow: none;
		font-family: var(--mono);
		font-size: 11px;
		color: var(--mut);
		gap: 4px;
	}
	.rchip:hover {
		background: var(--p2);
	}
	.rchip.on {
		background: var(--p3);
		border-color: var(--ln2);
		color: var(--tx);
	}
	.star {
		color: var(--amb);
	}
	.hint {
		margin-left: auto;
		color: var(--fnt);
		font-size: 11px;
		white-space: nowrap;
	}
</style>
