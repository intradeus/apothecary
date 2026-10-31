<script lang="ts">
	// Checkbox (DESIGN 4.14): a 16px box that fills green with a drawn tick
	// when checked, the tick springing in. The real <input> sits underneath,
	// so the keyboard, forms and screen readers get a native checkbox; the
	// whole row (box and label) is the hit area. For an on/off setting that
	// takes effect at once, use Toggle; for a set of choices, CheckboxGroup.
	//
	//   <Checkbox bind:checked={keep} label="Keep the workspace" />
	import type { Snippet } from 'svelte';

	let {
		checked = $bindable(false),
		indeterminate = false,
		disabled = false,
		label,
		detail,
		children,
		id,
		name,
		value,
		title,
		/** sm: a toolbar's option, in quieter text. */
		size = 'md',
		onchange
	}: {
		checked?: boolean;
		/** Some but not all (a "select all" over a list). */
		indeterminate?: boolean;
		disabled?: boolean;
		label?: string;
		/** A quieter second part after the label. */
		detail?: string;
		/** Richer label content, in place of `label`. */
		children?: Snippet;
		id?: string;
		name?: string;
		value?: string;
		title?: string;
		size?: 'sm' | 'md';
		onchange?: (checked: boolean) => void;
	} = $props();
</script>

<label class="ck" class:sm={size === 'sm'} class:off={disabled} {title}>
	<input
		type="checkbox"
		{id}
		{name}
		{value}
		{disabled}
		{indeterminate}
		bind:checked
		onchange={() => onchange?.(checked)}
	/>
	<span
		class="box"
		class:on={checked || indeterminate}
		class:mixed={indeterminate}
		aria-hidden="true"
	></span>
	{#if label || children}
		<span class="txt">
			{#if children}{@render children()}{:else}{label}{/if}{#if detail}<span class="muted"
					>{' — '}{detail}</span
				>{/if}
		</span>
	{/if}
</label>

<style>
	.ck {
		position: relative;
		display: inline-flex;
		align-items: flex-start;
		gap: 8px;
		margin: 0;
		font-size: var(--fs-body);
		font-weight: 400;
		color: var(--text);
		cursor: pointer;
		-webkit-tap-highlight-color: transparent;
	}
	.sm {
		font-size: var(--fs-help);
		color: var(--text-muted);
		align-items: center;
	}
	.sm .box,
	.sm input {
		margin-top: 0;
	}
	.off {
		opacity: var(--o-disabled);
		cursor: default;
	}
	/* The native input keeps focus and state; the box draws them. */
	input {
		position: absolute;
		opacity: 0;
		width: 16px;
		height: 16px;
		margin: 2px 0 0;
		pointer-events: none;
	}
	.box {
		position: relative;
		flex: none;
		width: 16px;
		height: 16px;
		margin-top: 2px;
		border-radius: var(--r-xs);
		background: var(--fill-1);
		border: 1px solid var(--line-strong);
		box-shadow: var(--specular);
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
	}
	.ck:hover .box:not(.on) {
		border-color: var(--success-line);
	}
	.box.on {
		background: var(--success);
		border-color: transparent;
	}
	/* The tick: two borders of a rotated box, scaled in on the spring. */
	.box::after {
		content: '';
		position: absolute;
		left: 4.5px;
		top: 1.5px;
		width: 4px;
		height: 8px;
		border: solid var(--on-success);
		border-width: 0 2px 2px 0;
		transform: rotate(45deg) scale(0);
		transition: transform var(--dur-spring) var(--spring);
	}
	.box.on::after {
		transform: rotate(45deg) scale(1);
	}
	/* Some but not all: a dash. */
	.box.mixed::after {
		left: 3px;
		top: 6px;
		width: 8px;
		height: 0;
		border-width: 0 0 2px;
		transform: none;
	}
	input:focus-visible + .box {
		outline: 2px solid var(--brand);
		outline-offset: 2px;
	}
	.ck:active:not(.off) .box {
		transform: scale(var(--press-scale));
		transition-duration: var(--dur-press);
	}
	.txt {
		min-width: 0;
		line-height: 1.45;
	}
</style>
