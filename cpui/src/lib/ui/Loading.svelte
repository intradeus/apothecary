<script lang="ts">
	// In-flight state for a live pane (DESIGN 4.17): a pulsing dot and a
	// muted line ("Connecting…", "Reading the flow…"). Never a blank pane.
	import type { Hue } from './hues';

	let { text = 'Loading…', hue = 'az' }: { text?: string; hue?: Hue } = $props();
</script>

<div class="ld" role="status"><span class="ldot sm pulse hue-{hue}"></span>{text}</div>

<style>
	.ld {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 16px;
		font-size: var(--fs-label);
		color: var(--mut);
	}
</style>
