<script lang="ts">
	// Text that changes on a timer ("just now" → "3s ago") without nudging its
	// neighbours: every `reserve` string is laid out invisibly in the same grid
	// cell, so the box is as wide as the widest of them, and digits are tabular
	// so a ticking count keeps its width.
	let { text, reserve = [] }: { text: string; reserve?: string[] } = $props();
</script>

<span class="st"
	>{#each reserve as r, i (i)}<span class="sz" aria-hidden="true">{r}</span>{/each}<span
		>{text}</span
	></span
>

<style>
	.st {
		display: inline-grid;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.st > span {
		grid-area: 1 / 1;
	}
	.sz {
		visibility: hidden;
	}
</style>
