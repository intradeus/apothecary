<script lang="ts" generics="T">
	// Radio (DESIGN 4.14): one of a set — a 16px ring whose green dot springs
	// in when it is the group's value. The real <input type="radio"> sits
	// underneath (arrow keys move within its `name`). Usually drawn by
	// RadioGroup; alone, bind the group's value:
	//
	//   <Radio bind:group={db} value="pg" name="db" label="Postgres" />
	import type { Snippet } from 'svelte';

	let {
		group = $bindable(),
		value,
		name,
		disabled = false,
		label,
		detail,
		children,
		id,
		title,
		onchange
	}: {
		/** The set's current value: this radio is on when it equals `value`. */
		group: T | undefined;
		value: T;
		name?: string;
		disabled?: boolean;
		label?: string;
		/** A quieter second part after the label. */
		detail?: string;
		children?: Snippet;
		id?: string;
		title?: string;
		onchange?: (value: T) => void;
	} = $props();

	const on = $derived(group === value);
</script>

<label class="rd" class:off={disabled} {title}>
	<input
		type="radio"
		{id}
		{name}
		{disabled}
		checked={on}
		onchange={() => {
			group = value;
			onchange?.(value);
		}}
	/>
	<span class="ring" class:on aria-hidden="true"></span>
	{#if label || children}
		<span class="txt">
			{#if children}{@render children()}{:else}{label}{/if}{#if detail}<span class="muted"
					>{' — '}{detail}</span
				>{/if}
		</span>
	{/if}
</label>

<style>
	.rd {
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
	.off {
		opacity: var(--o-disabled);
		cursor: default;
	}
	input {
		position: absolute;
		opacity: 0;
		width: 16px;
		height: 16px;
		margin: 2px 0 0;
		pointer-events: none;
	}
	.ring {
		position: relative;
		flex: none;
		width: 16px;
		height: 16px;
		margin-top: 2px;
		border-radius: var(--r-pill);
		background: var(--fill-1);
		border: 1px solid var(--line-strong);
		box-shadow: var(--specular);
		transition: border-color var(--dur-fade) var(--ease-out);
	}
	.rd:hover .ring:not(.on) {
		border-color: var(--success-line);
	}
	.ring.on {
		border-color: var(--success);
	}
	.ring::after {
		content: '';
		position: absolute;
		inset: 3px;
		border-radius: var(--r-pill);
		background: var(--success);
		transform: scale(0);
		transition: transform var(--dur-spring) var(--spring);
	}
	.ring.on::after {
		transform: scale(1);
	}
	input:focus-visible + .ring {
		outline: 2px solid var(--brand);
		outline-offset: 2px;
	}
	.rd:active:not(.off) .ring {
		transform: scale(var(--press-scale));
		transition-duration: var(--dur-press);
	}
	.txt {
		min-width: 0;
		line-height: 1.45;
	}
</style>
