<script lang="ts">
	// A captioned group in a panel: its label, then what it says tight under
	// it.
	import type { Snippet } from 'svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';

	let { caption, children }: { caption: string; children?: Snippet } = $props();
</script>

<div class="grp">
	<Caption text={caption} />
	{@render children?.()}
</div>

<style>
	.grp {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin-top: 4px;
	}
</style>
