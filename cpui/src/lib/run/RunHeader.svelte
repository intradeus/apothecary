<script lang="ts">
	// The run's top bar (DESIGN 4.5, 7.1): one glass strip. The title block —
	// breadcrumb, a small title, a status line — then two inset groups that keep
	// the RUN's lifecycle and the WORKSPACE's apart, then the MR island. The
	// title gives context; the windows hold the information. The run offers
	// itself to the assistant's page mode: it reads the run, its journal and
	// its flow's other runs, and proposes a change to its flow, which opens in
	// the flow editor against the flow as it is now (the run keeps the version
	// it started on).
	import { goto } from '$app/navigation';
	import { offerPage } from '$lib/assistant/assistant.svelte';
	import { HandoffGuard } from '$lib/assistant/fillGuard.svelte';
	import { stashFlowFill } from '$lib/assistant/handoff';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { runFill, type RunFill } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { shortId } from '$lib/core/format';
	import Callout from '$lib/ui/Callout.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import type { DeskState } from '$lib/desk/deskState.svelte';
	import RerunModal from './RerunModal.svelte';
	import type { RunStore } from './store.svelte';
	import EnvMenu from './header/EnvMenu.svelte';
	import RebuildMenu from './header/RebuildMenu.svelte';
	import RunControls from './header/RunControls.svelte';
	import RunMrs from './header/RunMrs.svelte';
	import RunTitle from './header/RunTitle.svelte';
	import WorkspaceControls from './header/WorkspaceControls.svelte';
	import type { HeaderAct, HeaderActions } from './header/actions.svelte';

	let {
		rs,
		desk,
		actions,
		onretried
	}: {
		rs: RunStore;
		desk: DeskState;
		/** What is in flight: the page's, so its banners share the busy set. */
		actions: HeaderActions;
		onretried: () => void;
	} = $props();

	const run = $derived(rs.run!);
	const sid = $derived(shortId(run.id));

	// The menus sit outside the strip, not in the groups that open them: a
	// fixed popover inside glass would be clipped by it. So their triggers and
	// open state live here.
	let envBtn = $state<HTMLButtonElement | null>(null);
	let rebuildBtn = $state<HTMLButtonElement | null>(null);
	let envOpen = $state(false);
	let rebuildOpen = $state(false);
	let rerunOpen = $state(false);
	const act: HeaderAct = (which, fn) => actions.run(which, () => rs.act(fn));

	// The assistant's page mode on a run: a change to the run's flow is a
	// fill for another page, held here until the person takes it there.
	const guard = new HandoffGuard<RunFill>(runFill);
	/** The run as the person sees it, for the question. */
	const pageState = () => ({
		run: run.id,
		title: run.title,
		state: run.state,
		flow: run.flowName,
		flow_hash: run.flowHash,
		current_stage: run.currentStage,
		paused: run.pause?.reason,
		open_pings: rs.pendingPings.length,
		merge_requests: run.mrs.map((m) => m.url)
	});
	offerPage(() =>
		run.projectId
			? {
					kind: 'run',
					subject: run.id,
					workshop: run.projectId,
					name: run.title,
					guard,
					pageState,
					handoff: runHandoff
				}
			: null
	);
	/** The YAML of a change this tab could not hold for the editor: its
	 *  callout stays, saying so, with the YAML to copy instead. */
	let unheld = $state<string | null>(null);
	function openInEditor(ws: string, f: RunFill) {
		if (!stashFlowFill(ws, f.flow, f.yaml)) {
			unheld = f.yaml;
			return;
		}
		unheld = null;
		guard.revert();
		void goto(to.flow(ws, f.flow));
	}
	async function copyYaml(yaml: string) {
		try {
			await navigator.clipboard.writeText(yaml);
			toast(t('assistant.yamlCopied'));
		} catch {
			toast(t('ui.copy.failed'), { tone: 'bad' });
		}
	}
</script>

<header class="rh glass">
	<RunTitle {rs} />
	<div class="groups">
		<RunControls {rs} {actions} {act} onrerun={() => (rerunOpen = true)} {onretried} />
		<WorkspaceControls {rs} {actions} bind:envBtn bind:rebuildBtn bind:envOpen bind:rebuildOpen />
		<RunMrs {rs} />
	</div>
</header>

<EnvMenu {rs} anchor={envBtn} bind:open={envOpen} />

{#snippet runHandoff()}
	{#if guard.handoff && run.projectId}
		{@const f = guard.handoff}
		{@const ws = run.projectId}
		<Callout hue="accent" glyph={I.info} ondismiss={() => guard.revert()}>
			<div class="col">
				<span>{t('assistant.handoff', { flow: f.flow })}</span>
				<div class="row">
					<button class="btn-primary btn-sm" onclick={() => openInEditor(ws, f)}
						>{t('assistant.openInEditor')}</button
					>
					{#if unheld === f.yaml}
						<button class="btn-sm" onclick={() => copyYaml(f.yaml)}
							>{t('assistant.copyYaml')}</button
						>
					{/if}
				</div>
				{#if unheld === f.yaml}
					<div class="error" role="alert">{t('assistant.handoffUnheld')}</div>
				{/if}
			</div>
		</Callout>
	{/if}
{/snippet}

<RebuildMenu {rs} {desk} {act} anchor={rebuildBtn} bind:open={rebuildOpen} />

<RerunModal
	{rs}
	bind:open={rerunOpen}
	onstarted={(id) => {
		const from = run.id;
		toast(t('run.rerun.started', { id: shortId(id) }), {
			action: { label: t('run.rerun.back', { id: sid }), run: () => goto(to.run(from)) }
		});
		goto(to.run(id));
	}}
/>

<style>
	.rh {
		position: relative;
		z-index: var(--z-bar);
		flex: none;
		border-radius: var(--r-pane);
		padding: 8px 16px;
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.groups {
		flex: none;
		display: flex;
		gap: 8px;
		align-items: stretch;
		justify-content: flex-end;
		min-width: 0;
	}
	.groups :global(.gg + .gg) {
		max-width: 330px;
	}
	.groups :global(.gg .btns > button),
	.groups :global(.gg .btns > .btn) {
		font-size: var(--fs-label);
	}
	.groups :global(.ws) {
		font-size: var(--fs-help);
	}
	.groups :global(.ws .id) {
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
		max-width: 170px;
	}
	/* Container queries on the run page's own width (the desk beside the
	 * sidebar), at the shared breakpoints. */
	@container runpage (max-width: 1000px) {
		.rh {
			flex-wrap: wrap;
		}
		.groups {
			justify-content: flex-start;
			flex-wrap: wrap;
		}
	}
	@container runpage (max-width: 760px) {
		.rh {
			padding: 10px 12px;
		}
		.groups {
			flex-basis: 100%;
			flex-direction: column;
			align-items: stretch;
		}
		.groups :global(.pill-mr) {
			align-self: flex-start;
		}
		.groups > :global(.gg),
		.groups > :global(.gg + .gg) {
			max-width: none;
			flex-direction: row;
			align-items: center;
			flex-wrap: wrap;
			gap: 6px 10px;
		}
		/* The workspace's id gives its room to the buttons. */
		.groups :global(.ws .id) {
			display: none;
		}
		.groups > :global(.gg .btns) {
			margin-left: auto;
			flex-wrap: wrap;
			justify-content: flex-end;
		}
	}
</style>
