<script lang="ts">
	// Brewing: the wait, apothecary style. An Erlenmeyer flask whose liquid
	// sloshes and bubbles rise from, a test tube beside it, and a line saying
	// what is brewing. For any wait with nothing better to show; still under
	// prefers-reduced-motion.
	import { t } from '$lib/i18n/index.svelte';

	let {
		label,
		size = 'md'
	}: {
		/** What is brewing, for the person and their screen reader
		 *  (default "Brewing…"). */
		label?: string;
		/** sm 32px tall, md 56px, lg 88px. */
		size?: 'sm' | 'md' | 'lg';
	} = $props();
</script>

<span class="brewing {size}" role="status">
	<svg viewBox="0 0 48 40" aria-hidden="true">
		<g class="tube">
			<rect x="38" y="10" width="6" height="26" rx="3" class="glass" />
			<rect x="39" y="22" width="4" height="13" rx="2" class="liquid alt slosh" />
		</g>
		<g class="flask">
			<path class="liquid slosh" d="M9 26 L27 26 L32 35 Q33 37 31 37 L5 37 Q3 37 4 35 Z" />
			<circle class="bubble b1" cx="14" cy="31" r="1.6" />
			<circle class="bubble b2" cx="19" cy="33" r="1.2" />
			<circle class="bubble b3" cx="23" cy="30" r="1.4" />
			<path
				class="glass"
				d="M14 4 L22 4 M15 4 L15 15 L4 35 Q3 37 5 37 L31 37 Q33 37 32 35 L21 15 L21 4"
			/>
		</g>
	</svg>
	<span class="label">{label ?? t('ui.brewing')}</span>
</span>

<style>
	.brewing {
		display: inline-flex;
		flex-direction: column;
		align-items: center;
		gap: 8px;
		color: var(--text-muted);
	}
	svg {
		height: 56px;
		width: auto;
		overflow: visible;
	}
	.sm svg {
		height: 32px;
	}
	.lg svg {
		height: 88px;
	}
	.label {
		font-size: var(--fs-meta);
	}
	.glass {
		fill: none;
		stroke: var(--line-strong);
		stroke-width: 1.5;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.liquid {
		fill: color-mix(in oklch, var(--running) 75%, transparent);
	}
	.liquid.alt {
		fill: color-mix(in oklch, var(--attention) 75%, transparent);
	}
	.bubble {
		fill: var(--bubble);
		transform-box: fill-box;
		animation: ap-bubble var(--loop-bubble) steps(10, jump-none) infinite;
	}
	.b2 {
		animation-delay: calc(var(--loop-bubble) / -3);
	}
	.b3 {
		animation-delay: calc(var(--loop-bubble) / -1.5);
	}
	.slosh {
		transform-box: fill-box;
		animation: ap-slosh var(--loop-slosh) steps(6, jump-none) infinite;
	}
	.tube .slosh {
		animation-direction: reverse;
	}
	@media (prefers-reduced-motion: reduce) {
		.bubble,
		.slosh {
			animation: none;
		}
	}
</style>
