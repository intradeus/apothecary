<script lang="ts">
	// Workspace/project glyph (DESIGN 2.6). An idle workshop is an empty vial.
	import { workshopVial } from '$lib/core/urls';

	let { projectId, idle = false }: { projectId: string; idle?: boolean } = $props();
	const v = $derived(workshopVial(projectId));
</script>

<span class="vial" class:idle style:--tone={v.hue} style:--f="{100 - v.fill}%" aria-hidden="true"
></span>

<style>
	.vial {
		flex: none;
		display: inline-block;
		width: 10px;
		height: 13px;
		border-radius: var(--r-vial);
		border: 1px solid color-mix(in oklch, var(--tone) 60%, transparent);
		background: linear-gradient(transparent var(--f), var(--tone) var(--f));
		box-shadow: 0 0 8px color-mix(in oklch, var(--tone) 35%, transparent);
	}
	/* Empty, but still this workshop's: its colour, faint, and a trace at the
	   bottom. A grey outline alone read as a broken image at header size. */
	.vial.idle {
		border-color: color-mix(in oklch, var(--tone) 40%, var(--vial-idle-line));
		background: linear-gradient(
			transparent 78%,
			color-mix(in oklch, var(--tone) 30%, transparent) 78%
		);
		box-shadow: none;
	}
</style>
