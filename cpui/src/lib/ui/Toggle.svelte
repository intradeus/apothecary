<script lang="ts">
	// Toggle (DESIGN 4.14): 28×16 track, green when on.
	let {
		checked = $bindable(false),
		label,
		disabled = false,
		title,
		onchange
	}: {
		checked: boolean;
		label?: string;
		disabled?: boolean;
		title?: string;
		onchange?: (v: boolean) => void;
	} = $props();
</script>

<button
	type="button"
	class="tg"
	role="switch"
	aria-checked={checked}
	{disabled}
	{title}
	onclick={() => {
		checked = !checked;
		onchange?.(checked);
	}}
>
	<span class="track" class:on={checked}><span class="knob"></span></span>
	{#if label}<span class="l">{label}</span>{/if}
</button>

<style>
	.tg {
		all: unset;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--tx);
	}
	.tg:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: 2px;
		border-radius: 6px;
	}
	.tg:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.track {
		position: relative;
		flex: none;
		width: 28px;
		height: 16px;
		border-radius: 9px;
		background: var(--p2);
		border: 1px solid var(--ln2);
	}
	.track.on {
		background: var(--grn);
		border-color: transparent;
	}
	.knob {
		position: absolute;
		top: 1px;
		left: 1px;
		width: 12px;
		height: 12px;
		border-radius: 50%;
		background: #fff;
		box-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
	}
	.on .knob {
		left: 13px;
	}
	.l {
		white-space: nowrap;
	}
</style>
