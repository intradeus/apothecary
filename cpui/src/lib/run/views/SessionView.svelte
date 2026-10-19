<script lang="ts">
	// Session (DESIGN §6): the transcript, auto-following the bottom; the ping
	// card docked above the composer while the run waits on you; the composer
	// while the run is active, a closing note once it is finished.
	//
	// Following glides (a smooth scroll that retargets as text streams in);
	// scrolling up hands control to the reader and the "Follow" pill
	// materialises; sending snaps straight back to the bottom.
	import { tick } from 'svelte';
	import { buildTranscript } from '$lib/transcript';
	import { I } from '$lib/ui/icons';
	import { materialize } from '$lib/ui/motion';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Transcript, { type Sending } from '../Transcript.svelte';
	import PingDock from '../PingDock.svelte';
	import Composer from '../Composer.svelte';
	import ReviewCard from '../ReviewCard.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs, visible, onlog }: { rs: RunStore; visible: boolean; onlog: (stage: string) => void } =
		$props();

	const items = $derived(buildTranscript(rs.events, rs.live));
	const ping = $derived(rs.terminal ? undefined : rs.pendingPings[0]);
	const run = $derived(rs.run);
	const inReview = $derived(
		!!run && !rs.terminal && run.mrs.length > 0 && (run.idle || !!run.phase)
	);

	let scroller = $state<HTMLDivElement | null>(null);
	let stick = $state(true);
	let lastTop = 0;
	let sending = $state<Sending[]>([]);

	const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

	function toBottom(smooth: boolean) {
		const el = scroller;
		if (!el) return;
		el.scrollTo({ top: el.scrollHeight, behavior: smooth && !reduced() ? 'smooth' : 'instant' });
	}
	function onScroll() {
		const el = scroller;
		if (!el) return;
		// Near the bottom: following. Moving UP: the reader took over. (A
		// smooth follow only ever moves down, so it never unsticks itself.)
		if (el.scrollHeight - el.scrollTop - el.clientHeight < 60) stick = true;
		else if (el.scrollTop < lastTop - 2) stick = false;
		lastTop = el.scrollTop;
	}
	function follow(smooth = true) {
		stick = true;
		tick().then(() => toBottom(smooth));
	}
	// The history (and the replay burst behind it) jumps into place; only
	// what arrives after the list went quiet glides.
	let settled = false;
	$effect(() => {
		void items.length;
		if (settled) return;
		const t = setTimeout(() => (settled = true), 700);
		return () => clearTimeout(t);
	});
	// Follow while the reader is at the bottom; leave them alone once they
	// scroll up to read. Re-pin (instantly) when the view comes back on screen.
	$effect(() => {
		void items.length;
		void rs.live;
		void ping?.id;
		if (stick && scroller) tick().then(() => toBottom(settled));
	});
	$effect(() => {
		if (visible && stick) tick().then(() => toBottom(false));
	});
	// A sent bubble stays until the run echoes the message back.
	$effect(() => {
		const echoed = new Set(items.flatMap((it) => (it.kind === 'user' ? [it.text] : [])));
		if (sending.some((s) => echoed.has(s.text)))
			sending = sending.filter((s) => !echoed.has(s.text));
	});
</script>

<div class="session">
	<div class="sw">
		<div class="scroll" bind:this={scroller} onscroll={onScroll}>
			{#if items.length === 0}
				<div class="noconv">
					{#if rs.terminal}
						No conversation was recorded for this run.
					{:else}
						<span class="ldot sm pulse hue-az"></span>No conversation yet. When an agent stage
						starts, its prompt, replies and tool calls appear here as they happen.
					{/if}
				</div>
			{/if}
			<Transcript {items} {sending} over={rs.terminal} {onlog} onanswer={() => follow()} />
			{#if inReview && run}
				{#key run.idle + (run.phase ?? '')}<ReviewCard runId={rs.runId} idle={run.idle} />{/key}
			{/if}
		</div>
		{#if !stick}
			<div class="followbar">
				<button
					class="follow"
					transition:materialize={{ origin: 'center bottom', y: 8 }}
					onclick={() => follow()}>{I.follow} Follow</button
				>
			</div>
		{/if}
	</div>

	{#if ping}
		<PingDock
			{ping}
			queued={rs.pendingPings.length - 1}
			busy={rs.answering}
			onanswer={(a, note, files) => rs.answer(ping.id, a, note, files)}
		/>
	{/if}
	{#if rs.run && !rs.terminal}
		<Composer
			runId={rs.runId}
			onsent={(text, interrupt) => {
				sending = [...sending, { text, interrupt }];
				follow(false);
			}}
			onfailed={(text) => (sending = sending.filter((s) => s.text !== text))}
		/>
	{:else if rs.run}
		<div class="closed">
			<EmptyState
				compact
				inline
				title="The bottle is corked"
				text="The run is finished.{rs.hasWorkspace
					? ' The workspace stays up until the reaper removes it.'
					: ''}"
			/>
		</div>
	{/if}
</div>

<style>
	.session {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
		position: relative;
		container-type: inline-size;
	}
	.sw {
		flex: 1;
		min-height: 0;
		position: relative;
		display: flex;
		flex-direction: column;
	}
	.scroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 16px 20px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.noconv {
		color: var(--mut);
		font-size: var(--fs-ui);
		padding: 20px 4px;
		max-width: var(--measure-short);
	}
	.noconv .ldot {
		margin-right: 8px;
		vertical-align: 1px;
	}
	/* Centres the pill without a transform, so its own transform is free for
	 * the materialise. */
	.followbar {
		position: absolute;
		left: 0;
		right: 0;
		bottom: 12px;
		z-index: var(--z-float);
		display: flex;
		justify-content: center;
		pointer-events: none;
	}
	.follow {
		pointer-events: auto;
		background: var(--sel);
		border: 1px solid var(--selLn);
		box-shadow: var(--selSh), var(--floatSh);
		font-weight: 600;
	}
	.closed {
		flex: none;
		margin: 0 14px 14px;
	}
</style>
