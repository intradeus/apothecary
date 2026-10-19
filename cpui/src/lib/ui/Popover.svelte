<script lang="ts" module>
	// Open popovers, innermost last: Esc closes only the top one, so a menu
	// inside a sticky composer doesn't take the composer with it.
	const stack: symbol[] = [];
</script>

<script lang="ts">
	// Popover / menu (DESIGN 4.8). Positioned BESIDE its anchor, never on top
	// of it; closes on outside pointer-down or Esc. It materialises out of its
	// trigger (transform-origin on the anchor's side) and leaves the same way;
	// reopening mid-close reverses from wherever it is.
	//
	//   <button bind:this={btn} onclick={() => (open = !open)}>Env ▾</button>
	//   <Popover anchor={btn} bind:open placement="below-end">…</Popover>
	import type { Snippet } from 'svelte';
	import { materialize } from './motion';

	let {
		anchor,
		open = $bindable(false),
		placement = 'below-start',
		width,
		title,
		legend,
		children,
		/** Explicit fixed position, overriding the anchor (e.g. the guild switcher). */
		at,
		/** Stay open on outside clicks (a floating composer); Esc still closes. */
		sticky = false,
		onclose
	}: {
		anchor?: HTMLElement | null;
		open: boolean;
		placement?: 'below-start' | 'below-end' | 'right' | 'above-start' | 'above-end';
		width?: number;
		title?: string;
		legend?: string;
		children: Snippet;
		at?: { left?: number; top?: number; right?: number; bottom?: number };
		sticky?: boolean;
		onclose?: () => void;
	} = $props();

	// Grow from the side the trigger is on, travelling a few px away from it.
	const motion = $derived.by(() => {
		if (at) return { origin: 'left top', x: -6, y: 0 };
		return (
			{
				'below-start': { origin: 'left top', x: 0, y: -6 },
				'below-end': { origin: 'right top', x: 0, y: -6 },
				'above-start': { origin: 'left bottom', x: 0, y: 6 },
				'above-end': { origin: 'right bottom', x: 0, y: 6 },
				right: { origin: 'left top', x: -6, y: 0 }
			} as const
		)[placement];
	});

	function close() {
		open = false;
		onclose?.();
	}

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
		const down = (e: PointerEvent) => {
			const t = e.target as Node;
			if (sticky || el?.contains(t) || anchor?.contains(t)) return;
			close();
		};
		const me = Symbol('popover');
		stack.push(me);
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape' && stack.at(-1) === me && !e.defaultPrevented) {
				e.preventDefault();
				close();
				anchor?.focus();
			}
		};
		const reflow = () => place();
		window.addEventListener('pointerdown', down, true);
		window.addEventListener('keydown', key);
		window.addEventListener('resize', reflow);
		return () => {
			stack.splice(stack.indexOf(me), 1);
			window.removeEventListener('pointerdown', down, true);
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
		transition:materialize={motion}
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
		z-index: var(--z-pop);
		border-radius: var(--r-group);
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
		font-size: var(--fs-section);
	}
	.lg2 {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	/* Menu rows (.mi, .menu-div) are shared classes in components.css; the
	 * old .div divider name still works inside a popover. */
	.pop :global(.div) {
		height: 1px;
		background: var(--ln);
		margin: 4px 6px;
	}
</style>
