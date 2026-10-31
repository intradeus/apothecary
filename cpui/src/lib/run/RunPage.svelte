<script lang="ts">
	// The live run (DESIGN 7.1): header, an alert banner when something needs
	// a decision, and the window desk with the run's fifteen views.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import { shortId, times } from '$lib/core/format';
	import Banner from '$lib/ui/Banner.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Desk, { type ViewMeta } from '$lib/desk/Desk.svelte';
	import { DeskState } from '$lib/desk/desk.svelte';
	import { activeDesk } from '$lib/desk/active.svelte';
	import { NEUTRAL_MIX, noise } from '$lib/ui/noise.svelte';
	import type { ViewId } from '$lib/desk/tree';
	import RunHeader from './RunHeader.svelte';
	import NoWorkspace from './NoWorkspace.svelte';
	import FlowView from './views/FlowView.svelte';
	import SessionView from './views/SessionView.svelte';
	import AskView from './views/AskView.svelte';
	import PingsView from './views/PingsView.svelte';
	import PhasesView from './views/PhasesView.svelte';
	import TimelineView from './views/TimelineView.svelte';
	import RecordView from './views/RecordView.svelte';
	import WorkspaceView from './views/WorkspaceView.svelte';
	import LogsView from './views/LogsView.svelte';
	import EgressView from './views/EgressView.svelte';
	import PortsView from './views/PortsView.svelte';
	import ServicesView from './views/ServicesView.svelte';
	import VitalsView from './views/VitalsView.svelte';
	import { TRANSCRIPT_TYPES } from './derive';
	import { RunStore } from './store.svelte';

	let { runId, onretried }: { runId: string; onretried: () => void } = $props();

	// svelte-ignore state_referenced_locally
	const rs = new RunStore(runId);
	const desk = new DeskState();

	// A phone (or any page this narrow) gets the focus layout — one window,
	// every view a tab — and the page itself scrolls: the header scrolls away
	// and the window keeps a full screen of height.
	let pageW = $state(0);
	let pageH = $state(0);
	const compact = $derived(pageW > 0 && pageW <= 760);
	$effect(() => desk.setCompact(compact));
	onMount(() => rs.start());

	// The sidebar's Layout button drives this desk while the page is here.
	$effect(() => {
		activeDesk.desk = desk;
		return () => {
			activeDesk.desk = null;
		};
	});

	// The backdrop follows the run: ember while it brews, sky while it waits
	// on you, neutral otherwise (DESIGN 2.2).
	$effect(() => {
		const s = rs.run?.state;
		noise.blue =
			s === 'running' || s === 'provisioning' ? 20 : s === 'waiting_on_human' ? 80 : NEUTRAL_MIX;
		return () => {
			noise.blue = NEUTRAL_MIX;
		};
	});

	let unknownDismissed = $state(false);

	const timelineCount = $derived(rs.events.filter((e) => !TRANSCRIPT_TYPES.has(e.type)).length);
	const fileCount = $derived(rs.snapshots.length + rs.attachments.length);

	function meta(v: ViewId): ViewMeta {
		switch (v) {
			case 'flow':
				return {
					hint: rs.run
						? `${rs.run.flowName}${rs.run.flowHash ? ` @${rs.run.flowHash.slice(0, 6)}` : ''}`
						: ''
				};
			case 'session':
				return { hint: t(rs.terminal ? 'run.page.closed' : 'run.page.following') };
			case 'ask':
				return { hint: t('run.page.askHint') };
			case 'pings':
				return rs.pendingPings.length
					? { count: rs.pendingPings.length, alert: 'attention', hint: t('run.page.waitingOnYou') }
					: { count: rs.pings.length || '' };
			case 'phases':
				return {
					hint:
						rs.run?.idle && !rs.terminal ? t('run.page.betweenPhases') : t('run.page.whatCanStart')
				};
			case 'timeline':
				return { count: timelineCount || '', hint: t('run.page.everyEvent') };
			case 'record':
				return { count: fileCount || '', hint: t('run.page.keptAfter') };
			case 'changes':
				return {
					count: rs.hasWorkspace ? (rs.changesCount ?? '') : '',
					hint: rs.hasWorkspace && !rs.terminal ? t('run.page.refresh5') : ''
				};
			case 'files':
				return { hint: t('run.page.readOnly') };
			case 'terminal':
				return { hint: t('run.page.xterm') };
			case 'logs':
				return { hint: rs.logSource === 'workspace' && !rs.terminal ? 'tail -f' : '' };
			case 'egress':
				return rs.newDenials.length
					? { count: rs.newDenials.length, alert: 'attention', hint: t('run.page.rulesLive') }
					: { hint: t('run.page.rulesLive') };
			case 'services': {
				const svcs = rs.services?.services ?? [];
				const up = svcs.filter((s) => s.state === 'healthy').length;
				const hint = up ? t('run.page.servicesHint') : '';
				return svcs.some((s) => s.state === 'unhealthy')
					? { count: up || '', alert: 'danger', hint }
					: { count: up || '', hint };
			}
			case 'ports':
				return { count: rs.observed.length || '' };
			case 'vitals':
				return { hint: t('run.page.sampled') };
		}
	}

	// A stage's pill opens what it has: its log once it finished an attempt,
	// else the session where it is happening.
	function openStage(id: string) {
		const logged = rs.events.some(
			(e) =>
				e.payload.stage_id === id &&
				['stage_completed', 'stage_failed', 'stage_paused'].includes(e.type)
		);
		if (logged) {
			rs.logSource = id;
			desk.reveal('logs');
		} else desk.reveal('session');
	}
	function openLog(stage: string) {
		rs.logSource = stage;
		desk.reveal('logs');
	}
	const openRecord = () => desk.reveal('record');

	const showDenials = $derived(rs.newDenials.length > 0 && rs.hasWorkspace && !rs.terminal);
	const showUnknown = $derived(rs.run?.envStatus === 'unknown' && !unknownDismissed);
