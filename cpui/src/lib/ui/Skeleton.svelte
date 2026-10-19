<script lang="ts">
	// Loading skeleton (DESIGN 4.17). Never a blank pane.
	let { lines = 3, height = 12 }: { lines?: number; height?: number } = $props();
</script>

<div class="sk" aria-busy="true" aria-label="Loading">
	{#each Array(lines) as _, i (i)}
		<span class="skel" style:height="{height}px" style:width="{i === lines - 1 ? 60 : 100 - i * 7}%"
		></span>
	{/each}
</div>

<style>
	.sk {
		display: flex;
		flex-direction: column;
		gap: 9px;
		padding: 4px 0;
	}
</style>
