<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// In-flight state for a live pane (DESIGN 4.17): a pulsing dot and a
	// muted line ("Connecting…", "Reading the flow…"). Never a blank pane.
	import type { Hue } from '$lib/design/hues';

	let { text, hue = 'running' }: { text?: string; hue?: Hue } = $props();
</script>

<div class="ld" role="status">
	<span class="live-dot sm pulse hue-{hue}"></span>{text ?? t('ui.loading')}
</div>

<style>
	.ld {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 16px;
		font-size: var(--fs-label);
		color: var(--text-muted);
	}
</style>