</script>

<svelte:head>
	<title
		>{rs.run ? `${shortId(rs.run.id)} · ${rs.run.title}` : t('run.page.docTitle')} · Apothecary</title
	>
</svelte:head>

<div
	class="runpage"
	class:compact
	class:full={!!desk.max}
	bind:clientWidth={pageW}
	bind:clientHeight={pageH}
	style:--desk-h="{Math.max(320, pageH - 22)}px"
>
	{#if rs.run}
		<RunHeader {rs} {desk} {onretried} />

		{#if showDenials}
			<div class="bn">
				<Banner
					tone="attention"
					title={t('run.page.egressHeld', { n: times(rs.newDenials.length) })}
					aside={t('run.page.egressAside')}
					items={rs.newDenials.map((d) => d.host)}
					action={t('run.page.review')}
					onaction={() => {
						rs.egressRule = 'held';
						desk.reveal('egress');
					}}
					ondismiss={() => rs.dismissDenials()}
				/>
			</div>
		{/if}
		{#if showUnknown}
			<div class="bn">
				<Banner
					tone="attention"
					title={t('run.page.wsUnknown')}
					items={[t('run.page.provisionerDown')]}
					action={t('run.page.integrations')}
					onaction={() => goto(to.system('integrations'))}
					ondismiss={() => (unknownDismissed = true)}
				/>
			</div>
		{/if}
		{#if rs.error}
			<div class="bn error">
				<ErrorCard cause={rs.error} ondismiss={() => (rs.error = '')} />
			</div>
		{/if}

		<Desk {desk} {meta}>
			{#snippet view(v, p)}
				{#if v === 'flow'}
					<FlowView {rs} w={p.w} h={p.h} onstage={openStage} />
				{:else if v === 'session'}
					<SessionView
						{rs}
						visible={p.visible}
						onlog={openLog}
						onphases={() => desk.reveal('phases')}
					/>
				{:else if v === 'ask'}
					<AskView {rs} visible={p.visible} />
				{:else if v === 'pings'}
					<PingsView {rs} />
				{:else if v === 'phases'}
					<PhasesView {rs} />
				{:else if v === 'timeline'}
					<TimelineView {rs} />
				{:else if v === 'record'}
					<RecordView {rs} />
				{:else if v === 'changes' || v === 'files' || v === 'terminal'}
					<WorkspaceView {rs} kind={v} visible={p.visible} onrecord={openRecord} />
				{:else if v === 'logs'}
					<LogsView {rs} visible={p.visible} />
				{:else if v === 'egress'}
					<EgressView {rs} visible={p.visible} />
				{:else if v === 'services'}
					<ServicesView {rs} visible={p.visible} onstage={openStage} />
				{:else if !rs.hasWorkspace && (v === 'ports' || rs.samples.length < 2)}
					<NoWorkspace {rs} onrecord={openRecord} />
				{:else if v === 'ports'}
					<PortsView {rs} visible={p.visible} />
				{:else if v === 'vitals'}
					<VitalsView {rs} visible={p.visible} />
				{/if}
			{/snippet}
		</Desk>
	{:else if rs.loadError}
		<div class="pad">
			<ErrorCard
				cause={t('run.page.loadError')}
				detail={rs.loadError}
				onretry={() => rs.refreshRun()}
			/>
		</div>
	{:else}
		<div class="pad"><Loading text={t('run.page.connecting')} /></div>
	{/if}
</div>

<style>
	.runpage {
		position: relative;
		flex: 1;
		min-height: 0;
		min-width: 0;
		padding: var(--shell-gap);
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
		container: runpage / inline-size;
	}
	/* A fullscreen window rises over the sidebar: the container query makes
	 * this page a stacking context of its own. */
	.runpage.full {
		z-index: var(--z-full);
	}
	.bn {
		flex: none;
	}
	.pad {
		padding: 22px;
	}
	.compact {
		padding: 8px;
		gap: 8px;
		overflow-x: hidden;
		overflow-y: auto;
		overscroll-behavior-y: contain;
	}
	.compact > :global(.desk) {
		flex: none;
		height: var(--desk-h);
	}
</style>
