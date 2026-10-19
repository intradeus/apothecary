<script lang="ts">
	// A labelled cluster of controls on control glass (DESIGN 4.5).
	import type { Snippet } from 'svelte';

	let { label, children }: { label: Snippet | string; children: Snippet } = $props();
</script>

<div class="gg lg">
	<div class="lbl">
		{#if typeof label === 'string'}{label}{:else}{@render label()}{/if}
	</div>
	<div class="btns">{@render children()}</div>
</div>

<style>
	.gg {
		border-radius: 17px;
		padding: 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 7px;
		min-width: 0;
	}
	.lbl {
		font-size: 11px;
		font-weight: 500;
		color: var(--mut);
		display: flex;
		align-items: center;
		gap: 8px;
		white-space: nowrap;
	}
	.btns {
		display: flex;
		gap: 5px;
		align-items: center;
	}
</style>
