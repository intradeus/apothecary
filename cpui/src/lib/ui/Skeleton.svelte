<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Loading skeleton (DESIGN 4.17). Never a blank pane. `pane` wraps it in
	// the standard content-glass pane, which is how most pages load.
	let {
		lines = 3,
		height = 12,
		pane = false
	}: { lines?: number; height?: number; pane?: boolean } = $props();
</script>

{#snippet bars()}
	<div class="sk" aria-busy="true" aria-label={t('ui.loading.aria')}>
		{#each Array(lines) as _, i (i)}
			<span
				class="skel"
				style:height="{height}px"
				style:width="{i === lines - 1 ? 60 : 100 - i * 7}%"
			></span>
		{/each}
	</div>
{/snippet}

{#if pane}
	<div class="c pane"><div class="pane-body">{@render bars()}</div></div>
{:else}
	{@render bars()}
{/if}

<style>
	.sk {
		display: flex;
		flex-direction: column;
		gap: 9px;
		padding: 4px 0;
	}
</style>
