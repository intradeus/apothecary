<script lang="ts">
	// InfoTip: a ⓘ beside a field's label that explains a rule worth knowing
	// but too long for the label — shown on hover, on focus, or on a tap,
	// hidden on leave, blur or Esc. The tip is the button's description
	// (aria-describedby), so a screen reader reads it with the button
	// whether it shows or not. It lives under <body>: a glass pane would
	// clip it.
	//
	//   <div class="label-row">
	//     <label for="x">Stop</label>
	//     <InfoTip label="What happens without a stop command">…</InfoTip>
	//   </div>
	//
	// Beside the label, never inside it: a button in a <label> is a second
	// control for it.
	import type { Snippet } from 'svelte';
	import { I } from '$lib/design/icons';
	import { portal } from './portal';

	let { label, children }: { label: string; children: Snippet } = $props();

	const uid = $props.id();
	const tipId = `${uid}-tip`;
	let btn = $state<HTMLButtonElement | null>(null);
	let open = $state(false);
	/** The tip's anchor: the middle of the button's top edge. */
	let at = $state({ left: 0, top: 0 });

	function show() {
		if (!btn) return;
		const r = btn.getBoundingClientRect();
		at = { left: r.left + r.width / 2, top: r.top };
		open = true;
	}
	const hide = () => (open = false);
</script>

<button
	bind:this={btn}
	type="button"
	class="info-tip"
	aria-label={label}
	aria-describedby={tipId}
	aria-expanded={open}
	onmouseenter={show}
	onmouseleave={hide}
	onfocus={show}
	onblur={hide}
	onclick={() => (open ? hide() : show())}
	onkeydown={(e) => {
		if (e.key === 'Escape' && open) {
			e.stopPropagation();
			hide();
		}
	}}>{I.info}</button
>
<div
	use:portal
	id={tipId}
	role="tooltip"
	class="tip island"
	hidden={!open}
	style:left="{at.left}px"
	style:top="{at.top}px"
>
	{@render children()}
</div>

<style>
	.info-tip {
		display: inline-grid;
		place-items: center;
		width: 16px;
		height: 16px;
		padding: 0;
		border: 0;
		background: none;
		color: var(--text-faint);
		font-size: var(--fs-label);
		line-height: 1;
		cursor: help;
	}
	.info-tip:hover,
	.info-tip:focus-visible,
	.info-tip[aria-expanded='true'] {
		color: var(--text);
	}
	.tip {
		position: fixed;
		z-index: var(--z-pop);
		max-width: min(320px, calc(100vw - 32px));
		padding: 8px 10px;
		border-radius: var(--r-sm);
		font-size: var(--fs-help);
		line-height: 1.5;
		color: var(--text);
		/* Above the button, centred on it, a few px clear. */
		transform: translate(-50%, calc(-100% - 6px));
		pointer-events: none;
	}
	.tip[hidden] {
		display: none;
	}
</style>
