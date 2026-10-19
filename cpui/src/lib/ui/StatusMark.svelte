<script lang="ts" module>
	export type MarkTone = 'ok' | 'bad' | 'warn' | 'info' | 'live' | 'pending' | 'mut';
</script>

<script lang="ts">
	// A round status glyph (timeline events, plan items, tool calls, check
	// steps): ✓ done, ✕ failed, ! warn, a pulsing dot while live, a hollow
	// ring while pending.
	import { I } from './icons';

	let {
		tone,
		glyph,
		size = 'md',
		title
	}: { tone: MarkTone; glyph?: string; size?: 'sm' | 'md'; title?: string } = $props();

	const g = $derived(
		glyph ??
			(
				{
					ok: I.ok,
					bad: I.bad,
					warn: I.warn,
					info: '',
					live: '',
					pending: '',
					mut: I.none
				} as const
			)[tone]
	);
</script>

<span class="sm-mark {tone} {size}" {title} role="img" aria-label={title ?? tone}>
	{#if tone === 'live'}<span class="ldot sm pulse"></span>{:else}{g}{/if}
</span>

<style>
	.sm-mark {
		flex: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 18px;
		height: 18px;
		border-radius: 50%;
		font-size: var(--fs-id);
		font-weight: 700;
		line-height: 1;
	}
	.sm {
		width: 15px;
		height: 15px;
		font-size: var(--fs-badge);
	}
	.ok {
		background: var(--grn);
		color: var(--deepInk);
	}
	.bad {
		background: var(--rosBg);
		color: var(--ros);
		border: 1px solid var(--rosLn);
	}
	.warn {
		background: var(--ambBg);
		color: var(--amb);
		border: 1px solid var(--ambLn);
	}
	.info {
		background: var(--azBg);
		border: 1px solid var(--azLn);
	}
	.live {
		color: var(--az);
		border: 1.5px solid var(--azLn);
	}
	.pending {
		border: 1.5px solid var(--ln2);
	}
	.mut {
		background: var(--p2);
		color: var(--mut);
	}
</style>
