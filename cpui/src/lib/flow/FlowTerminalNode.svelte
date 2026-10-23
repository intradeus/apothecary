<script lang="ts">
	// The flow's terminals (DESIGN 7.8): serif italic pills on content glass.
	//  - start (brand, solid): the entry's beginning — the phase every run
	//    begins with — naming what begins it; its handle drags onto a stage
	//    to make that stage the entry. Each reaction phase draws its own
	//    beginning (data.entry false), violet and dashed, naming its signal:
	//    one beginning per phase, one phase per beginning.
	//  - end (tea, dashed): a phase is over and the run goes back to waiting —
	//    where most chains close, a reaction's always.
	//  - done (grn, dashed) and abort (ros, dashed): where the RUN ends.
	//  - debug (amb, dashed): the stop that comes back — a human fixes the
	//    workspace, then the stage that sent the run here runs again. Drawn
	//    once per stage that escalates to it, beside that stage, so its source
	//    handle carries the return edge.
	// One component for all of them; the node type picks the tone.
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import type { Hue } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { t } from '$lib/i18n/index.svelte';

	let { type, data }: NodeProps = $props();

	type Tone = 'start' | 'trigger' | 'end' | 'done' | 'abort' | 'debug';
	const tone = $derived<Tone>(
		type === 'start'
			? data?.entry
				? 'start'
				: 'trigger'
			: type === 'abort'
				? 'abort'
				: type === 'debug'
					? 'debug'
					: type === 'end'
						? 'end'
						: 'done'
	);
	const HUE: Record<Tone, Hue> = {
		start: 'brand',
		trigger: 'vio',
		end: 'tea',
		done: 'grn',
		abort: 'ros',
		debug: 'amb'
	};
	const GLYPH: Record<Tone, string> = {
		start: I.play,
		trigger: I.play,
		end: I.loop,
		done: I.ok,
		abort: I.bad,
		debug: I.retry
	};
	const TITLE: Record<Tone, () => string> = {
		start: () => t('flow.term.start'),
		trigger: () => '',
		end: () => t('flow.term.end'),
		done: () => t('flow.term.done'),
		abort: () => t('flow.term.abort'),
		debug: () => t('flow.term.debug')
	};
	const label = $derived(
		tone === 'start' || tone === 'trigger' ? ((data?.label as string | undefined) ?? 'start') : tone
	);
	const title = $derived((data?.title as string | undefined) ?? TITLE[tone]());
	const handle = '--xy-handle-background-color: var(--h)';
</script>

<div class="term {tone} h-{HUE[tone]}" {title}>
	{#if tone === 'end' || tone === 'done' || tone === 'abort' || tone === 'debug'}
		<Handle type="target" position={Position.Left} style={handle} />
	{/if}
	<span class="l">{label}</span><span class="g">{GLYPH[tone]}</span>
	{#if tone === 'start' || tone === 'trigger' || tone === 'debug'}
		<Handle type="source" position={Position.Right} style={handle} />
	{/if}
</div>

<style>
	.term {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		padding: 4px 14px 5px;
		border-radius: var(--r-pill);
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
		border: 1.5px dashed color-mix(in oklch, var(--h) 38%, transparent);
		color: var(--h);
		white-space: nowrap;
	}
	/* The entry is the one solid, glowing terminal. */
	.start {
		border: 1px solid color-mix(in oklch, var(--h) 38%, transparent);
		box-shadow: 0 0 var(--glow-md) color-mix(in oklch, var(--h) 14%, transparent);
	}
	.l {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-title);
		line-height: 1.1;
	}
	.trigger .l {
		font-size: var(--fs-aside);
	}
	.g {
		font-size: var(--fs-badge);
		font-weight: 700;
	}
</style>
