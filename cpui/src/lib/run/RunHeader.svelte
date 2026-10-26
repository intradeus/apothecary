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
	import { t, th } from '$lib/i18n/index.svelte';
	import type { MergeRequestLive } from '$lib/domain';
	import { ago, fmtAbs, fmtCost, fmtHoursMinutes, shortId, stateHue } from '$lib/format';
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
	import RerunModal from './RerunModal.svelte';
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
						label: project?.name ?? (rs.projectName || t('run.header.workshopFallback')),
						href: to.workshop(run.projectId)
					}
				]
			: []),
		...(run.ticketId
			? [
					{
						label: ticketKey ?? rs.ticket?.title ?? t('run.header.ticketFallback'),
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
	const wsName = $derived(run.envId ?? t('run.header.theWorkspace'));

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
	let rerunOpen = $state(false);
	let busy = $state<'' | 'pause' | 'play' | 'stop' | 'retry' | 'archive' | 'editor' | 'rebuild'>(
		''
	);
	let copied = $state('');

	// Hold the live ports and services feeds while the Env menu is open (its
	// app links, a healthy service's preview links).
	$effect(() => {
		if (!envOpen) return;
		const ports = rs.usePorts();
		const services = rs.useServices();
		return () => (ports(), services());
	});

	async function act(which: typeof busy, fn: () => Promise<unknown>): Promise<boolean> {
		busy = which;
		const ok = await rs.act(fn);
		busy = '';
		return ok;
	}

	// What the repository host says about the run's merge requests right now, polled
	// while the run is alive; an observation, not part of the run's record.
	let mrLive = $state<MergeRequestLive[]>([]);
	const hasMr = $derived(run.mrs.length > 0 || !!run.mrUrl);
	async function loadMrs() {
		if (!hasMr) return;
		try {
			mrLive = await api.runs.mrStatus(run.id);
		} catch {
			// The header must not break on a repository host hiccup; the next poll retries.
		}
	}
	onMount(() => {
		void loadMrs();
		const timer = setInterval(() => active && !document.hidden && loadMrs(), 30_000);
		return () => clearInterval(timer);
	});
	const mrShown = $derived([
		...mrLive.filter((m) => m.state === 'open' || m.error),
		...mrLive.filter((m) => m.state === 'merged' && !m.error)
	]);

	async function stop() {
		const ok = await confirmDialog({
			title: t('run.stop.title', { id: sid }),
			body: `${th('run.stop.lead')}<br><br>${
				run.envId ? th('run.stop.envUp', { env: run.envId }) : t('run.stop.noEnv')
			} ${t('run.stop.kept')}`,
			action: t('run.stop.action')
		});
		if (ok) await act('stop', () => api.runs.abort(run.id));
	}

	async function archive() {
		const ok = await confirmDialog({
			title: t('run.archive.title', { id: sid }),
			body: `${th('run.archive.lead')}<br><br>${
				run.envId ? th('run.archive.envDestroyed', { env: run.envId }) : t('run.archive.noEnv')
			}`,
			action: t('run.archive.action')
		});
		if (ok && (await act('archive', () => api.runs.archive(run.id))))
			toast(t('run.archive.done', { id: sid }), {
				action: run.ticketId
					? { label: t('run.archive.openTicket'), run: () => goto(to.ticket(run.ticketId!)) }
					: undefined
			});
	}

	async function retry() {
		busy = 'retry';
		// Same run, same workspace: the page re-subscribes once it continues.
		if (await rs.act(() => api.runs.retry(run.id))) onretried();
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
		toast(noCache ? t('run.rebuild.startedNoCache') : t('run.rebuild.started'), {
			tone: 'info',
			action: { label: t('run.rebuild.watchLogs'), run: () => desk.reveal('logs') }
		});
		if (await act('rebuild', () => api.runs.rebuild(run.id, noCache))) toast(t('run.rebuild.done'));
	}

	async function copy(l: { kind: string; command?: string }) {
		if (!l.command) return;
		try {
			await navigator.clipboard.writeText(l.command);
			toast(t('run.env.copied', { kind: l.kind }));
			copied = l.kind;
			setTimeout(() => copied === l.kind && (copied = ''), 1500);
		} catch {
			rs.error = t('run.env.copyFailed');
		}
	}

	const canRebuild = $derived(
		!!run.envId && run.state !== 'archived' && (rs.terminal || run.state === 'paused')
	);
	// The run's merge requests: the one it was created on, the ones it
	// opened and — only when its flow opens merge requests — a "not yet" slot
	// for each checkout still without one. A run that has none and whose flow
	// opens none shows no slot at all.
	const mrSlots = $derived.by(() => {
		const n = Math.max(rs.opensMRs ? run.checkouts.length : 0, run.mrs.length, 1);
		const slots: { label: string; url?: string; title: string }[] = [];
		const seen = new Set<string>();
		const a = run.adopted;
		if (a)
			slots.push({
				label: `!${a.iid} ${I.external}`,
				url: a.url,
				title: t('run.mr.adopted', { title: a.title || `!${a.iid}`, author: a.author ?? '?' })
			});
		const name = (i: number) => (n > 1 ? t('run.mr.slot', { i: i + 1, n }) : t('run.mr.one'));
		run.mrs.forEach((mr, i) => {
			seen.add(mr.repo);
			if (mr.url === a?.url) return;
			slots.push({
				label: `${name(i)} ${I.external}`,
				url: mr.url,
				title: `${mr.repo !== 'repo' ? mr.repo + ': ' : ''}${mr.branch}${mr.target ? ' → ' + mr.target : ''}`
			});
		});
		for (const c of rs.opensMRs ? run.checkouts : []) {
			const key = c.primary ? 'repo' : c.name;
			if (seen.has(key) || seen.has(c.name) || slots.length >= n) continue;
			slots.push({
				label: `${name(slots.length)} ${I.none}`,
				title: t('run.mr.notOpenedRepo', { name: c.name })
			});
		}
		// Runs from before multi-repo carry only mrUrl, no mrs list.
		if (!slots.length && run.mrUrl)
			slots.push({
				label: `${t('run.mr.one')} ${I.external}`,
				url: run.mrUrl,
				title: run.branch ?? t('run.mr.title')
			});
		if (!slots.length && rs.opensMRs)
			slots.push({ label: `${t('run.mr.one')} ${I.none}`, title: t('run.mr.notOpened') });
		return slots;
	});
</script>

<header class="rh">
	<div class="rbadge lg" style:--glow={glow}>
		<div class="bt"><span>{t('run.header.batch')}</span><span class="mono">Nº</span></div>
		<div class="bid">{shortId(run.id, '')}</div>
		<div
			class="bb tick"
			title={t('run.header.started', {
				at: fmtAbs(run.createdAt),
				ago: ago(run.createdAt, rs.now)
			})}
		>
			{t(rs.terminal ? 'run.header.brewed' : 'run.header.brewing', { duration: brewed })}
		</div>
		<span class="bg"></span>
	</div>

	<div class="mid">
		<div class="crow">
			<Breadcrumbs {crumbs} />
			<!-- A narrow screen has one window: no layout to pick. -->
			{#if !desk.compact}<button
					bind:this={splitBtn}
					class="btn-sel layout"
					title={t('run.header.layouts')}
					aria-expanded={desk.menuOpen}
					onclick={() => (desk.menuOpen = !desk.menuOpen)}
				>
					<span class="lg-ic"><span></span><span></span><span></span></span>{desk.label}
					{I.menu}
				</button>{/if}
		</div>
		<div class="trow">
			<h1 title={run.title}>
				{plain}{#if accent}{' '}<span class="accent">{accent}</span>{/if}
			</h1>
			<StatusChip state={run.state} suffix />
		</div>
		<div class="meta">
			{#if run.ticketId}
				<a class="tk tx" href={to.ticket(run.ticketId)}
					>{#if ticketKey}<span class="tag">{ticketKey}</span>{/if}{t('run.header.ticket')}
					{I.external}</a
				>
			{/if}
			{#if run.ticketUrl}
				<a class="tx" href={run.ticketUrl} target="_blank" rel="noreferrer"
					>{t('run.header.tracker')} {I.external}</a
				>
			{/if}
			<span
				>{t('run.header.flow')} <span class="tx">{run.flowName}</span>{#if run.flowHash}
					<span class="mono hash fnt">@{run.flowHash.slice(0, 6)}</span>{/if}</span
			>
			{#if run.branch}
				<span
					class="mono br ell"
					title={t('run.header.branchTitle', {
						source: run.sourceBranch ?? t('run.header.theDefault')
					})}
					><span class="tx">{run.branch}</span> ← {run.sourceBranch ??
						t('run.header.default')}</span
				>
			{/if}
			<span
				><span class="mono tx cost">{fmtCost(rs.totals.cost)}</span> · {t('run.header.turns', {
					count: rs.totals.turns
				})}</span
			>
		</div>
	</div>

	<div class="groups">
		<GlassGroup label={t('run.header.groupRun')}>
			{#if active}
				{#if run.state === 'paused'}
					<button
						class="btn-primary"
						disabled={busy !== ''}
						aria-busy={busy === 'play'}
						onclick={() => act('play', () => api.runs.play(run.id))}
						title={t('run.header.playTitle')}>{I.play} {t('run.header.play')}</button
					>
				{:else if run.state !== 'pending'}
					<button
						disabled={busy !== ''}
						aria-busy={busy === 'pause'}
						onclick={() => act('pause', () => api.runs.pause(run.id))}
						title={t('run.header.pauseTitle')}>{I.pause} {t('run.header.pause')}</button
					>
				{/if}
				<button class="btn-danger" disabled={busy !== ''} aria-busy={busy === 'stop'} onclick={stop}
					>{I.stop} {t('run.header.stop')}</button
				>
			{:else}
				{#if run.state === 'failed' || run.state === 'abandoned'}
					<button
						class="btn-primary"
						disabled={busy !== ''}
						aria-busy={busy === 'retry'}
						onclick={retry}
						title={t('run.header.retryTitle', {
							stage: retryStage ?? t('run.header.theFailedStage')
						})}
						>{I.retry}
						{retryStage
							? t('run.header.retryStage', { stage: retryStage })
							: t('run.header.retry')}</button
					>
				{/if}
				<button
					disabled={busy !== ''}
					onclick={() => (rerunOpen = true)}
					title={t('run.header.rerunTitle')}>{t('run.header.rerun')}</button
				>
				{#if run.state !== 'archived'}
					<button
						class="btn-danger"
						disabled={busy !== ''}
						aria-busy={busy === 'archive'}
						onclick={archive}>{t('run.header.archive')}</button
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
				title={rs.links.length ? t('run.env.links') : t('run.env.noLinks')}
				aria-expanded={envOpen}
				onclick={() => (envOpen = !envOpen)}>{t('run.env.button')} {I.menu}</button
			>
			<button
				disabled={!rs.hasWorkspace || busy === 'editor'}
				aria-busy={busy === 'editor'}
				onclick={openEditor}
				title={t('run.header.editorTitle')}>{t('run.header.editor')} {I.external}</button
			>
			<button
				bind:this={rebuildBtn}
				disabled={!canRebuild || busy === 'rebuild'}
				aria-busy={busy === 'rebuild'}
				aria-expanded={rebuildOpen}
				title={run.envDirty
					? t(canRebuild ? 'run.rebuild.dirtyTitle' : 'run.rebuild.dirtyTitleBusy')
					: canRebuild
						? t('run.rebuild.title')
						: run.envId
							? t('run.rebuild.pauseFirst')
							: t('run.rebuild.noWorkspace')}
				onclick={() => (rebuildOpen = !rebuildOpen)}
				>{#if run.envDirty}<span class="dirty" aria-hidden="true"></span>{/if}{t(
					'run.rebuild.button'
				)}
				{I.menu}</button
			>
		</GlassGroup>

		{#if mrSlots.length}<GlassGroup>
				{#snippet label()}
					<span>{t('run.mr.requests')}</span>
					{#each mrShown as mr (mr.repo + mr.branch)}<MrStatus
							{mr}
							repo={mrLive.length > 1}
						/>{/each}
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
			</GlassGroup>{/if}
	</div>
</header>

<LayoutsPopover {desk} anchor={splitBtn} />

<Popover
	anchor={envBtn}
	bind:open={envOpen}
	placement="below-end"
	width={300}
	title={t('run.env.title')}
	legend={t('run.env.legend', { count: rs.links.length })}
>
	{#each rs.links as l (l.kind + (l.url ?? l.command ?? ''))}
		{#if l.url}
			<a class="mi" href={l.url} target="_blank" rel="noreferrer" title={l.url}>
				<span class="col"><span>{l.kind}</span><span class="sub ell">{l.url}</span></span>
				<span class="g">{I.external}</span>
			</a>
		{:else if l.command}
			<button
				class="mi"
				title={t('run.env.copyTitle', { command: l.command })}
				onclick={() => copy(l)}
			>
				<span class="col"><span>{l.kind}</span><span class="sub mono ell">{l.command}</span></span>
				<span class="cp" class:hue-grn={copied === l.kind}
					>{copied === l.kind ? `${t('run.env.copiedLabel')} ${I.ok}` : t('run.env.copy')}</span
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
	title={t('run.rebuild.popTitle')}
	legend={run.envDirty ? t('run.rebuild.legendDirty') : t('run.rebuild.legendClean')}
>
	<button class="mi" onclick={() => rebuild(false)}>
		<span class="col wrap">
			<span>{t('run.rebuild.workspace')}</span>
			<span class="sub">{t('run.rebuild.workspaceSub')}</span>
		</span>
	</button>
	<button class="mi" onclick={() => rebuild(true)}>
		<span class="col wrap">
			<span>{t('run.rebuild.noCache')}</span>
			<span class="sub">{t('run.rebuild.noCacheSub')}</span>
		</span>
	</button>
</Popover>

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
	/* Three lines at most — a merge request's title and its author's handle
	   can run long; the whole of it is the tooltip. */
	h1 {
		min-width: 0;
		font-size: var(--fs-empty);
		overflow-wrap: anywhere;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 3;
		line-clamp: 3;
		overflow: hidden;
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
			padding: 10px 12px 8px;
			gap: 10px;
		}
		/* A smaller title, the state under it rather than squeezed beside
		   it. */
		.trow {
			flex-direction: column;
			align-items: flex-start;
			gap: 6px;
		}
		h1 {
			font-size: var(--fs-section);
			line-height: 1.25;
		}
		h1 .accent {
			font-size: var(--fs-mark);
		}
		/* Run, workspace and merge requests as one card, a row each: what it
		   is on the left, what can be done on the right. */
		.groups {
			flex-direction: column;
			gap: 0;
		}
		.groups > :global(.gg) {
			flex-direction: row;
			align-items: center;
			flex-wrap: wrap;
			gap: 6px 10px;
			padding: 7px 8px 7px 12px;
			border-radius: 0;
		}
		.groups > :global(.gg + .gg) {
			margin-top: -1px;
		}
		.groups > :global(.gg:first-child) {
			border-top-left-radius: var(--r-group);
			border-top-right-radius: var(--r-group);
		}
		.groups > :global(.gg:last-child) {
			border-bottom-left-radius: var(--r-group);
			border-bottom-right-radius: var(--r-group);
		}
		.groups > :global(.gg .lbl) {
			min-width: 0;
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
	/* The devcontainer config moved on since the build: the rebuild is due. */
	.dirty {
		display: inline-block;
		width: 7px;
		height: 7px;
		margin-right: 6px;
		border-radius: 50%;
		background: var(--amb);
		box-shadow: 0 0 6px var(--amb);
		vertical-align: middle;
	}
</style>
