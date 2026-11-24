<script lang="ts" module>
	/** How often the list is read while the pane shows. */
	export const POLL_MS = 5000;
	/** The sidecar refuses a session past this many. */
	export const MAX_SESSIONS = 16;
</script>

<script lang="ts">
	// The Terminal pane's sessions: the shells living in the run's workspace,
	// shared by everyone who may open one, as tabs. The list is read when the
	// pane comes on screen, after a create or a close, and every few seconds
	// while it shows, so others' sessions appear and a tab's "keeps the
	// workspace awake" marker stays current. The session shown is the one last
	// shown here if it is still listed, else the newest, else a new one; "+"
	// opens one in the checkout picked. A workspace whose sidecar predates
	// sessions (the list answers 501) gets today's single throwaway terminal.
	import { api } from '$lib/api';
	import { ApiError } from '$lib/api/client';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import type { RunCheckout, TerminalSession } from '$lib/domain';
	import { t, th } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Tabs, { type TabItem } from '$lib/ui/Tabs.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import Terminal from './Terminal.svelte';
	import { rememberSession, rememberedSession } from './terminalSessions.svelte';

	let {
		runId,
		repo = '',
		dir = '',
		checkouts = [],
		visible
	}: {
		runId: string;
		/** Where "+" opens a session ('' is the primary). */
		repo?: string;
		/** That checkout's directory, relative to the workspace home. */
		dir?: string;
		/** The run's checkouts, to say where each session opened. */
		checkouts?: RunCheckout[];
		visible: boolean;
	} = $props();

	let sessions = $state<TerminalSession[]>([]);
	let phase = $state<'loading' | 'ready' | 'legacy' | 'failed'>('loading');
	let failure = $state('');
	/** The session on screen; '' while a new one waits for its id, or none is. */
	let shown = $state('');
	/** The Terminal mounted: its key, and the session it attaches to ('' creates). */
	let view = $state<{ key: string; session: string } | null>(null);
	let created = 0;
	/** Lists asked for, and the last one whose answer is still news: an answer
	 *  asked before a create or a close here would undo it. */
	let asked = 0;
	let floor = 0;
	let alive = true;
	$effect(() => () => (alive = false));

	const current = $derived(sessions.find((s) => s.id === shown));
	const full = $derived(sessions.length >= MAX_SESSIONS);
	/** A session's checkout directory, relative to the workspace home ('' unknown). */
	const placeOf = (s: TerminalSession) =>
		(s.repo ? checkouts.find((c) => c.name === s.repo) : checkouts.find((c) => c.primary))?.dir ??
		'';
	const tabs = $derived.by((): TabItem<string>[] => {
		const items: TabItem<string>[] = sessions.map((s) => {
			const place = placeOf(s);
			return {
				value: s.id,
				label: [String(s.n), place && `~/${place}`, s.busy && s.command]
					.filter(Boolean)
					.join(' · '),
				count: s.attached > 1 ? s.attached : undefined,
				title:
					[
						s.busy
							? s.command
								? t('run.terminal.awake', { command: s.command })
								: t('run.terminal.awakeUnnamed')
							: '',
						s.attached > 1 ? t('run.terminal.attached', { count: s.attached }) : ''
					]
						.filter(Boolean)
						.join(' · ') || undefined,
				dot: s.busy ? 'running' : undefined
			};
		});
		if (view && !current) items.push({ value: shown, label: t('run.terminal.opening') });
		return items;
	});

	function show(id: string) {
		shown = id;
		view = { key: id, session: id };
		rememberSession(runId, id);
	}

	function create() {
		shown = '';
		view = { key: `new-${++created}`, session: '' };
	}

	/** Something changed here: answers asked before it are stale. */
	function changed() {
		floor = asked;
		void refresh();
	}

	async function refresh() {
		const mine = ++asked;
		let list: TerminalSession[];
		try {
			list = await api.workspace.terminals(runId);
		} catch (e) {
			if (!alive || mine <= floor) return;
			if (e instanceof ApiError && e.status === 501) phase = 'legacy';
			else if (phase !== 'ready') {
				phase = 'failed';
				failure = errorMessage(e);
			}
			return;
		}
		if (!alive || mine <= floor) return;
		floor = mine;
		const before = sessions;
		sessions = list;
		if (phase !== 'ready') {
			phase = 'ready';
			const kept = rememberedSession(runId);
			const id = list.some((s) => s.id === kept) ? kept : (list.at(-1)?.id ?? '');
			if (id) show(id);
			else create();
		} else if (shown && !list.some((s) => s.id === shown)) {
			// It ended: its shell exited, someone closed it, or the workspace stopped.
			toast(list.length ? t('run.terminal.endedToast') : t('run.terminal.workspaceRestarted'), {
				tone: list.length ? 'info' : 'warn'
			});
			const at = before.findIndex((s) => s.id === shown);
			const next = list.find((s) => before.findIndex((b) => b.id === s.id) > at) ?? list.at(-1);
			if (next) show(next.id);
			else create();
		}
	}

	// The list is read while the pane shows, and rests while it is hidden. (On
	// `legacy`, not `phase`: the first answer must not start a second timer.)
	const legacy = $derived(phase === 'legacy');
	$effect(() => {
		if (!visible || legacy) return;
		void refresh();
		const timer = setInterval(() => void refresh(), POLL_MS);
		return () => clearInterval(timer);
	});

	function opened(id: string) {
		shown = id;
		rememberSession(runId, id);
		changed();
	}

	async function close(s: TerminalSession) {
		const ok = await confirmDialog({
			title: t('run.terminal.closeTitle', { n: s.n }),
			body:
				s.busy && s.command
					? th('run.terminal.closeConfirmBusy', { command: s.command })
					: t('run.terminal.closeConfirm'),
			action: t('run.terminal.closeAction'),
			tone: 'danger'
		});
		if (!ok) return;
		const at = sessions.findIndex((x) => x.id === s.id);
		sessions = sessions.filter((x) => x.id !== s.id);
		if (s.id === shown) {
			const next = sessions[at] ?? sessions.at(-1);
			if (next) show(next.id);
			else {
				shown = '';
				view = null;
			}
		}
		try {
			await api.workspace.closeTerminal(runId, s.id);
		} catch (e) {
			// Gone already is what was asked for.
			if (!(e instanceof ApiError && e.status === 404))
				toast(t('run.terminal.closeFailed', { error: errorMessage(e) }), { tone: 'bad' });
		}
		changed();
	}
