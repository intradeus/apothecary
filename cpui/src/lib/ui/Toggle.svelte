<script lang="ts">
	// Toggle (DESIGN 4.14): 28×16 track, green when on. It feels physical:
	// the knob stretches toward its destination on press (like iOS) and
	// springs across on release; flipping it again mid-travel retargets from
	// where it is.
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
		font-size: var(--fs-label);
		color: var(--tx);
		-webkit-tap-highlight-color: transparent;
	}
	.tg:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: 2px;
		border-radius: var(--r-tag);
	}
	.tg:disabled {
		opacity: var(--o-disabled);
		cursor: default;
	}
	.track {
		position: relative;
		flex: none;
		width: 28px;
		height: 16px;
		border-radius: var(--r-pill);
		background: var(--p2);
		border: 1px solid var(--ln2);
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
	}
	.track.on {
		background: var(--grn);
		border-color: transparent;
	}
	/* Positioned by transform (compositor-only), sized by width; both ride
	 * the spring. Pressed, the knob stretches toward the far side. */
	.knob {
		position: absolute;
		top: 1px;
		left: 1px;
		width: 12px;
		height: 12px;
		border-radius: var(--r-pill);
		background: var(--knob);
		box-shadow: var(--knobSh);
		transition:
			transform var(--dur-spring) var(--spring),
			width var(--dur-spring) var(--spring);
	}
	.on .knob {
		transform: translateX(12px);
	}
	.tg:active:not(:disabled) .knob {
		width: 15px;
		transition-duration: var(--dur-press);
	}
	.tg:active:not(:disabled) .on .knob {
		transform: translateX(9px);
	}
	.l {
		white-space: nowrap;
	}
</style>
