<script lang="ts">
	// The Layout button (DESIGN 6): a squircle in the corner (FabCorner),
	// above the assistant's, over every window, a fullscreen one included.
	// Shown while a desk is on screen; a phone's desk has one window and
	// nothing to arrange.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { activeDesk } from './active.svelte';
	import LayoutsPopover from './LayoutsPopover.svelte';

	const desk = $derived(activeDesk.desk?.compact ? null : activeDesk.desk);
	let btn = $state<HTMLButtonElement | null>(null);
</script>

{#if desk}
	<button
		bind:this={btn}
		class="fab"
		title={t('run.header.layouts')}
		aria-label={t('run.header.layouts')}
		aria-expanded={desk.menuOpen}
		onclick={() => (desk.menuOpen = !desk.menuOpen)}
	>
		{I.layout}<span class="key">L</span>
	</button>
	<LayoutsPopover {desk} anchor={btn} />
{/if}

<style>
	.key {
		position: absolute;
		right: -5px;
		bottom: -5px;
		padding: 0 4px;
		border-radius: var(--r-tag);
		background: var(--brand);
		color: var(--on-brand);
		font-family: var(--font-code);
		font-size: var(--fs-badge);
		font-weight: 700;
		line-height: 1.5;
	}
</style>
