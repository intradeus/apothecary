<script lang="ts">
	// The live run's header (DESIGN 7.1, 4.5, 4.6): the batch label, the
	// breadcrumb with the Layouts button, the title with its state chip, the
	// meta row, and three control groups that keep the RUN's lifecycle and
	// the WORKSPACE's apart — plus the merge requests the run opened.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { currentGuild, to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { onMount } from 'svelte';
	import type { MergeRequestLive } from '$lib/domain';
	import { ago, fmtAbs, fmtCost, fmtHoursMinutes, shortId, stateHue } from '$lib/format';
	import { esc } from '$lib/ui/html';
	import { I } from '$lib/ui/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import Breadcrumbs, { type Crumb } from '$lib/ui/Breadcrumbs.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import LayoutsPopover from '$lib/desk/LayoutsPopover.svelte';
	import type { DeskState } from '$lib/desk/desk.svelte';
	import MrStatus from './MrStatus.svelte';
	import RestartModal from './RestartModal.svelte';
	import { accentSplit } from './derive';
	import type { RunStore } from './store.svelte';

	let { rs, desk, onretried }: { rs: RunStore; desk: DeskState; onretried: () => void } = $props();

	const run = $derived(rs.run!);
	const guild = $derived(currentGuild());
	const project = $derived(run.projectId ? fleet.projectById.get(run.projectId) : undefined);
	const sid = $derived(shortId(run.id));
	const [plain, accent] = $derived(accentSplit(run.title));
	const ticketKey = $derived(rs.ticket?.source?.key);
	const crumbs = $derived<Crumb[]>([
		{ label: guild.name, href: to.desk() },
		...(run.projectId
			? [
					{
						label: project?.name ?? (rs.projectName || 'workshop'),
						href: to.workshop(run.projectId)
					}
				]
			: []),
		...(run.ticketId
			? [
					{
						label: ticketKey ?? rs.ticket?.title ?? 'ticket',
						href: to.ticket(run.ticketId),
						mono: !!ticketKey
					}
				]
			: []),
		{ label: sid, mono: true }
	]);
	// How long the batch has brewed: ticking while the run lives, stopped at
	// its last word once it ends (updatedAt for runs that never said one).
	const END = new Set(['run_completed', 'run_failed', 'run_abandoned']);
	const endedAt = $derived(
		rs.terminal ? (rs.events.findLast((e) => END.has(e.type))?.ts ?? run.updatedAt) : undefined
	);
	const brewed = $derived(
		fmtHoursMinutes(
			(endedAt ? new Date(endedAt).getTime() : rs.now) - new Date(run.createdAt).getTime()
		)
	);
	const glow = $derived(`color-mix(in oklch, ${stateHue[run.state]} 45%, transparent)`);
	const active = $derived(!rs.terminal);
	const wsName = $derived(run.envId ?? 'the workspace');

	// The stage a retry re-enters: where the run stopped.
	const retryStage = $derived(
		run.currentStage ??
			(rs.events.findLast((e) => e.type === 'stage_failed')?.payload.stage_id as string | undefined)
	);

	let splitBtn = $state<HTMLButtonElement | null>(null);
	let envBtn = $state<HTMLButtonElement | null>(null);
	let rebuildBtn = $state<HTMLButtonElement | null>(null);
	let envOpen = $state(false);
	let rebuildOpen = $state(false);
	let restartOpen = $state(false);
	let busy = $state<'' | 'pause' | 'play' | 'stop' | 'retry' | 'archive' | 'editor' | 'rebuild'>(
		''
	);
	let copied = $state('');

	// Hold the live ports feed while the Env menu is open (its app links).
	$effect(() => {
		if (envOpen) return rs.usePorts();
	});

	async function act(which: typeof busy, fn: () => Promise<unknown>): Promise<boolean> {
		busy = which;
		const ok = await rs.act(fn);
		busy = '';
		return ok;
	}

	// What the forge says about the run's merge requests right now, polled
	// while the run is alive; an observation, not part of the run's record.
	let mrLive = $state<MergeRequestLive[]>([]);
	const hasMr = $derived(run.mrs.length > 0 || !!run.mrUrl);
	async function loadMrs() {
		if (!hasMr) return;
		try {
			mrLive = await api.runs.mrStatus(run.id);
		} catch {
			// The header must not break on a forge hiccup; the next poll retries.
		}
	}
	onMount(() => {
		void loadMrs();
		const t = setInterval(() => active && !document.hidden && loadMrs(), 30_000);
		return () => clearInterval(t);
	});
	const mrShown = $derived([
		...mrLive.filter((m) => m.state === 'open' || m.error),
		...mrLive.filter((m) => m.state === 'merged' && !m.error)
	]);

	async function stop() {
		const ok = await confirmDialog({
			title: `Stop run ${sid}?`,
			body: `<span class="tx">The current turn is cancelled and the run becomes <b>abandoned</b>. You can still retry a stage or start a new run from it.</span><br><br>${
				run.envId
					? `Workspace <span class="mono">${esc(run.envId)}</span> stays up until the reaper removes it.`
					: 'No workspace is running for it.'
			} The record and timeline are kept.`,
			action: 'Stop run'
		});
		if (ok) await act('stop', () => api.runs.abort(run.id));
	}

	async function archive() {
		const ok = await confirmDialog({
			title: `Archive run ${sid}?`,
			body: `<span class="tx">The run is filed away as <b>archived</b> and leaves the lists; its record and timeline are kept.</span><br><br>${
				run.envId
					? `Workspace <span class="mono">${esc(run.envId)}</span> is <b>destroyed</b> — anything not committed or captured is gone.`
					: 'It has no workspace left to destroy.'
			}`,
			action: 'Archive run'
		});
		if (ok && (await act('archive', () => api.runs.archive(run.id))))
			toast(`Run ${sid} archived`, {
				action: run.ticketId
					? { label: 'Open ticket', run: () => goto(to.ticket(run.ticketId!)) }
					: undefined
			});
	}

	async function retry() {
		busy = 'retry';
		// Same run, same workspace: the page re-subscribes once it continues.
		if (await rs.act(() => api.runs.resume(run.id))) onretried();
		busy = '';
	}

	async function openEditor() {
		busy = 'editor';
		rs.error = '';
		// Open the tab inside the click so it is not blocked; code-server boots
		// for a few seconds on first open.
		const w = window.open('', '_blank');
		try {
			const url = await api.runs.openEditor(run.id);
			if (w) w.location.href = url;
			else window.open(url, '_blank');
		} catch (e) {
			w?.close();
			rs.error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = '';
		}
	}

	async function rebuild(noCache: boolean) {
		rebuildOpen = false;
		// The request blocks for the whole build; the badge and the Logs tail
		// show progress meanwhile.
		toast(noCache ? 'Rebuild started, without cache' : 'Rebuild started', {
			tone: 'info',
			action: { label: 'Watch logs', run: () => desk.reveal('logs') }
		});
		if (await act('rebuild', () => api.runs.rebuild(run.id, noCache))) toast('Workspace rebuilt');
	}

	async function copy(l: { kind: string; command?: string }) {
		if (!l.command) return;
		try {
			await navigator.clipboard.writeText(l.command);
			toast(`Copied the ${l.kind} command`);
			copied = l.kind;
			setTimeout(() => copied === l.kind && (copied = ''), 1500);
		} catch {
			rs.error = 'Could not copy — the browser refused clipboard access.';
		}
	}

	const canRebuild = $derived(
		!!run.envId && run.state !== 'archived' && (rs.terminal || run.state === 'paused')
	);
	// One slot per checkout: an opened MR links out, the rest are "not yet".
	const mrSlots = $derived.by(() => {
		const n = Math.max(run.checkouts.length, run.mrs.length, 1);
		const slots: { label: string; url?: string; title: string }[] = [];
		const seen = new Set<string>();
		const name = (i: number) => (n > 1 ? `MR ${i + 1}/${n}` : 'MR');
		run.mrs.forEach((mr, i) => {
			seen.add(mr.repo);
			slots.push({
				label: `${name(i)} ${I.external}`,
				url: mr.url,
				title: `${mr.repo !== 'repo' ? mr.repo + ': ' : ''}${mr.branch}${mr.target ? ' → ' + mr.target : ''}`
			});
		});
		for (const c of run.checkouts) {
			const key = c.primary ? 'repo' : c.name;
			if (seen.has(key) || seen.has(c.name) || slots.length >= n) continue;
			slots.push({ label: `${name(slots.length)} ${I.none}`, title: `${c.name}: not opened yet` });
		}
		// Runs from before multi-repo carry only mrUrl, no mrs list.
		if (!slots.length && run.mrUrl)
			slots.push({
				label: `MR ${I.external}`,
				url: run.mrUrl,
				title: run.branch ?? 'Merge request'
			});
		if (!slots.length) slots.push({ label: `MR ${I.none}`, title: 'Not opened yet' });
		return slots;
	});
