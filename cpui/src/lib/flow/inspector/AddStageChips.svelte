<script lang="ts">
	// The stages a chain or a sequence may add, one chip each in its kind's
	// hue.
	import { STAGE_HUE, STAGE_WORD } from '$lib/design/hues';
	import type { StageKind } from '$lib/domain/flowdoc';

	let {
		kinds,
		onadd
	}: {
		kinds: StageKind[];
		onadd: (kind: StageKind) => void;
	} = $props();
</script>

<div class="chips">
	{#each kinds as kind (kind)}
		<button class="btn-sm" onclick={() => onadd(kind)}
			><span class="live-dot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
				kind}</button
		>
	{/each}
</div>
