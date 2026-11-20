<script lang="ts">
	// The assistant beside a run (docs/assistant-plan.md, phase 5): it reads
	// the run, its journal and its flow's other runs, asks the person to
	// confirm acts on it, and proposes a change to its flow, which opens in
	// the flow editor against the flow as it is now (the run keeps the
	// version it started on).
	import { goto } from '$app/navigation';
	import AssistantPanel from '$lib/assistant/AssistantPanel.svelte';
	import { HandoffGuard } from '$lib/assistant/fillGuard.svelte';
	import { stashFlowFill } from '$lib/assistant/handoff';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { runFill, type RunFill } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const guard = new HandoffGuard<RunFill>(runFill);

	/** The run as the person sees it, for the question. */
	const pageState = () => {
		const r = rs.run;
		if (!r) return null;
		return {
			run: r.id,
			title: r.title,
			state: r.state,
			flow: r.flowName,
			flow_hash: r.flowHash,
			current_stage: r.currentStage,
			paused: r.pause?.reason,
			open_pings: rs.pendingPings.length,
			merge_requests: r.mrs.map((m) => m.url)
		};
	};

	function open(f: RunFill) {
		const ws = rs.run?.projectId;
		if (!ws) return;
		stashFlowFill(ws, f.flow, f.yaml);
		guard.revert();
		void goto(to.flow(ws, f.flow));
	}
</script>

<div class="av col">
	{#if guard.handoff}
		{@const f = guard.handoff}
		<Callout hue="accent" glyph={I.info} ondismiss={() => guard.revert()}>
			<div class="col">
				<span>{t('assistant.handoff', { flow: f.flow })}</span>
				<div class="row">
					<button class="btn-primary btn-sm" onclick={() => open(f)}
						>{t('assistant.openInEditor')}</button
					>
				</div>
			</div>
		</Callout>
	{/if}
	{#if rs.run?.projectId}
		<AssistantPanel
			kind="run"
			subject={rs.run.id}
			workshop={rs.run.projectId}
			{guard}
			{pageState}
		/>
	{/if}
</div>

<style>
	.av {
		gap: 10px;
		padding: 10px;
		min-height: 0;
		overflow-y: auto;
		height: 100%;
	}
</style>
