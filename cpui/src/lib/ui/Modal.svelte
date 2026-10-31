<script lang="ts">
	// Modal (DESIGN 4.9): scrim + a 440px opaque island. Esc and the
	// scrim close it; focus is trapped inside while open.
	import type { Snippet } from 'svelte';
	import { portal } from './portal';
	import { trap } from './trap';
	import { materialize, scrim } from './motion';

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
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="scrim"
		use:portal
		transition:scrim
		onpointerdown={(e) => e.target === e.currentTarget && close()}
	>
		<div
			use:trap={{ onescape: close }}
			transition:materialize={{ y: 10 }}
			class="dlg island"
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
	/* Shared .scrim (components.css) plus centring. */
	.scrim {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
	}
	.dlg {
		border-radius: var(--r-modal);
		box-shadow: var(--specular), var(--shadow-modal);
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
		font-family: var(--font-body);
		font-style: normal;
		font-size: var(--fs-title);
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
		font-size: var(--fs-lead);
		font-weight: 700;
		background: var(--fill-2);
		color: var(--text);
	}
	.ic.danger {
		background: var(--danger-bg);
		color: var(--danger-text);
	}
	.ic.warn {
		background: var(--warning-bg);
		color: var(--warning-text);
	}
	.body {
		color: var(--text-muted);
		text-wrap: pretty;
		font-size: var(--fs-body);
	}
	.body :global(b),
	.body :global(strong) {
		color: var(--text);
	}
	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}
</style>
