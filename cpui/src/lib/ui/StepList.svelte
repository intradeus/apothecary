<script lang="ts" module>
	import type { MarkTone } from './StatusMark.svelte';

	export interface Step {
		/** Stable identity (defaults to the label). */
		key?: string;
		tone: MarkTone;
		/** The step's name, mono ("image", "firewall"). */
		label: string;
		detail?: string;
		/** Show the detail as an error (rose, mono). */
		bad?: boolean;
	}
</script>

<script lang="ts">
	// A check's steps as a vertical timeline (DESIGN 7.7 envcheck, preflight):
	// a StatusMark per step joined by a hairline, the step's mono name, then
	// its detail. Rows materialise as they land, so a live check reads as
	// steps arriving one by one; the first batch staggers in. `live` adds a
	// trailing pulsing row for the step still running.
	import { onMount } from 'svelte';
	import StatusMark from './StatusMark.svelte';
	import { materialize } from './motion';

	let {
		steps,
		live,
		empty
	}: {
		steps: Step[];
		/** Label of the trailing in-progress row ("next step"); none when idle. */
		live?: { label: string; detail?: string } | null;
		/** Shown when there are no steps and nothing is live. */
		empty?: string;
	} = $props();

	// Rows rendered in the first frame stagger in; later rows arrive alone.
	let settled = false;
	onMount(() => {
		const r = requestAnimationFrame(() => (settled = true));
		return () => cancelAnimationFrame(r);
	});
	function arrive(node: Element, i: number) {
		const t = materialize(node, { origin: 'left center', x: -6 });
		return { ...t, delay: settled ? 0 : Math.min(i, 8) * 40 };
	}
</script>

<ol class="sl">
	{#each steps as s, i (s.key ?? s.label)}
		<li class="st" in:arrive|global={i}>
			<StatusMark tone={s.tone} title={s.tone} />
			<span class="mono id ell" class:fnt={s.tone === 'mut' || s.tone === 'pending'}>{s.label}</span
			>
			<span class="dt" class:bad={s.bad}>{s.detail ?? ''}</span>
		</li>
	{/each}
	{#if live}
		<li class="st" in:arrive|global={steps.length}>
			<StatusMark tone="live" title="running" />
			<span class="mono id fnt ell">{live.label}</span>
			<span class="dt fnt">{live.detail ?? ''}</span>
		</li>
	{/if}
	{#if !steps.length && !live && empty}
		<li class="st">
			<StatusMark tone="pending" title="none" />
			<span class="dt fnt span">{empty}</span>
		</li>
	{/if}
</ol>

<style>
	.sl {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.st {
		position: relative;
		display: grid;
		grid-template-columns: 18px var(--step-id, 120px) minmax(0, 1fr);
		gap: 10px;
		align-items: center;
		padding: 5px 0;
		font-size: var(--fs-label);
	}
	/* The hairline joining one mark to the next. */
	.st:not(:last-child)::before {
		content: '';
		position: absolute;
		left: 8.5px;
		top: calc(50% + 11px);
		bottom: calc(-50% + 11px);
		width: 1px;
		background: var(--ln2);
	}
	.id {
		font-size: var(--fs-help);
	}
	.dt {
		color: var(--mut);
		overflow-wrap: anywhere;
		min-width: 0;
	}
	.dt.bad {
		color: var(--ros);
		font-family: var(--mono);
		font-size: var(--fs-meta);
	}
	.span {
		grid-column: 2 / -1;
	}
</style>
