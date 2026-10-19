<script lang="ts">
	// The live run's header (DESIGN 7.1, 4.5, 4.6): the batch label, the
	// breadcrumb with the Layouts button, the title with its state chip, the
	// meta row, and three control groups that keep the RUN's lifecycle and
	// the WORKSPACE's apart — plus the merge requests the run opened.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { currentGuild, to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { fmtClock, fmtCost, shortId, stateHue } from '$lib/format';
	import { lore, loreLc, isLore } from '$lib/lore';
	import Breadcrumbs, { type Crumb } from '$lib/ui/Breadcrumbs.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import LayoutsPopover from '$lib/desk/LayoutsPopover.svelte';
	import type { DeskState } from '$lib/desk/desk.svelte';
	import MergeStatus from './MergeStatus.svelte';
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
						label: project?.name ?? (rs.projectName || loreLc('workshop')),
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
	const glow = $derived(`color-mix(in oklch, ${stateHue[run.state]} 45%, transparent)`);
	const active = $derived(!rs.terminal);
	const wsName = $derived(run.envId ?? 'the workspace');
	const esc = (s: string) =>
		s.replace(
			/[&<>"']/g,
			(c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!
		);

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

	async function act(which: typeof busy, fn: () => Promise<unknown>) {
		busy = which;
		await rs.act(fn);
		busy = '';
	}

	async function stop() {
		const ok = await confirmDialog({
			title: `Stop run ${sid}?`,
			body: `<span style="color:var(--tx)">The current turn is cancelled and the run becomes <b>abandoned</b>. You can still retry a stage or start a new run from it.</span><br><br>${
				run.envId
					? `Workspace <span style="font-family:var(--mono)">${esc(run.envId)}</span> stays up until the reaper removes it.`
					: 'No workspace is running for it.'
			} The record and timeline are kept.`,
			action: 'Stop run'
		});
		if (ok) await act('stop', () => api.runs.abort(run.id));
	}

	async function archive() {
		const ok = await confirmDialog({
			title: `Archive run ${sid}?`,
			body: `<span style="color:var(--tx)">The run is filed away as <b>archived</b> and leaves the lists; its record and timeline are kept.</span><br><br>${
				run.envId
					? `Workspace <span style="font-family:var(--mono)">${esc(run.envId)}</span> is <b>destroyed</b> — anything not committed or captured is gone.`
					: 'It has no workspace left to destroy.'
			}`,
			action: 'Archive run'
		});
		if (ok) await act('archive', () => api.runs.archive(run.id));
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
		await act('rebuild', () => api.runs.rebuild(run.id, noCache));
	}

	async function copy(l: { kind: string; command?: string }) {
		if (!l.command) return;
		try {
			await navigator.clipboard.writeText(l.command);
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
				label: `${name(i)} ↗`,
				url: mr.url,
				title: `${mr.repo !== 'repo' ? mr.repo + ': ' : ''}${mr.branch}${mr.target ? ' → ' + mr.target : ''}`
			});
		});
		for (const c of run.checkouts) {
			const key = c.primary ? 'repo' : c.name;
			if (seen.has(key) || seen.has(c.name) || slots.length >= n) continue;
			slots.push({ label: `${name(slots.length)} —`, title: `${c.name}: not opened yet` });
		}
		// Runs from before multi-repo carry only mrUrl, no mrs list.
		if (!slots.length && run.mrUrl)
			slots.push({ label: 'MR ↗', url: run.mrUrl, title: run.branch ?? 'Merge request' });
		if (!slots.length) slots.push({ label: 'MR —', title: 'Not opened yet' });
		return slots;
	});
</script>

