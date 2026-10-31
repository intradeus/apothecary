<script lang="ts" module>
	export type MarkTone = 'ok' | 'bad' | 'warn' | 'info' | 'live' | 'pending' | 'muted';
</script>

<script lang="ts">
	// A round status glyph (timeline events, plan items, tool calls, check
	// steps): ✓ done, ✕ failed, ! warn, a pulsing dot while live, a hollow
	// ring while pending.
	import { I } from '$lib/design/icons';

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
					muted: I.none
				} as const
			)[tone]
	);
</script>

<span class="sm-mark {tone} {size}" {title} role="img" aria-label={title ?? tone}>
	{#if tone === 'live'}<span class="live-dot sm pulse"></span>{:else}{g}{/if}
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
		background: var(--success);
		color: var(--on-success);
	}
	.bad {
		background: var(--danger-bg);
		color: var(--danger-text);
		border: 1px solid var(--danger-line);
	}
	.warn {
		background: var(--warning-bg);
		color: var(--warning-text);
		border: 1px solid var(--warning-line);
	}
	.info {
		background: var(--running-bg);
		border: 1px solid var(--running-line);
	}
	.live {
		color: var(--running-text);
		border: 1.5px solid var(--running-line);
	}
	.pending {
		border: 1.5px solid var(--line-strong);
	}
	.muted {
		background: var(--fill-2);
		color: var(--text-muted);
	}
</style>
