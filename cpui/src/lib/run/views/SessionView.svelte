<script lang="ts">
	// Session (DESIGN §6): the transcript, auto-following the bottom; the ping
	// card docked above the composer while the run waits on you; the composer
	// while the run is active, a closing note once it is finished.
	//
	// Following glides (a smooth scroll that retargets as text streams in);
	// scrolling up hands control to the reader and the "Follow" pill
	// materialises; sending snaps straight back to the bottom.
	import { t } from '$lib/i18n/index.svelte';
	import { tick } from 'svelte';
	import { buildTranscript } from '$lib/transcript';
	import { I } from '$lib/ui/icons';
	import { materialize } from '$lib/ui/motion';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Transcript, { type Sending } from '../Transcript.svelte';
	import PingDock from '../PingDock.svelte';
	import Composer from '../Composer.svelte';
	import type { RunStore } from '../store.svelte';

	let {
		rs,
		visible,
		onlog,
		onphases
	}: {
		rs: RunStore;
		visible: boolean;
		onlog: (stage: string) => void;
		/** Open the Phases window. */
		onphases: () => void;
	} = $props();

	const items = $derived(buildTranscript(rs.events, rs.live));
	const ping = $derived(rs.terminal ? undefined : rs.pendingPings[0]);
	const run = $derived(rs.run);
	// An agent stage in flight is what can be taken over.
	const canTakeOver = $derived(
		!!run &&
			run.state === 'running' &&
			!!rs.current &&
			Boolean(rs.current.payload.agent || rs.current.payload.harness)
	);
	// Between phases (idle, or parked at a wait point) a one-line nudge points
	// at the Phases window: what may start next lives there. Dismissed, it
	// stays away until another phase has ended.
	const phasesDone = $derived(rs.events.filter((e) => e.type === 'phase_completed').length);
	const showNudge = $derived(
		!!run && !rs.terminal && run.idle && rs.phasesNudgeDismissedAt !== phasesDone
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
		const timer = setTimeout(() => (settled = true), 700);
		return () => clearTimeout(timer);
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
						{t('runview.session.noconv_over')}
					{:else}
						<span class="ldot sm pulse hue-az"></span>{t('runview.session.noconv_pending')}
					{/if}
				</div>
			{/if}
			<Transcript {items} {sending} over={rs.terminal} {onlog} onanswer={() => follow()} />
			{#if showNudge}
				<div class="nudge" transition:materialize={{ origin: 'center bottom', y: 6 }}>
					<span class="mut">{t('runview.session.betweenPhases')}</span>
					<button class="linklike brand" onclick={onphases}
						>{t('runview.session.openPhases')} {I.forward}</button
					>
					<button
						class="btn-icon btn-sm"
						title={t('runview.session.dismissNudge')}
						aria-label={t('runview.session.dismissNudge')}
						onclick={() => (rs.phasesNudgeDismissedAt = phasesDone)}>{I.close}</button
					>
				</div>
			{/if}
		</div>
		{#if !stick}
			<div class="followbar">
				<button
					class="bead follow"
					transition:materialize={{ origin: 'center bottom', y: 8 }}
					onclick={() => follow()}>{I.follow} {t('runview.session.follow')}</button
				>
			</div>
		{/if}
	</div>

	{#if ping}
		<PingDock
			{ping}
			queued={rs.pendingPings.length - 1}
			busy={rs.answering}
			onanswer={(a, note, files, content) => rs.answer(ping.id, a, note, files, content)}
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
			{canTakeOver}
			hold={rs.run.hold}
			releasing={rs.releasing}
			now={rs.now}
			ontakeover={() => rs.takeOver()}
			onrelease={() => rs.release()}
		/>
	{:else if rs.run}
		<div class="closed">
			<EmptyState
				compact
				inline
				title={t('runview.session.closed_title')}
				text={rs.hasWorkspace
					? t('runview.session.closed_text_ws')
					: t('runview.session.closed_text')}
			/>
		</div>
	{/if}
</div>

<style>
	.nudge {
		display: flex;
		align-items: center;
		gap: 10px;
		margin: 10px 0 4px;
		padding: 6px 8px 6px 12px;
		border: 1px solid var(--ln);
		border-radius: var(--r-pill);
		background: var(--cBg);
		font-size: var(--fs-ui);
		width: fit-content;
		max-width: 100%;
	}
	.nudge .mut {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
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
	/* The selected look (.bead), floating over the transcript. */
	.follow {
		pointer-events: auto;
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh),
			var(--floatSh);
	}
	.closed {
		flex: none;
		margin: 0 14px 14px;
	}
</style>
