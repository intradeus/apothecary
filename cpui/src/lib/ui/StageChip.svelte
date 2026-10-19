<script lang="ts">
	// Stage type chip (DESIGN 3.3): a flat tinted pill, hue from hues.ts.
	import { STAGE_HUE, STAGE_WORD, hueOf } from './hues';

	let { type }: { type: string } = $props();
</script>

<span class="pill sm flat sq t-{hueOf(STAGE_HUE, type)}">{STAGE_WORD[type] ?? type}</span>
