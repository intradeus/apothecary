<script lang="ts">
	// Popover / menu (DESIGN 4.8). Positioned BESIDE its anchor, never on top
	// of it; closes on outside mousedown or Esc.
	//
	//   <button bind:this={btn} onclick={() => (open = !open)}>Env ▾</button>
	//   <Popover anchor={btn} bind:open placement="below-end">…</Popover>
	import type { Snippet } from 'svelte';

	let {
		anchor,
		open = $bindable(false),
		placement = 'below-start',
		width,
		title,
		legend,
		children,
		/** Explicit fixed position, overriding the anchor (e.g. the guild switcher). */
		at
	}: {
		anchor?: HTMLElement | null;
		open: boolean;
		placement?: 'below-start' | 'below-end' | 'right' | 'above-start' | 'above-end';
		width?: number;
		title?: string;
		legend?: string;
		children: Snippet;
		at?: { left?: number; top?: number; right?: number; bottom?: number };
	} = $props();

	let el = $state<HTMLDivElement | null>(null);
	let pos = $state<{ left?: number; top?: number; right?: number; bottom?: number }>({});

	function place() {
		if (at) {
			pos = at;
			return;
		}
		if (!anchor) return;
		const r = anchor.getBoundingClientRect();
		const vw = window.innerWidth;
		const vh = window.innerHeight;
		const gap = 6;
		switch (placement) {
			case 'below-start':
				pos = { left: Math.max(8, r.left), top: r.bottom + gap };
				break;
			case 'below-end':
				pos = { right: Math.max(8, vw - r.right), top: r.bottom + gap };
				break;
			case 'above-start':
				pos = { left: Math.max(8, r.left), bottom: vh - r.top + gap };
				break;
			case 'above-end':
				pos = { right: Math.max(8, vw - r.right), bottom: vh - r.top + gap };
				break;
			case 'right':
				pos = { left: r.right + 10, top: Math.max(8, r.top) };
				break;
		}
	}

	$effect(() => {
		if (!open) return;
		place();
		const down = (e: MouseEvent) => {
			const t = e.target as Node;
			if (el?.contains(t) || anchor?.contains(t)) return;
			open = false;
		};
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape') {
				open = false;
				anchor?.focus();
			}
		};
		const reflow = () => place();
		window.addEventListener('mousedown', down, true);
		window.addEventListener('keydown', key);
		window.addEventListener('resize', reflow);
		return () => {
			window.removeEventListener('mousedown', down, true);
			window.removeEventListener('keydown', key);
			window.removeEventListener('resize', reflow);
		};
	});
</script>

{#if open}
	<div
		bind:this={el}
		class="pop lg-pop"
		role="dialog"
		style:left={pos.left !== undefined ? `${pos.left}px` : null}
		style:top={pos.top !== undefined ? `${pos.top}px` : null}
		style:right={pos.right !== undefined ? `${pos.right}px` : null}
		style:bottom={pos.bottom !== undefined ? `${pos.bottom}px` : null}
		style:width={width ? `${width}px` : null}
	>
		{#if title}
			<div class="ph">
				<span class="t">{title}</span>
				{#if legend}<span class="lg2">{legend}</span>{/if}
			</div>
		{/if}
		{@render children()}
	</div>
{/if}

<style>
	.pop {
		position: fixed;
		z-index: 30;
		border-radius: 17px;
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
		max-height: calc(100vh - 24px);
		overflow: auto;
		min-width: 180px;
	}
	.ph {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 10px;
		padding: 6px 8px 8px;
	}
	.t {
		font-family: var(--serif);
		font-size: 19px;
	}
	.lg2 {
		font-size: 11px;
		color: var(--mut);
	}
	/* Menu rows: <button class="mi"> / <a class="mi"> inside a popover. */
	.pop :global(.mi) {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: 9px;
		padding: 7px 9px;
		border-radius: 10px;
		font-size: 12.5px;
		color: var(--tx);
		white-space: nowrap;
	}
	.pop :global(.mi:hover) {
		background: var(--p3);
		text-decoration: none;
	}
	.pop :global(.mi.on) {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
		font-weight: 600;
	}
	.pop :global(.mi .sub) {
		color: var(--mut);
		font-size: 11px;
	}
	.pop :global(.div) {
		height: 1px;
		background: var(--ln);
		margin: 4px 6px;
	}
</style>
