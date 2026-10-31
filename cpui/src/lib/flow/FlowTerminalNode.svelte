<script lang="ts">
	// The flow's terminals (DESIGN 7.8): display pills on window glass.
	//  - start (brand, solid): the main chain's beginning — what every run
	//    starts with — naming what runs are started on; its handle drags onto
	//    a stage to start the chain there. Each watcher draws its own
	//    beginning (data.entry false), violet and dashed, naming its signal:
	//    one beginning per chain, one chain per beginning. An outcome's
	//    (data.outcome) wears the end it says (data.then): green for done,
	//    rose for abort — it fires once and ends the run.
	//  - end (tea, dashed): a chain is over and the run goes back to waiting —
	//    where most chains close, a watcher's always.
	//  - done (grn, dashed) and abort (ros, dashed): where the RUN ends.
	//  - debug (sky, dashed): the stop that comes back — a human fixes the
	//    workspace, then the stage that sent the run here runs again. Drawn
	//    once per stage that escalates to it, beside that stage, so its source
	//    handle carries the return edge.
	//  - exit (grn) and failexit (ros), in a sequence's view only: the ways
	//    out of the sequence, back to the stage that runs it — its success,
	//    and `fail` (its on_fail). data.label names where each one goes.
	// A watcher's or an outcome's marker branches from the hub (data.hub): it
	// takes the hub's edge on its left, which nothing else may connect to.
	// One component for all of them; the node type picks the tone.
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import type { Hue } from '$lib/design/hues';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';

	let { type, data }: NodeProps = $props();

	type Tone =
		'start' | 'trigger' | 'outcome' | 'end' | 'done' | 'abort' | 'debug' | 'exit' | 'failexit';
	const tone = $derived<Tone>(
		type === 'start'
			? data?.entry
				? 'start'
				: data?.outcome
					? 'outcome'
					: 'trigger'
			: type === 'abort' || type === 'debug' || type === 'end' || type === 'exit'
				? type
				: type === 'failexit'
					? 'failexit'
					: 'done'
	);
	const HUE: Record<Tone, Hue> = {
		get outcome(): Hue {
			return data?.then === 'abort' ? 'danger' : 'success';
		},
		start: 'brand',
		trigger: 'accent',
		end: 'accent-2',
		done: 'success',
		abort: 'danger',
		debug: 'attention',
		exit: 'success',
		failexit: 'danger'
	};
	const GLYPH: Record<Tone, string> = {
		start: I.play,
		trigger: I.play,
		outcome: I.play,
		end: I.loop,
		done: I.ok,
		abort: I.bad,
		debug: I.retry,
		exit: I.forward,
		failexit: I.forward
	};
	const TITLE: Record<Tone, () => string> = {
		start: () => t('flow.term.start'),
		trigger: () => '',
		outcome: () => '',
		end: () => t('flow.term.end'),
		done: () => t('flow.term.done'),
		abort: () => t('flow.term.abort'),
		debug: () => t('flow.term.debug'),
		exit: () => t('flow.term.exit'),
		failexit: () => t('flow.term.failexit')
	};
	const marker = $derived(tone === 'start' || tone === 'trigger' || tone === 'outcome');
	const label = $derived((data?.label as string | undefined) ?? (marker ? 'start' : tone));
	const title = $derived((data?.title as string | undefined) ?? TITLE[tone]());
	const handle = '--xy-handle-background-color: var(--tone)';
</script>

<div class="term {tone} tone-{HUE[tone]}" {title}>
	{#if !marker}
		<Handle type="target" position={Position.Left} style={handle} />
	{:else if data?.hub}
		<Handle id="hub" type="target" position={Position.Left} style={handle} isConnectable={false} />
	{/if}
	<span class="l">{label}</span><span class="g">{GLYPH[tone]}</span>
	{#if marker || tone === 'debug'}
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
		background: var(--glass-bg);
		backdrop-filter: var(--glass-filter);
		-webkit-backdrop-filter: var(--glass-filter);
		border: 1.5px dashed color-mix(in oklch, var(--tone) 38%, transparent);
		color: var(--tone);
		white-space: nowrap;
	}
	/* The entry is the one solid, glowing terminal. */
	.start {
		border: 1px solid color-mix(in oklch, var(--tone) 38%, transparent);
		box-shadow: 0 0 var(--glow-md) color-mix(in oklch, var(--tone) 14%, transparent);
	}
	.l {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-title);
		line-height: 1.1;
	}
	.trigger .l,
	.outcome .l {
		font-size: var(--fs-aside);
	}
	.g {
		font-size: var(--fs-badge);
		font-weight: 700;
	}
</style>