<header class="rh">
	<div class="rbadge lg" style:--glow={glow}>
		<div class="bt"><span>{isLore() ? lore('run') : 'Run'}</span><span class="mono">Nº</span></div>
		<div class="bid">{shortId(run.id, '')}</div>
		<div class="bb">{isLore() ? 'brewed' : 'started'} {fmtClock(run.createdAt)}</div>
		<span class="bg"></span>
	</div>

	<div class="mid">
		<div class="crow">
			<Breadcrumbs {crumbs} />
			<button
				bind:this={splitBtn}
				class="btn-sel layout"
				title="Windows & layouts"
				onclick={() => (desk.menuOpen = !desk.menuOpen)}
			>
				<span class="lg-ic"><span></span><span></span><span></span></span>{desk.label} ▾
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
				<a class="tk" href={to.ticket(run.ticketId)}
					>{#if ticketKey}<span class="tag">{ticketKey}</span>{/if}ticket ↗</a
				>
			{/if}
			<span
				>flow <span class="tx">{run.flowName}</span>{#if run.flowHash}
					<span class="mono hash">@{run.flowHash.slice(0, 6)}</span>{/if}</span
			>
			{#if run.branch}
				<span
					class="mono br"
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
						onclick={() => act('play', () => api.runs.play(run.id))}
						title="Continue where it left off — the in-flight stage re-enters (agent sessions resume); retry budgets are untouched"
						>▶ Resume</button
					>
				{:else if run.state !== 'pending'}
					<button
						disabled={busy !== ''}
						onclick={() => act('pause', () => api.runs.pause(run.id))}
						title="Park the run: the current attempt is stopped, the workspace stays up for inspection, and nothing moves until Resume"
						>{busy === 'pause' ? 'Pausing…' : '❚❚ Pause'}</button
					>
				{/if}
				<button class="btn-danger" disabled={busy !== ''} onclick={stop}
					>{busy === 'stop' ? 'Stopping…' : '■ Stop…'}</button
				>
			{:else}
				{#if run.state === 'failed' || run.state === 'abandoned'}
					<button
						class="btn-primary"
						disabled={busy !== ''}
						onclick={retry}
						title="Re-run {retryStage ??
							'the failed stage'} in the same workspace — keeps the plan, build and commits; only retries what failed"
						>{busy === 'retry'
							? 'Retrying…'
							: `↻ Retry${retryStage ? ` ${retryStage}` : ''}`}</button
					>
				{/if}
				<button
					disabled={busy !== ''}
					onclick={() => (restartOpen = true)}
					title="A fresh run of the same ticket: new workspace, new branch — with direction, files and a flow of your choosing"
					>New run from this…</button
				>
				{#if run.state !== 'archived'}
					<button class="arch" disabled={busy !== ''} onclick={archive}
						>{busy === 'archive' ? 'Archiving…' : 'Archive…'}</button
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
				onclick={() => (envOpen = !envOpen)}>Env ▾</button
			>
			<button
				disabled={!rs.hasWorkspace || busy === 'editor'}
				onclick={openEditor}
				title="VS Code (code-server) in the browser on this workspace — the repo, the agent's commits, a terminal"
				>{busy === 'editor' ? 'Opening…' : 'Editor ↗'}</button
			>
			<button
				bind:this={rebuildBtn}
				disabled={!canRebuild || busy === 'rebuild'}
				title={canRebuild
					? 'Recreate the container in place — the run and its commits are untouched'
					: run.envId
						? 'Pause or stop the run first — a live run refuses a rebuild'
						: 'No workspace to rebuild'}
				onclick={() => (rebuildOpen = !rebuildOpen)}
				>{busy === 'rebuild' ? 'Rebuilding…' : 'Rebuild ▾'}</button
			>
		</GlassGroup>

		<GlassGroup>
			{#snippet label()}
				<span>Merge requests</span>
				{#if run.mrs.length || run.mrUrl}<MergeStatus runId={run.id} {active} />{/if}
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

<Popover anchor={envBtn} bind:open={envOpen} placement="below-end" width={270}>
	<div class="ph">Environment links</div>
	{#each rs.links as l (l.kind + (l.url ?? l.command ?? ''))}
		{#if l.url}
			<a class="mi" href={l.url} target="_blank" rel="noreferrer" title={l.url}>
				<span class="grow">{l.kind}</span><span>↗</span>
			</a>
		{:else if l.command}
			<button class="mi" title="Copy: {l.command}" onclick={() => copy(l)}>
				<span class="grow cmd">{l.command}</span>
				<span class="cp" class:ok={copied === l.kind}
					>{copied === l.kind ? 'copied ✓' : 'copy'}</span
				>
			</button>
		{/if}
	{/each}
</Popover>

<Popover anchor={rebuildBtn} bind:open={rebuildOpen} placement="below-end" width={300}>
	<button class="mi col" onclick={() => rebuild(false)}>
		<span>Rebuild workspace</span>
		<span class="sub"
			>Recreate the container in place, re-run setup. The run and its commits stay.</span
		>
	</button>
	<button class="mi col" onclick={() => rebuild(true)}>
		<span>Rebuild without cache</span>
		<span class="sub"
			>Every layer from scratch — for a moved base tag or a stale layer the cache can't see. Slow.</span
		>
	</button>
</Popover>

<RestartModal {rs} bind:open={restartOpen} onstarted={(id) => goto(to.run(id))} />

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
		border-radius: 17px;
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		padding: 8px 10px;
		position: relative;
		overflow: hidden;
	}
	.bt,
	.bb {
		font-size: 10.5px;
		color: var(--mut);
		display: flex;
		justify-content: space-between;
	}
	.bt .mono {
		font-size: 10.5px;
	}
	.bid {
		font-family: var(--mono);
		font-weight: 600;
		font-size: 21px;
		letter-spacing: -0.02em;
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
		border-radius: 9px;
		font-size: 11.5px;
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
		border-radius: 2px;
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
		font-size: 24px;
		overflow-wrap: anywhere;
	}
	h1 .accent {
		font-size: 29px;
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
		font-size: 12px;
		align-items: center;
		min-width: 0;
	}
	.tk {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		color: var(--tx);
	}
	.tx {
		color: var(--tx);
		font-weight: 500;
	}
	.hash {
		font-size: 11px;
		color: var(--fnt);
		font-weight: 500;
	}
	.br {
		font-size: 11.5px;
		font-weight: 400;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.br .tx {
		font-weight: 400;
	}
	.cost {
		font-size: 12px;
	}
	.groups {
		display: flex;
		gap: 8px;
		align-items: stretch;
		justify-content: flex-end;
		flex-wrap: wrap;
	}
	.arch {
		background: transparent;
		box-shadow: none;
		color: var(--mut);
		border-color: var(--ln);
	}
	.arch:hover {
		color: var(--tx);
	}
	.ph {
		padding: 5px 8px 6px;
		font-size: 11px;
		color: var(--mut);
	}
	.grow {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.cmd {
		font-family: var(--mono);
		font-size: 11.5px;
	}
	.cp {
		font-size: 11.5px;
		font-weight: 600;
		color: var(--mut);
	}
	.cp.ok {
		color: var(--grn);
	}
	:global(.pop) .col {
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		white-space: normal;
	}
	.col .sub {
		color: var(--mut);
		font-size: 11px;
		line-height: 1.35;
	}
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
	@container runpage (max-width: 640px) {
		.rbadge {
			display: none;
		}
		.rh {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
