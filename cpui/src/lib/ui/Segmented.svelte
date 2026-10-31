<script lang="ts" module>
	import type { Hue } from './hues';

	export interface SegOption<V extends string> {
		value: V;
		label: string;
		count?: number | string;
		title?: string;
		/** A small dot in this hue before the label (type filters). */
		dot?: Hue;
		/** Tint the label in this hue while selected (allow / block). */
		tone?: Hue;
	}
</script>

<script lang="ts" generics="T extends string">
	// Segmented control (DESIGN 4.4): filters and pickers. One bead springs
	// between the options instead of the selection jumping (ui/motion
	// `indicator`); clicking again mid-slide retargets it from where it is.
	// A radio group to assistive tech: one stop on Tab, ← → Home End move
	// the choice. A page's sections are Tabs, not this.
	import { indicator } from './motion';

	let {
		options,
		value = $bindable(),
		onchange,
		/** On window glass (default) or flat inside a pane. */
		glass = true,
		size = 'md',
		/** Mono labels (repo names, ids). */
		mono = false,
		/** Not interactive right now (a change is saving); dims. */
		disabled = false,
		label
	}: {
		options: SegOption<T>[];
		value: T;
		onchange?: (v: T) => void;
		glass?: boolean;
		size?: 'sm' | 'md';
		mono?: boolean;
		disabled?: boolean;
		/** Accessible name for the group. */
		label?: string;
	} = $props();

	function choose(v: T) {
		value = v;
		onchange?.(v);
	}

	function keys(e: KeyboardEvent) {
		const at = options.findIndex((o) => o.value === value);
		const n = options.length;
		const to =
			e.key === 'ArrowRight' || e.key === 'ArrowDown'
				? (at + 1) % n
				: e.key === 'ArrowLeft' || e.key === 'ArrowUp'
					? (at - 1 + n) % n
					: e.key === 'Home'
						? 0
						: e.key === 'End'
							? n - 1
							: -1;
		if (to < 0 || disabled) return;
		e.preventDefault();
		choose(options[to].value);
		(e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="radio"]')[to]?.focus();
	}
</script>

<div
	class="seg {size}"
	class:c={glass}
	class:flat={!glass}
	class:mono
	class:off={disabled}
	aria-disabled={disabled || undefined}
	role="radiogroup"
	tabindex="-1"
	aria-label={label}
	use:indicator
	onkeydown={keys}
>
	{#each options as o (o.value)}
		<button
			type="button"
			role="radio"
			aria-checked={value === o.value}
			tabindex={value === o.value ? 0 : -1}
			{disabled}
			class:on={value === o.value}
			style:color={value === o.value && o.tone ? `var(--${o.tone})` : null}
			title={o.title}
			onclick={() => choose(o.value)}
			>{#if o.dot}<span class="ldot sm flat hue-{o.dot}"
				></span>{/if}{o.label}{#if o.count !== undefined}<span class="n">{o.count}</span
				>{/if}</button
		>
	{/each}
</div>

<style>
	.flat {
		background: var(--p1);
		border: 1px solid var(--ln);
	}
	.sm > button {
		font-size: var(--fs-help);
		padding: 3px 9px;
		border-radius: var(--r-sm);
	}
	.sm > :global(.ind) {
		border-radius: var(--r-sm);
	}
	.off {
		opacity: var(--o-dim);
		pointer-events: none;
	}
	.mono > button {
		font-family: var(--mono);
		font-size: var(--fs-help);
	}
</style>
