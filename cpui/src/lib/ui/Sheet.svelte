<script lang="ts">
	// A sheet (drawer) sliding in from an edge over a scrim: the mobile
	// navigation drawer, side panels on small screens. It slides out along
	// the same edge it came from, and can be flicked away: it tracks the
	// finger 1:1, rubber-bands the wrong way, and on release projects the
	// flick's momentum to decide (ui/motion `dragDismiss`).
	import type { Snippet } from 'svelte';
	import { dragDismiss, scrim, slide } from './motion';
	import { trap } from './trap';

	let {
		open = $bindable(false),
		side = 'left',
		label,
		children
	}: {
		open: boolean;
		side?: 'left' | 'right' | 'bottom';
		label: string;
		children: Snippet;
	} = $props();

	const close = () => (open = false);
	const dir = $derived(side === 'bottom' ? 'down' : side);
</script>

{#if open}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="scrim sh-scrim"
		transition:scrim
		onpointerdown={(e) => e.target === e.currentTarget && close()}
	></div>
	<div
		class="sheet {side}"
		role="dialog"
		aria-modal="true"
		aria-label={label}
		transition:slide={{ from: side }}
		use:trap={{ onescape: close }}
		use:dragDismiss={{ direction: dir, ondismiss: close }}
	>
		{@render children()}
	</div>
{/if}

<style>
	.sh-scrim {
		z-index: var(--z-drawer);
	}
	.sheet {
		position: fixed;
		z-index: var(--z-drawer);
		display: flex;
		will-change: transform;
	}
	.left {
		left: 0;
		top: 0;
		bottom: 0;
		max-width: calc(100vw - 48px);
	}
	.right {
		right: 0;
		top: 0;
		bottom: 0;
		max-width: calc(100vw - 48px);
	}
	.bottom {
		left: 0;
		right: 0;
		bottom: 0;
		max-height: 85vh;
	}
</style>
