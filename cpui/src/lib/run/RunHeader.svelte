<script lang="ts">
	// The run's top bar (DESIGN 4.5, 7.1): one glass strip. The title block —
	// breadcrumb, a small title, a status line — then two inset groups that keep
	// the RUN's lifecycle and the WORKSPACE's apart, then the MR island. The
	// title gives context; the windows hold the information.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { to } from '$lib/urls';
	import { fleet } from '$lib/fleet.svelte';
	import { onMount } from 'svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import type { MergeRequestLive, RunSsh } from '$lib/domain';
	import { ago, fmtAbs, fmtClockDuration, fmtCost, shortId } from '$lib/format';
	import { I } from '$lib/ui/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import Breadcrumbs, { type Crumb } from '$lib/ui/Breadcrumbs.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import type { DeskState } from '$lib/desk/desk.svelte';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import MrIsland, { type MrRow } from './MrIsland.svelte';
	import RerunModal from './RerunModal.svelte';
	import { accentSplit, vscodeRemoteSettings } from './derive';
	import type { RunStore } from './store.svelte';

	let { rs, desk, onretried }: { rs: RunStore; desk: DeskState; onretried: () => void } = $props();

	const run = $derived(rs.run!);
	const project = $derived(run.projectId ? fleet.projectById.get(run.projectId) : undefined);
	const sid = $derived(shortId(run.id));
	const [plain, accent] = $derived(accentSplit(run.title));
	const ticketKey = $derived(rs.ticket?.source?.key);
	const crumbs = $derived<Crumb[]>([
		{ label: t('nav.desk'), href: to.desk() },
		...(run.projectId
			? [
					{
						label: project?.name ?? (rs.projectName || t('run.header.workshopFallback')),
						href: to.workshop(run.projectId)
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
		fmtClockDuration(
			(endedAt ? new Date(endedAt).getTime() : rs.now) - new Date(run.createdAt).getTime()
		)
	);
	const active = $derived(!rs.terminal);
	const wsName = $derived(run.envId ?? t('run.header.theWorkspace'));

	// The stage a retry re-enters: where the run stopped.
	const retryStage = $derived(
		run.currentStage ??
			(rs.events.findLast((e) => e.type === 'stage_failed')?.payload.stage_id as string | undefined)
	);

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

	// The SSH door's facts for this run (TODO 46), read each time the Env menu
	// opens: a key registered in another tab counts at the next open. The last
	// answer stays on screen while a fresh one is on its way.
	let ssh = $state<RunSsh | null>(null);
	let sshState = $state<'idle' | 'loading' | 'ready' | 'failed'>('idle');
	let sshFor = '';
	$effect(() => {
		if (!envOpen) return;
		const id = run.id;
		if (sshFor !== id) {
			sshFor = id;
			ssh = null;
		}
		let stale = false;
		sshState = 'loading';
		api.runs.ssh(id).then(
			(s) => {
				if (stale) return;
				ssh = s;
				sshState = 'ready';
			},
			() => {
				if (!stale) sshState = 'failed';
			}
		);
		return () => {
			stale = true;
		};
	});
	// The line that pins the door's host key in ~/.ssh/known_hosts.
	const knownHosts = $derived(
		ssh?.enabled && ssh.host && ssh.hostKey
			? `${ssh.port && ssh.port !== 22 ? `[${ssh.host}]:${ssh.port}` : ssh.host} ${ssh.hostKey}`
			: ''
	);

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

	async function copy(
		l: { kind: string; command?: string },
		said = t('run.env.copied', { kind: l.kind })
	) {
		if (!l.command) return;
		try {
			await navigator.clipboard.writeText(l.command);
			toast(said);
			copied = l.kind;
			setTimeout(() => copied === l.kind && (copied = ''), 1500);
		} catch {
			rs.error = t('run.env.copyFailed');
		}
	}

	const canRebuild = $derived(
		!!run.envId && run.state !== 'archived' && (rs.terminal || run.state === 'paused')
	);
	// The run's merge requests: the one it was created on, the ones it opened
	// and — only when its flow opens merge requests — a "not yet" slot for each
	// checkout still without one. A run that has none and whose flow opens none
	// shows no island at all. Live status joins by URL; merged ones sink to the
	// end, so the pill leads with what is still in play.
	const mrRows = $derived.by(() => {
		const rows: MrRow[] = [];
		const seen = new Set<string>();
		const live = (url: string) => mrLive.find((m) => m.url === url);
		const a = run.adopted;
		if (a) {
			seen.add(a.repo);
			rows.push({
				key: a.url,
				label: `!${a.iid}`,
				url: a.url,
				title: t('run.mr.adopted', { title: a.title || `!${a.iid}`, author: a.author ?? '?' }),
				live: live(a.url)
			});
		}
		for (const mr of run.mrs) {
			seen.add(mr.repo);
			if (mr.url === a?.url) continue;
			rows.push({
				key: mr.url,
				label: `!${mr.iid}`,
				url: mr.url,
				title: `${mr.repo !== 'repo' ? mr.repo + ': ' : ''}${mr.branch}${mr.target ? ' → ' + mr.target : ''}`,
				live: live(mr.url)
			});
		}
		for (const c of rs.opensMRs ? run.checkouts : []) {
			const key = c.primary ? 'repo' : c.name;
			if (seen.has(key) || seen.has(c.name)) continue;
			rows.push({ key: c.name, label: I.none, title: t('run.mr.notOpenedRepo', { name: c.name }) });
		}
		// Runs from before multi-repo carry only mrUrl, no mrs list.
		if (!rows.length && run.mrUrl)
			rows.push({
				key: run.mrUrl,
				label: t('run.mr.one'),
				url: run.mrUrl,
				title: run.branch ?? t('run.mr.title'),
				live: live(run.mrUrl)
			});
		if (!rows.length && rs.opensMRs)
			rows.push({ key: 'none', label: I.none, title: t('run.mr.notOpened') });
		return [...rows].sort(
			(x, y) =>
				Number(!x.url) - Number(!y.url) ||
				Number(x.live?.state === 'merged') - Number(y.live?.state === 'merged')
		);
	});
	const mrTarget = $derived.by(() => {
		const m = run.mrs[0];
		if (!m) return undefined;
		return `${project?.name ?? m.repo} → ${m.target ?? run.sourceBranch ?? t('run.header.default')}`;
	});
</script>

<header class="rh c">
	<div class="title">
		<div class="crow">
			<Breadcrumbs {crumbs} />
			{#if run.ticketId}
				<span class="dot-sep">·</span>
				<a class="link" href={to.ticket(run.ticketId)} title={rs.ticket?.title}
					>{ticketKey ?? t('run.header.ticket')} {I.external}</a
				>
			{/if}
			{#if run.ticketUrl}
				<a class="link" href={run.ticketUrl} target="_blank" rel="noreferrer"
					>{t('run.header.tracker')} {I.external}</a
				>
			{/if}
		</div>
		<h1 title={run.title}>
			{plain}{#if accent}{' '}<span class="accent">{accent}</span>{/if}
		</h1>
		<div class="status">
			<StatusChip state={run.state} size="sm" suffix={run.state === 'failed'} />
			{#if run.hold && !rs.terminal}
				<span
					class="pill sm t-sky"
					title={t('run.header.heldTitle', { ago: ago(run.hold.since, rs.now) })}
					><span class="ldot sm pulse hue-sky"></span>{t('run.header.held')} ·
					<span class="tick">{fmtClockDuration(rs.now - new Date(run.hold.since).getTime())}</span
					></span
				>
			{/if}
			<span class="mono stats tick">
				<span
					class:hue-emb={active}
					title={t('run.header.started', {
						at: fmtAbs(run.createdAt),
						ago: ago(run.createdAt, rs.now)
					})}>{brewed}</span
				>
				· {fmtCost(rs.totals.cost)} · {t('run.header.turns', { count: rs.totals.turns })}
			</span>
			{#if run.skillsets?.length}
				<span class="sk" title={t('run.header.skillsets')}
					>{#each run.skillsets as p (p.id)}<span class="pill sm t-mut"
							>{p.name} <span class="mono">v{p.seq}</span></span
						>{/each}</span
				>
			{/if}
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
						title={t('run.header.playTitle')}
						><TransportIcon kind="play" /> {t('run.header.play')}</button
					>
				{:else if run.state !== 'pending'}
					<button
						disabled={busy !== ''}
						aria-busy={busy === 'pause'}
						onclick={() => act('pause', () => api.runs.pause(run.id))}
						title={t('run.header.pauseTitle')}
						><TransportIcon kind="pause" /> {t('run.header.pause')}</button
					>
				{/if}
				<button class="btn-danger" disabled={busy !== ''} aria-busy={busy === 'stop'} onclick={stop}
					><TransportIcon kind="stop" /> {t('run.header.stop')}</button
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
				disabled={!rs.hasWorkspace}
				title={rs.hasWorkspace ? t('run.env.links') : t('run.env.noLinks')}
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

		{#if mrRows.length}<MrIsland
				rows={mrRows}
				target={mrTarget}
				multiRepo={mrLive.length > 1}
			/>{/if}
	</div>
</header>

<Popover
	anchor={envBtn}
	bind:open={envOpen}
	placement="below-end"
	width={300}
	title={t('run.env.title')}
	legend={rs.links.length ? t('run.env.legend', { count: rs.links.length }) : undefined}
>
	<!-- Into the workspace from a desktop (TODO 46): the VS Code link, the
	     ssh command, the door's host key — or what stands in the way. -->
	{#if ssh === null}
		<div class="mi still" aria-busy={sshState === 'loading'}>
			<span class="sub"
				>{sshState === 'failed' ? t('run.env.sshFailed') : t('run.env.sshLoading')}</span
			>
		</div>
	{:else if !ssh.enabled}
		<div class="mi still">
			<span class="col wrap"
				><span>{t('run.env.sshOff')}</span><span class="sub">{ssh.reason}</span></span
			>
		</div>
	{:else if ssh.keys === 0}
		<a class="mi" href={to.account()}>
			<span class="col"
				><span>{t('run.env.sshNoKey')}</span><span class="sub">{t('run.env.sshNoKeySub')}</span
				></span
			>
			<span class="g">{I.forward}</span>
		</a>
	{:else}
		{@const s = ssh}
		{#if s.vscodeUrl}
			{@const settingsLines = vscodeRemoteSettings(s.extensions)}
			<a class="mi" href={s.vscodeUrl} title={s.vscodeUrl}>
				<span class="col"
					><span>{t('run.env.vscode')}</span><span class="sub ell"
						>{s.path ?? t('run.env.vscodeSub')}</span
					></span
				>
				<span class="g">{I.external}</span>
			</a>
			<!-- The desktop's extensions follow it in: two settings.json lines,
			     the list pre-filled with what the workspace's editor runs. -->
			<button
				class="mi"
				title={t('run.env.copyTitle', { command: settingsLines })}
				onclick={() =>
					copy({ kind: 'settings', command: settingsLines }, t('run.env.copiedVscodeSettings'))}
			>
				<span class="col"
					><span>{t('run.env.vscodeExts')}</span><span class="sub mono ell"
						>{t('run.env.vscodeExtsSub')}</span
					></span
				>
				<span class="cp" class:hue-grn={copied === 'settings'}
					>{copied === 'settings' ? `${t('run.env.copiedLabel')} ${I.ok}` : t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if s.command}
			<button
				class="mi"
				title={t('run.env.copyTitle', { command: s.command })}
				onclick={() => copy({ kind: 'SSH', command: s.command })}
			>
				<span class="col"
					><span>{t('run.env.sshCommand')}</span><span class="sub mono ell">{s.command}</span></span
				>
				<span class="cp" class:hue-grn={copied === 'SSH'}
					>{copied === 'SSH' ? `${t('run.env.copiedLabel')} ${I.ok}` : t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if knownHosts}
			<button
				class="mi"
				title={t('run.env.copyTitle', { command: knownHosts })}
				onclick={() =>
					copy({ kind: 'known_hosts', command: knownHosts }, t('run.env.copiedKnownHosts'))}
			>
				<span class="col"
					><span>{t('run.env.sshKnownHosts')}</span><span class="sub mono ell">{knownHosts}</span
					></span
				>
				<span class="cp" class:hue-grn={copied === 'known_hosts'}
					>{copied === 'known_hosts'
						? `${t('run.env.copiedLabel')} ${I.ok}`
						: t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if s.fingerprint}
			<div class="mi still">
				<span class="sub mono ell" title={s.fingerprint}
					>{t('run.env.sshFingerprint', { fingerprint: s.fingerprint })}</span
				>
			</div>
		{/if}
	{/if}
	<!-- What the workspace serves: its app ports and service previews. -->
	{#if rs.links.length}
		<div class="menu-div"></div>
		{#each rs.links as l (l.kind + l.url)}
			<a class="mi" href={l.url} target="_blank" rel="noreferrer" title={l.url}>
				<span class="col"><span>{l.kind}</span><span class="sub ell">{l.url}</span></span>
				<span class="g">{I.external}</span>
			</a>
		{/each}
	{/if}
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
		position: relative;
		z-index: var(--z-bar);
		flex: none;
		border-radius: var(--r-pane);
		padding: 8px 10px 8px 16px;
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.title {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.crow {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		font-size: var(--fs-help);
		white-space: nowrap;
	}
	.crow :global(.bc) {
		flex: 0 1 auto;
		font-size: var(--fs-help);
	}
	.crow :global(.bc > *) {
		flex: 0 1 auto;
		min-width: 0;
	}
	.crow :global(.bc > .sep) {
		flex: none;
	}
	.dot-sep {
		color: var(--fnt);
	}
	.crow .link {
		flex: none;
	}
	h1 {
		min-width: 0;
		font-size: var(--fs-mark);
		letter-spacing: var(--tr-run);
		line-height: 1.15;
		text-wrap: nowrap;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.status {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
	}
	.stats {
		color: var(--mut);
	}
	.sk {
		display: inline-flex;
		gap: 4px;
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
	.cp {
		font-size: var(--fs-help);
		font-weight: 600;
		color: var(--mut);
	}
	.cp.hue-grn {
		color: var(--grn);
	}
	/* A row that only says something: no hover, nothing to press. */
	.still {
		cursor: default;
	}
	.still:hover {
		background: none;
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
	@container runpage (max-width: 1000px) {
		.rh {
			flex-wrap: wrap;
			padding-right: 10px;
		}
		.title {
			flex-basis: 100%;
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
		h1 {
			white-space: normal;
			text-wrap: balance;
			display: -webkit-box;
			-webkit-box-orient: vertical;
			-webkit-line-clamp: 3;
			line-clamp: 3;
		}
		.status {
			flex-wrap: wrap;
			row-gap: 4px;
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
	/* The devcontainer config moved on since the build: the rebuild is due. */
	.dirty {
		display: inline-block;
		width: 7px;
		height: 7px;
		margin-right: 6px;
		border-radius: 50%;
		background: var(--gold);
		box-shadow: 0 0 6px var(--gold);
		vertical-align: middle;
	}
</style>