</script>

<header class="rh">
	<div class="rbadge lg" style:--glow={glow}>
		<div class="bt"><span>Batch</span><span class="mono">Nº</span></div>
		<div class="bid">{shortId(run.id, '')}</div>
		<div class="bb" title="Started {fmtAbs(run.createdAt)} · {ago(run.createdAt, rs.now)}">
			{rs.terminal ? 'brewed' : 'brewing'}
			{brewed}
		</div>
		<span class="bg"></span>
	</div>

	<div class="mid">
		<div class="crow">
			<Breadcrumbs {crumbs} />
			<button
				bind:this={splitBtn}
				class="btn-sel layout"
				title="Windows & layouts"
				aria-expanded={desk.menuOpen}
				onclick={() => (desk.menuOpen = !desk.menuOpen)}
			>
				<span class="lg-ic"><span></span><span></span><span></span></span>{desk.label}
				{I.menu}
			</button>
		</div>
		<div class="trow">
			<h1>
				{plain}{#if accent}{' '}<span class="accent">{accent}</span>{/if}
			</h1>
			<StatusChip state={run.state} suffix />
		</div>
		<div class="meta">
			{#if run.ticketId}
				<a class="tk tx" href={to.ticket(run.ticketId)}
					>{#if ticketKey}<span class="tag">{ticketKey}</span>{/if}ticket {I.external}</a
				>
			{/if}
			<span
				>flow <span class="tx">{run.flowName}</span>{#if run.flowHash}
					<span class="mono hash fnt">@{run.flowHash.slice(0, 6)}</span>{/if}</span
			>
			{#if run.branch}
				<span
					class="mono br ell"
					title="This run's branch, forked off {run.sourceBranch ?? 'the default'}"
					><span class="tx">{run.branch}</span> ← {run.sourceBranch ?? 'default'}</span
				>
			{/if}
			<span
				><span class="mono tx cost">{fmtCost(rs.totals.cost)}</span> · {rs.totals.turns} turn{rs
					.totals.turns === 1
					? ''
					: 's'}</span
			>
		</div>
	</div>

	<div class="groups">
		<GlassGroup label="Run">
			{#if active}
				{#if run.state === 'paused'}
					<button
						class="btn-primary"
						disabled={busy !== ''}
						aria-busy={busy === 'play'}
						onclick={() => act('play', () => api.runs.play(run.id))}
						title="Continue where it left off — the in-flight stage re-enters (agent sessions resume); retry budgets are untouched"
						>{I.play} Resume</button
					>
				{:else if run.state !== 'pending'}
					<button
						disabled={busy !== ''}
						aria-busy={busy === 'pause'}
						onclick={() => act('pause', () => api.runs.pause(run.id))}
						title="Park the run: the current attempt is stopped, the workspace stays up for inspection, and nothing moves until Resume"
						>{I.pause} Pause</button
					>
				{/if}
				<button class="btn-danger" disabled={busy !== ''} aria-busy={busy === 'stop'} onclick={stop}
					>{I.stop} Stop…</button
				>
			{:else}
				{#if run.state === 'failed' || run.state === 'abandoned'}
					<button
						class="btn-primary"
						disabled={busy !== ''}
						aria-busy={busy === 'retry'}
						onclick={retry}
						title="Re-run {retryStage ??
							'the failed stage'} in the same workspace — keeps the plan, build and commits; only retries what failed"
						>{I.retry} Retry{retryStage ? ` ${retryStage}` : ''}</button
					>
				{/if}
				<button
					disabled={busy !== ''}
					onclick={() => (restartOpen = true)}
					title="A fresh run of the same ticket: new workspace, new branch — with direction, files and a flow of your choosing"
					>New run from this…</button
				>
				{#if run.state !== 'archived'}
					<button
						class="btn-danger"
						disabled={busy !== ''}
						aria-busy={busy === 'archive'}
						onclick={archive}>Archive…</button
					>
				{/if}
			{/if}
		</GlassGroup>

		<GlassGroup>
			{#snippet label()}
				<WorkspaceStatus state={run.state} envId={run.envId} envStatus={run.envStatus} />
			{/snippet}
			<button
				bind:this={envBtn}
				disabled={!rs.hasWorkspace || rs.links.length === 0}
				title={rs.links.length ? 'Environment links' : 'No environment links'}
				aria-expanded={envOpen}
				onclick={() => (envOpen = !envOpen)}>Env {I.menu}</button
			>
			<button
				disabled={!rs.hasWorkspace || busy === 'editor'}
				aria-busy={busy === 'editor'}
				onclick={openEditor}
				title="VS Code (code-server) in the browser on this workspace — the repo, the agent's commits, a terminal"
				>Editor {I.external}</button
			>
			<button
				bind:this={rebuildBtn}
				disabled={!canRebuild || busy === 'rebuild'}
				aria-busy={busy === 'rebuild'}
				aria-expanded={rebuildOpen}
				title={canRebuild
					? 'Recreate the container in place — the run and its commits are untouched'
					: run.envId
						? 'Pause or stop the run first — a live run refuses a rebuild'
						: 'No workspace to rebuild'}
				onclick={() => (rebuildOpen = !rebuildOpen)}>Rebuild {I.menu}</button
			>
		</GlassGroup>

		<GlassGroup>
			{#snippet label()}
				<span>Merge requests</span>
				{#each mrShown as mr (mr.repo + mr.branch)}<MrStatus {mr} repo={mrLive.length > 1} />{/each}
			{/snippet}
			{#each mrSlots as m, i (i)}
				{#if m.url}
					<a
						class="btn {i === 0 ? 'btn-primary' : ''}"
						href={m.url}
						target="_blank"
						rel="noreferrer"
						title={m.title}>{m.label}</a
					>
				{:else}
					<span class="btn btn-notyet" title={m.title}>{m.label}</span>
				{/if}
			{/each}
		</GlassGroup>
	</div>
</header>

<LayoutsPopover {desk} anchor={splitBtn} />

<Popover
	anchor={envBtn}
	bind:open={envOpen}
	placement="below-end"
	width={300}
	title="Environment"
	legend="{rs.links.length} link{rs.links.length === 1 ? '' : 's'}"
>
	{#each rs.links as l (l.kind + (l.url ?? l.command ?? ''))}
		{#if l.url}
			<a class="mi" href={l.url} target="_blank" rel="noreferrer" title={l.url}>
				<span class="col"><span>{l.kind}</span><span class="sub ell">{l.url}</span></span>
				<span class="g">{I.external}</span>
			</a>
		{:else if l.command}
			<button class="mi" title="Copy: {l.command}" onclick={() => copy(l)}>
				<span class="col"><span>{l.kind}</span><span class="sub mono ell">{l.command}</span></span>
				<span class="cp" class:hue-grn={copied === l.kind}
					>{copied === l.kind ? `copied ${I.ok}` : 'copy'}</span
				>
			</button>
		{/if}
	{/each}
</Popover>

<Popover
	anchor={rebuildBtn}
	bind:open={rebuildOpen}
	placement="below-end"
	width={300}
	title="Rebuild"
	legend="the run is untouched"
>
	<button class="mi" onclick={() => rebuild(false)}>
		<span class="col wrap">
			<span>Rebuild workspace</span>
			<span class="sub"
				>Recreate the container in place, re-run setup. The run and its commits stay.</span
			>
		</span>
	</button>
	<button class="mi" onclick={() => rebuild(true)}>
		<span class="col wrap">
			<span>Rebuild without cache</span>
			<span class="sub"
				>Every layer from scratch — for a moved base tag or a stale layer the cache can't see. Slow.</span
			>
		</span>
	</button>
</Popover>

<RestartModal
	{rs}
	bind:open={restartOpen}
	onstarted={(id) => {
		const from = run.id;
		toast(`New run ${shortId(id)} started`, {
			action: { label: `Back to ${sid}`, run: () => goto(to.run(from)) }
		});
		goto(to.run(id));
	}}
/>

<style>
	.rh {
		flex: none;
		padding: 16px 20px 12px;
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 16px;
		align-items: center;
	}
	.rbadge {
		width: 96px;
		height: 78px;
		border-radius: var(--r-group);
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		padding: 8px 10px;
		position: relative;
		overflow: hidden;
	}
	.bt,
	.bb {
		font-size: var(--fs-id);
		color: var(--mut);
		display: flex;
		justify-content: space-between;
	}
	.bt .mono {
		font-size: var(--fs-id);
	}
	.bid {
		font-family: var(--mono);
		font-weight: 600;
		font-size: var(--fs-mark);
		letter-spacing: var(--tr-display);
		line-height: 1;
	}
	.bg {
		position: absolute;
		right: -14px;
		bottom: -14px;
		width: 46px;
		height: 46px;
		border-radius: 50%;
		background: radial-gradient(circle, var(--glow), transparent 70%);
		pointer-events: none;
	}
	.mid {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.crow {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
	}
	.crow :global(.bc) {
		flex: 1;
	}
	.crow :global(.bc > *) {
		flex: 0 1 auto;
		min-width: 0;
	}
	.crow :global(.bc > .sep) {
		flex: none;
	}
	.groups :global(.ws .id) {
		max-width: 96px;
	}
	.layout {
		flex: none;
		margin-left: auto;
		padding: 3px 10px;
		border-radius: var(--r-tab);
		font-size: var(--fs-help);
		gap: 7px;
	}
	.lg-ic {
		width: 11px;
		height: 11px;
		display: grid;
		grid-template-columns: 1fr 1fr;
		grid-template-rows: 1fr 1fr;
		gap: 2px;
	}
	.lg-ic > span {
		border-radius: calc(var(--r-xs) / 2);
		border: 1.2px solid currentColor;
	}
	.lg-ic > span:first-child {
		grid-row: span 2;
	}
	.trow {
		display: flex;
		align-items: flex-end;
		gap: 12px;
		min-width: 0;
	}
	h1 {
		min-width: 0;
		font-size: var(--fs-empty);
		overflow-wrap: anywhere;
	}
	h1 .accent {
		font-size: var(--fs-display);
	}
	.trow :global(> :last-child) {
		flex: none;
		margin-bottom: 2px;
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		color: var(--mut);
		font-size: var(--fs-label);
		align-items: center;
		min-width: 0;
	}
	.meta .tx {
		font-weight: 500;
	}
	.tk {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.hash {
		font-size: var(--fs-meta);
	}
	.br {
		font-weight: 400;
		max-width: 100%;
	}
	.meta .br .tx {
		font-weight: 400;
	}
	.cost {
		font-size: var(--fs-label);
	}
	.groups {
		display: flex;
		gap: 8px;
		align-items: stretch;
		justify-content: flex-end;
		flex-wrap: wrap;
	}
	.cp {
		font-size: var(--fs-help);
		font-weight: 600;
		color: var(--mut);
	}
	.cp.hue-grn {
		color: var(--grn);
	}
	/* Menu rows don't wrap; these explain themselves in a sentence. */
	.wrap {
		white-space: normal;
	}
	.wrap .sub {
		line-height: 1.35;
	}
	/* Container queries on the run page's own width (the desk beside the
	 * sidebar), at the shared breakpoints. */
	@container runpage (max-width: 1180px) {
		.rh {
			grid-template-columns: auto minmax(0, 1fr);
			row-gap: 10px;
		}
		.groups {
			grid-column: 1 / -1;
			justify-content: flex-start;
		}
	}
	@container runpage (max-width: 760px) {
		.rbadge {
			display: none;
		}
		.rh {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
