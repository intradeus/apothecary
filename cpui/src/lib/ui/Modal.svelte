<script lang="ts">
	// Modal (DESIGN 4.9): scrim + 440px control-glass dialog. Esc and the
	// scrim close it; focus is trapped inside while open.
	import type { Snippet } from 'svelte';
	import { trap } from './trap';

	let {
		open = $bindable(false),
		title,
		icon,
		tone = 'neutral',
		width = 440,
		children,
		footer,
		onclose
	}: {
		open: boolean;
		title: string;
		/** A glyph in the 34px round header icon. */
		icon?: string;
		tone?: 'neutral' | 'danger' | 'warn';
		width?: number;
		children: Snippet;
		footer?: Snippet;
		onclose?: () => void;
	} = $props();

	function close() {
		open = false;
		onclose?.();
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="scrim" onmousedown={(e) => e.target === e.currentTarget && close()}>
		<div
			use:trap={{ onescape: close }}
			class="dlg lg"
			role="dialog"
			aria-modal="true"
			aria-label={title}
			style:width="min({width}px, calc(100vw - 32px))"
		>
			<div class="head">
				{#if icon}<span class="ic {tone}">{icon}</span>{/if}
				<h2>{title}</h2>
			</div>
			<div class="body">{@render children()}</div>
			{#if footer}<div class="foot">{@render footer()}</div>{/if}
		</div>
	</div>
{/if}

<style>
	.scrim {
		position: fixed;
		inset: 0;
		z-index: 50;
		background: var(--scrim);
		backdrop-filter: blur(6px);
		-webkit-backdrop-filter: blur(6px);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
	}
	.dlg {
		background: var(--lgBg), var(--thick);
		border-radius: 26px;
		box-shadow:
			var(--lgRim),
			0 30px 80px rgba(0, 0, 0, 0.5);
		padding: 20px 22px 18px;
		display: flex;
		flex-direction: column;
		gap: 14px;
		max-height: calc(100vh - 32px);
		overflow: auto;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 12px;
	}
	.head h2 {
		font-family: var(--sans);
		font-style: normal;
		font-size: 16px;
		font-weight: 600;
	}
	.ic {
		flex: none;
		width: 34px;
		height: 34px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 15px;
		font-weight: 700;
		background: var(--p2);
		color: var(--tx);
	}
	.ic.danger {
		background: var(--rosBg);
		color: var(--ros);
	}
	.ic.warn {
		background: var(--ambBg);
		color: var(--amb);
	}
	.body {
		color: var(--mut);
		text-wrap: pretty;
		font-size: 13px;
	}
	.body :global(b),
	.body :global(strong) {
		color: var(--tx);
	}
	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}
</style>
