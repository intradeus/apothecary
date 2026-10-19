<script lang="ts">
	// Workspace/project glyph (DESIGN 2.6). An idle workshop is an empty vial.
	import { workshopVial } from '$lib/guild';

	let { projectId, idle = false }: { projectId: string; idle?: boolean } = $props();
	const v = $derived(workshopVial(projectId));
</script>

<span class="vial" class:idle style:--h={v.hue} style:--f="{100 - v.fill}%" aria-hidden="true"
></span>

<style>
	.vial {
		flex: none;
		display: inline-block;
		width: 10px;
		height: 13px;
		border-radius: 2px 2px 5px 5px;
		border: 1px solid color-mix(in oklch, var(--h) 60%, transparent);
		background: linear-gradient(transparent var(--f), var(--h) var(--f));
		box-shadow: 0 0 8px color-mix(in oklch, var(--h) 35%, transparent);
	}
	.vial.idle {
		border-color: rgba(255, 255, 255, 0.2);
		background: transparent;
		box-shadow: none;
	}
</style>