</script>

{#if legacy}
	<!-- No sessions to list: one shell, ending with this view, as before them. -->
	{#key repo}
		<Terminal {runId} {repo} {dir} legacy />
	{/key}
{:else if phase === 'failed'}
	<ErrorCard cause={t('run.terminal.listFailed')} detail={failure} onretry={() => void refresh()} />
{:else if phase === 'loading'}
	<Loading />
{:else}
	<div class="strip">
		<Tabs items={tabs} value={shown} onchange={show} label={t('run.terminal.sessions')} />
		<button
			class="btn-icon btn-sm"
			disabled={full}
			title={full ? t('run.terminal.full', { max: MAX_SESSIONS }) : t('run.terminal.newSession')}
			aria-label={t('run.terminal.newSession')}
			onclick={create}>{I.add}</button
		>
		{#if current}
			{@const s = current}
			<button
				class="btn-icon btn-sm"
				title={t('run.terminal.closeSession', { n: s.n })}
				aria-label={t('run.terminal.closeSession', { n: s.n })}
				onclick={() => close(s)}>{I.close}</button
			>
		{/if}
	</div>
	{#if view}
		{#key view.key}
			<Terminal
				{runId}
				repo={current?.repo ?? repo}
				dir={current ? placeOf(current) : dir}
				session={view.session}
				onsession={opened}
				onended={changed}
			/>
		{/key}
	{:else}
		<EmptyState compact title={t('run.terminal.none')}>
			<button class="btn btn-sm" onclick={create}>{I.add} {t('run.terminal.newSession')}</button>
		</EmptyState>
	{/if}
{/if}

<style>
	.strip {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 0 8px;
		min-width: 0;
	}
	.strip > :global(.tabs) {
		flex: 1;
		min-width: 0;
	}
</style>
