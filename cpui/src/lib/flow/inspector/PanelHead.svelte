<script lang="ts">
	// A panel's head: what it shows (`lead`), its name, and on the far side
	// whatever counts it or removes it (`children`).
	import type { Snippet } from 'svelte';

	let { lead, id, children }: { lead: Snippet; id?: string; children?: Snippet } = $props();
</script>

<div class="ih">
	{@render lead()}
	{#if id !== undefined}<span class="mono iid truncate">{id}</span>{/if}
	<span class="spacer"></span>
	{@render children?.()}
</div>

<style>
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--line);
		min-width: 0;
	}
	.iid {
		font-size: var(--fs-body);
		font-weight: 600;
	}
</style>
