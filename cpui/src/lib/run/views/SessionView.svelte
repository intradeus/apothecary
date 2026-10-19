<script lang="ts">
	// Session (DESIGN §6): the transcript, auto-following the bottom; the ping
	// card docked above the composer while the run waits on you; the composer
	// while the run is active, a closing note once it is finished.
	import { tick } from 'svelte';
	import { buildTranscript } from '$lib/transcript';
	import Transcript from '../Transcript.svelte';
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

	function onScroll() {
		if (!scroller) return;
		stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 60;
	}
	function follow() {
		stick = true;
		tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight));
	}
	// Follow while the reader is at the bottom; leave them alone once they
	// scroll up to read. Also re-pin when the view comes back on screen.
	$effect(() => {
		void items.length;
		void rs.live;
		void visible;
		void ping?.id;
		if (stick && scroller)
			tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight));
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
						<span class="d pulse"></span>No conversation yet. When an agent stage starts, its
						prompt, replies and tool calls appear here as they happen.
					{/if}
				</div>
			{/if}
			<Transcript {items} over={rs.terminal} {onlog} onanswer={follow} />
			{#if inReview && run}
				{#key run.idle + (run.phase ?? '')}<ReviewCard runId={rs.runId} idle={run.idle} />{/key}
			{/if}
		</div>
		{#if !stick}
			<button class="follow" onclick={follow}>↓ Follow</button>
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
		<Composer runId={rs.runId} onsent={follow} />
	{:else if rs.run}
		<div class="closed">
			The run is finished, so messaging is closed.{#if rs.hasWorkspace}
				The workspace stays up until the reaper removes it.{/if}
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
		font-size: 12.5px;
		padding: 20px 4px;
		max-width: 60ch;
	}
	.d {
		display: inline-block;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
		margin-right: 8px;
		vertical-align: 1px;
	}
	.follow {
		position: absolute;
		left: 50%;
		transform: translateX(-50%);
		bottom: 12px;
		z-index: 3;
		background: var(--sel);
		border: 1px solid var(--selLn);
		box-shadow:
			var(--selSh),
			0 8px 24px rgba(0, 0, 0, 0.35);
		font-weight: 600;
	}
	.closed {
		flex: none;
		margin: 0 14px 14px;
		padding: 11px 14px;
		border-radius: 10px;
		border: 1px dashed var(--ln2);
		color: var(--mut);
		font-size: 12.5px;
	}
</style>
