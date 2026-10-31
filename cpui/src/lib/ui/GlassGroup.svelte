<script lang="ts">
	// A labelled cluster of controls, inset in the top bar (DESIGN 4.5a).
	import type { Snippet } from 'svelte';

	let { label, children }: { label: Snippet | string; children: Snippet } = $props();
</script>

<div class="gg inset">
	<div class="caption">
		{#if typeof label === 'string'}{label}{:else}{@render label()}{/if}
	</div>
	<div class="btns">{@render children()}</div>
</div>

<style>
	.gg {
		border-radius: var(--r-btn);
		padding: 7px 10px 8px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.caption {
		font-size: var(--fs-help);
		color: var(--text-muted);
		display: flex;
		align-items: center;
		gap: 7px;
		white-space: nowrap;
		min-width: 0;
	}
	.btns {
		display: flex;
		gap: 5px;
		align-items: center;
	}
</style>
