<script lang="ts">
	// The Layout button (DESIGN 6): a sky tile hovering at the bottom right of
	// the run page, over every window, a fullscreen one included. It lives at
	// the app's root rather than on the run page: that page's container query
	// would pin a fixed child to itself, and its stacking context would hold it
	// under the sidebar's. Shown while a desk is on screen; a phone's desk has
	// one window and nothing to arrange.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';
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
	.fab {
		position: fixed;
		right: calc(var(--shell-gap) + 10px);
		bottom: calc(var(--shell-gap) + 10px);
		z-index: var(--z-fab);
		width: 40px;
		height: 40px;
		padding: 0;
		border-radius: var(--r-row);
		/* Over content, so opaque under the tint. */
		background: linear-gradient(var(--brandBg), var(--brandBg)), var(--solid);
		border: 1px solid var(--brandLn);
		box-shadow: var(--spec), var(--floatSh);
		font-size: var(--fs-section);
		font-weight: 400;
		color: var(--brandLt);
	}
	.fab:hover {
		background: linear-gradient(var(--p3), var(--p3)), var(--solid);
		box-shadow:
			var(--spec),
			var(--floatSh),
			0 0 var(--glow-md) var(--brandGlow);
	}
	.key {
		position: absolute;
		right: -5px;
		bottom: -5px;
		padding: 0 4px;
		border-radius: var(--r-tag);
		background: var(--brand);
		color: var(--onBrand);
		font-family: var(--mono);
		font-size: var(--fs-badge);
		font-weight: 700;
		line-height: 1.5;
	}
</style>
