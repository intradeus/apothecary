<script lang="ts">
	// The flow's terminals (DESIGN 7.8): serif italic pills on content glass.
	//  - start (brand, solid): the entry marker; its handle drags onto a stage
	//    to make that stage the entry. A reaction phase draws one per phase,
	//    violet and dashed, naming the signal that starts it.
	//  - done (grn, dashed) and abort (ros, dashed): where runs end.
	//  - debug (amb, dashed): the stop that comes back — a human fixes the
	//    workspace, then the stage that sent the run here runs again.
	// One component for all of them; the node type picks the tone.
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import type { Hue } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';

	let { type, data }: NodeProps = $props();

	type Tone = 'start' | 'trigger' | 'done' | 'abort' | 'debug';
	const tone = $derived<Tone>(
		type === 'start'
			? data?.label
				? 'trigger'
				: 'start'
			: type === 'abort'
				? 'abort'
				: type === 'debug'
					? 'debug'
					: 'done'
	);
	const HUE: Record<Tone, Hue> = {
		start: 'brand',
		trigger: 'vio',
		done: 'grn',
		abort: 'ros',
		debug: 'amb'
	};
	const GLYPH: Record<Tone, string> = {
		start: I.play,
		trigger: I.play,
		done: I.ok,
		abort: I.bad,
		debug: I.retry
	};
	const TITLE: Record<Tone, string> = {
		start: 'The entry — drag this handle onto a stage to start the flow there',
		trigger: '',
		done: 'The run completes',
		abort: 'The run stops deliberately',
		debug:
			'A stop, not an end: the run parks for a human to fix the workspace, then goes back into the stage that sent it here'
	};
	const label = $derived(
		tone === 'start' || tone === 'trigger' ? ((data?.label as string | undefined) ?? 'start') : tone
	);
	const title = $derived((data?.title as string | undefined) ?? TITLE[tone]);
	const handle = '--xy-handle-background-color: var(--h)';
</script>

<div class="term {tone} h-{HUE[tone]}" {title}>
	{#if tone === 'done' || tone === 'abort' || tone === 'debug'}
		<Handle type="target" position={Position.Left} style={handle} />
	{/if}
	<span class="l">{label}</span><span class="g">{GLYPH[tone]}</span>
	{#if tone === 'start' || tone === 'trigger'}
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
