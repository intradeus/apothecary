<script lang="ts">
	// The repo picker of the workspace views (DESIGN §6): one option per
	// checkout of a multi-repo run, the primary starred, plus a hint.
	import type { Snippet } from 'svelte';
	import type { RunCheckout } from '$lib/domain';
	import Segmented from '$lib/ui/Segmented.svelte';
	import { I } from '$lib/ui/icons';

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

	const options = $derived(
		checkouts.map((c) => ({
			value: c.primary ? '' : c.name,
			label: c.primary ? `${c.name} ${I.star}` : c.name,
			title: c.primary ? `${c.url} (primary)` : c.url
		}))
	);
</script>

<div class="vbar">
	{#if checkouts.length > 1}
		<span class="lbl">Repo</span>
		<Segmented mono glass={false} size="sm" label="Repository" {options} bind:value />
	{/if}
	{#if children}{@render children()}{/if}
	{#if hint}<span class="hint">{hint}</span>{/if}
</div>
