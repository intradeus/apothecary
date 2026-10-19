<script lang="ts">
	// The live run (DESIGN 7.1): header, an alert banner when something needs
	// a decision, and the window desk with the run's thirteen views.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { to } from '$lib/guild';
	import { shortId, times } from '$lib/format';
	import { lore } from '$lib/lore';
	import Banner from '$lib/ui/Banner.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Desk, { type ViewMeta } from '$lib/desk/Desk.svelte';
	import { DeskState } from '$lib/desk/desk.svelte';
	import type { ViewId } from '$lib/desk/tree';
	import RunHeader from './RunHeader.svelte';
	import NoWorkspace from './NoWorkspace.svelte';
	import FlowView from './views/FlowView.svelte';
	import SessionView from './views/SessionView.svelte';
	import PingsView from './views/PingsView.svelte';
	import TimelineView from './views/TimelineView.svelte';
	import RecordView from './views/RecordView.svelte';
	import WorkspaceView from './views/WorkspaceView.svelte';
	import LogsView from './views/LogsView.svelte';
	import EgressView from './views/EgressView.svelte';
	import PortsView from './views/PortsView.svelte';
	import ResourcesView from './views/ResourcesView.svelte';
	import VitalsView from './views/VitalsView.svelte';
	import { TRANSCRIPT_TYPES } from './derive';
	import { RunStore } from './store.svelte';

	let { runId, onretried }: { runId: string; onretried: () => void } = $props();

	// svelte-ignore state_referenced_locally
	const rs = new RunStore(runId);
	const desk = new DeskState();
	onMount(() => rs.start());

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
				return { hint: rs.terminal ? 'closed' : 'following' };
			case 'pings':
				return rs.pendingPings.length
					? { count: rs.pendingPings.length, alert: 'amb', hint: 'waiting on you' }
					: { count: rs.pings.length || '' };
			case 'timeline':
				return { count: timelineCount || '', hint: 'every lifecycle event' };
			case 'record':
				return { count: fileCount || '', hint: 'kept after the workspace' };
			case 'changes':
				return {
					count: rs.hasWorkspace ? (rs.changesCount ?? '') : '',
					hint: rs.hasWorkspace && !rs.terminal ? 'refreshes every 5s' : ''
				};
			case 'files':
				return { hint: 'read-only · first 1 MB' };
			case 'terminal':
				return { hint: 'xterm · ends when hidden' };
			case 'logs':
				return { hint: rs.logSource === 'workspace' && !rs.terminal ? 'tail -f' : '' };
			case 'egress':
				return rs.newDenials.length
					? { count: rs.newDenials.length, alert: 'ros', hint: 'rules apply live' }
					: { hint: 'rules apply live' };
			case 'ports':
				return { count: rs.observed.length || '' };
			case 'resources':
			case 'vitals':
				return { hint: 'sampled every 10s' };
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
	<title>{rs.run ? `${shortId(rs.run.id)} · ${rs.run.title}` : 'Run'} · Apothecary</title>
</svelte:head>

<div class="runpage">
	{#if rs.run}
		<RunHeader {rs} {desk} {onretried} />

		{#if showDenials}
			<div class="bn">
				<Banner
					title="Egress blocked {times(rs.newDenials.length)}"
					aside={lore('egressBlocked') || undefined}
					items={rs.newDenials.map((d) => d.host)}
					action="Review & allow"
					onaction={() => {
						rs.egressVerdict = 'deny';
						desk.reveal('egress');
					}}
					ondismiss={() => rs.dismissDenials()}
				/>
			</div>
		{/if}
		{#if showUnknown}
			<div class="bn">
				<Banner
					tone="amb"
					title="Workspace unknown"
					items={['Provisioner unreachable — check the integration token']}
					action="Integrations"
					onaction={() => goto(to.system('integrations'))}
					ondismiss={() => (unknownDismissed = true)}
				/>
			</div>
		{/if}
		{#if rs.error}
			<div class="err" role="alert">
				<span>{rs.error}</span>
				<button class="btn-icon" title="Dismiss" onclick={() => (rs.error = '')}>×</button>
			</div>
		{/if}

		<Desk {desk} {meta}>
			{#snippet view(v, p)}
				{#if v === 'flow'}
					<FlowView {rs} w={p.w} h={p.h} onstage={openStage} />
				{:else if v === 'session'}
					<SessionView {rs} visible={p.visible} onlog={openLog} />
				{:else if v === 'pings'}
					<PingsView {rs} />
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
				{:else if !rs.hasWorkspace && (v === 'ports' || rs.samples.length < 2)}
					<NoWorkspace {rs} onrecord={openRecord} />
				{:else if v === 'ports'}
					<PortsView {rs} visible={p.visible} />
				{:else if v === 'resources'}
					<ResourcesView {rs} visible={p.visible} />
				{:else if v === 'vitals'}
					<VitalsView {rs} visible={p.visible} />
				{/if}
			{/snippet}
		</Desk>
	{:else if rs.loadError}
		<div class="pad">
			<ErrorCard
				cause="This run could not be loaded."
				detail={rs.loadError}
				onretry={() => rs.refreshRun()}
			/>
		</div>
	{:else}
		<div class="pad conn"><span class="d pulse"></span>Connecting…</div>
	{/if}
</div>

<style>
	.runpage {
		flex: 1;
		min-height: 0;
		min-width: 0;
		display: flex;
		flex-direction: column;
		container: runpage / inline-size;
	}
	.bn {
		flex: none;
		margin: 0 16px;
	}
	.bn + .bn {
		margin-top: 6px;
	}
	.err {
		flex: none;
		margin: 6px 16px 0;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 6px 4px 12px;
		border-radius: 10px;
		background: var(--rosBg);
		border: 1px solid var(--rosLn);
		color: var(--ros);
		font-size: 12px;
	}
	.err span {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.pad {
		padding: 22px;
	}
	.conn {
		display: flex;
		gap: 8px;
		align-items: center;
		color: var(--mut);
	}
	.d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--az);
		box-shadow: 0 0 8px var(--az);
	}
</style>
