<script lang="ts">
	// Ask (DESIGN §6): read-only sessions beside the run that answer
	// questions about the code as it stands. A run holds several, each its
	// own conversation and model, picked at the top right; they are the
	// run's, kept with it. One question at a time per session: its answer
	// streams in, then its box is yours again, while another session may be
	// answering beside it.
	import { errorMessage } from '$lib/core/errors';
	import { tick } from 'svelte';
	import { api } from '$lib/api';
	import type { AskMessage, AskSession, AskState } from '$lib/domain';
	import { fmtClock, fmtCost } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Select from '$lib/ui/Select.svelte';
	import AskComposer from '../AskComposer.svelte';
	import FileChips, { type StoredFile } from '$lib/artifacts/FileChips.svelte';
	import type { RunStore } from '../store.svelte';
	import { pathClick, type PathRef } from '$lib/core/paths';

	let {
		rs,
		visible,
		onpath
	}: {
		rs: RunStore;
		visible: boolean;
		/** Open a file an answer names: its paths become buttons. */
		onpath?: (ref: PathRef) => void;
	} = $props();
	const clickPath = $derived(pathClick(onpath));

	let pane = $state<AskState | null>(null);
	let loadError = $state('');
	let error = $state('');
	let pickingModel = $state(false);
	let creating = $state(false);
	/** The session on screen ('': none yet — the run has no session). */
	let selected = $state('');
	/** Each session's answer being written: its text so far, and what the
	 *  agent is doing. */
	let lives = $state<Record<string, { text: string; tool: string }>>({});
	let scroller = $state<HTMLDivElement | null>(null);

	const sessions = $derived(pane?.sessions ?? []);
	const current = $derived(sessions.find((s) => s.id === selected) ?? null);
	const messages = $derived((pane?.messages ?? []).filter((m) => m.sessionId === selected));
	const live = $derived(lives[selected] ?? null);
	const enabled = $derived(!!pane?.enabled);
	const busy = $derived(!!current?.busy);
	const modelOptions = $derived((pane?.models ?? []).map((m) => ({ value: m, label: m })));

	/** A session reads as its first question. */
	function title(s: AskSession): string {
		const first = pane?.messages.find((m) => m.sessionId === s.id && m.role === 'question');
		const text = first?.text.replace(/\s+/g, ' ').trim() ?? '';
		if (!text) return t('runview.ask.untitled');
		return text.length > 80 ? `${text.slice(0, 79)}…` : text;
	}

	const sessionOptions = $derived(
		sessions.map((s) => ({
			value: s.id,
			label: title(s),
			detail: fmtClock(s.createdAt),
			dot: s.busy ? ('running' as const) : undefined
		}))
	);

	/** Whether some of these files are ones the run's list has not seen yet
	 *  (a question asked elsewhere since it was read). */
	const unseen = (ids: string[]) => ids.some((id) => !rs.attachments.some((a) => a.id === id));

	async function load() {
		try {
			const got = await api.ask.state(rs.runId);
			// The session on screen stays while the run still has it; else the
			// latest.
			if (!got.sessions.some((s) => s.id === selected)) selected = got.sessions.at(-1)?.id ?? '';
			pane = got;
			loadError = '';
			if (got.messages.some((m) => unseen(m.attachmentIds))) void rs.refreshFiles();
		} catch (e) {
			// A conversation on screen outlives a failed re-read.
			if (!pane) loadError = errorMessage(e);
		}
	}

	function toBottom() {
		tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
	}

	/** Adds a session the pane has not seen, or brings one up to date. */
	function upsert(s: AskSession) {
		if (!pane) return;
		const had = pane.sessions.find((x) => x.id === s.id);
		if (had) {
			had.model = s.model ?? had.model;
			had.harnessSession = s.harnessSession ?? had.harnessSession;
			had.createdAt = s.createdAt;
		} else {
			pane.sessions = [...pane.sessions, s];
		}
		if (!selected) selected = s.id;
	}

	function session(id: string): AskSession | undefined {
		return pane?.sessions.find((s) => s.id === id);
	}

	// Load when shown; follow the stream while shown.
	$effect(() => {
		if (!visible) return;
		void load().then(toBottom);
		const stop = api.ask.stream(
			rs.runId,
			(f) => {
				if (!pane) return;
				if (f.kind === 'message') {
					if (!pane.messages.some((m) => m.seq === f.message.seq)) {
						pane.messages = [...pane.messages, f.message].sort((a, b) => a.seq - b.seq);
					}
					// A question asked elsewhere brings files this run's list
					// has not seen yet.
					if (unseen(f.message.attachmentIds)) void rs.refreshFiles();
					if (f.message.role === 'answer') delete lives[f.sessionId];
					if (f.sessionId === selected) toBottom();
				} else if (f.kind === 'delta') {
					lives[f.sessionId] = { text: f.text, tool: f.tool ?? '' };
					if (f.sessionId === selected) toBottom();
				} else if (f.kind === 'state') {
					const s = session(f.sessionId);
					if (s) {
						s.busy = f.busy;
						if (f.model) s.model = f.model;
					}
					if (!f.busy) delete lives[f.sessionId];
				} else {
					upsert(f.session);
				}
			},
			() => void load()
		);
		return stop;
	});

	/** The engine took a question: the pane follows it to its session (a
	 *  first question opened one) and waits on the answer. */
	function asked(q: AskMessage, into: string) {
		if (!session(q.sessionId)) upsert({ id: q.sessionId, busy: true, createdAt: q.at });
		if (!into) selected = q.sessionId;
		const s = session(q.sessionId);
		if (s) s.busy = true;
		lives[q.sessionId] = { text: '', tool: '' };
		toBottom();
	}

	async function newSession() {
		if (creating || !messages.length) return;
		creating = true;
		error = '';
		try {
			const s = await api.ask.newSession(rs.runId);
			upsert(s);
			selected = s.id;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			creating = false;
		}
	}

	async function pickModel(model: string) {
		if (!pane || model === (current?.model ?? '')) return;
		pickingModel = true;
		error = '';
		try {
			await api.ask.setModel(rs.runId, model, selected || undefined);
			if (current) current.model = model;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			pickingModel = false;
		}
	}

	/** The box shows the model in force, not a refused one. */
	async function typeModel(input: HTMLInputElement) {
		await pickModel(input.value.trim());
		input.value = current?.model ?? '';
	}

	/** A question's files, as far as the run's list knows them yet. */
	const filesOf = (ids: string[]): StoredFile[] =>
		ids.flatMap((id) => {
			const a = rs.attachments.find((x) => x.id === id);
			return a
				? [{ id, filename: a.filename, mime: a.mime, size: a.size, kind: 'attachment' }]
				: [];
		});

	const asker = (m: AskMessage) => (m.userId ? t('runview.ask.you') : t('runview.ask.someone'));
</script>

<div class="ask">
	{#if !pane && !loadError}
		<div class="pad"><Loading text={t('runview.ask.loading')} /></div>
	{:else if loadError}
		<div class="pad">
			<EmptyState compact inline title={t('runview.ask.loadFailed')} text={loadError} />
		</div>
	{:else if pane}
		<div class="head">
			<span class="display ttl">{t('runview.ask.title')}</span>
			{#if pane.harness}<span class="mono faint">{pane.harness}</span>{/if}
			<span class="spacer"></span>
			{#if sessions.length}
				<div class="session">
					<Select
						id="ask-session"
						size="sm"
						label={t('runview.ask.session')}
						bind:value={selected}
						options={sessionOptions}
					/>
				</div>
			{/if}
			<!-- Asking, a new session and the model are run:operate. -->
			{#if enabled && rs.canAnswer}
				<button
					class="btn-ghost btn-sm"
					onclick={newSession}
					aria-busy={creating}
					disabled={creating || !messages.length}>{t('runview.ask.newSession')}</button
				>
				{#if modelOptions.length}
					<Select
						id="ask-model"
						size="sm"
						label={t('runview.ask.model')}
						bind:value={() => current?.model ?? '', pickModel}
						options={modelOptions}
						placeholder={t('runview.ask.modelPlaceholder')}
						disabled={pickingModel || busy}
					/>
				{:else}
					<input
						class="model mono"
						value={current?.model ?? ''}
						aria-label={t('runview.ask.model')}
						placeholder={t('runview.ask.modelPlaceholder')}
						disabled={pickingModel || busy}
						onchange={(e) => typeModel(e.currentTarget)}
					/>
				{/if}
			{/if}
		</div>
		<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (delegated: the path buttons inside take the keys) -->
		<div class="scroll" bind:this={scroller} onclick={clickPath}>
			{#if !enabled}
				<EmptyState compact inline title={t('runview.ask.off')} text={pane.reason ?? ''} />
			{:else if messages.length === 0 && !live}
				<div class="hint muted">{t('runview.ask.empty')}</div>
			{/if}
			{#each messages as m (m.seq)}
				{#if m.role === 'question'}
					<div class="q">
						{#if m.text}<div class="bubble">{m.text}</div>{/if}
						<FileChips items={filesOf(m.attachmentIds)} />
						<div class="um">{asker(m)} · {fmtClock(m.at)}</div>
					</div>
				{:else}
					<div class="a">
						{#if m.error}
							<div class="error">{t('runview.ask.failed', { error: m.error })}</div>
						{/if}
						{#if m.text}<Markdown src={m.text} paths={!!onpath} />{/if}
						<div class="am muted">
							{[m.model, m.costUsd ? fmtCost(m.costUsd) : '', fmtClock(m.at)]
								.filter(Boolean)
								.join(' · ')}
						</div>
					</div>
				{/if}
			{/each}
			{#if live}
				<div class="a">
					{#if live.text}<Markdown src={live.text} paths={!!onpath} />{/if}
					<div class="am">
						<span class="live-dot sm pulse hue-running"></span>
						{live.tool ? t('runview.ask.doing', { tool: live.tool }) : t('runview.ask.thinking')}
					</div>
				</div>
			{/if}
		</div>
		{#if enabled && rs.canAnswer}
			<AskComposer {rs} session={selected} {busy} onasked={asked} bind:error />
		{/if}
	{/if}
</div>

<style>
	.ask {
		display: flex;
		flex-direction: column;
		min-height: 0;
		height: 100%;
	}
	.pad {
		padding: 22px;
	}
	.head {
		flex: none;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 14px 6px;
		min-width: 0;
	}
	.ttl {
		font-size: var(--fs-section);
	}
	.session {
		flex: 0 1 260px;
		min-width: 0;
	}
	.model {
		width: 220px;
		font-size: var(--fs-meta);
	}
	.scroll {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		padding: 6px 14px 10px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.hint {
		font-size: var(--fs-meta);
		padding: 8px 2px;
	}
	.q {
		align-self: flex-end;
		max-width: 70%;
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 3px;
	}
	.bubble {
		background: var(--brand-bg);
		border: 1px solid var(--brand-line);
		border-radius: var(--r-row) var(--r-card) var(--r-xs) var(--r-card);
		padding: 8px 12px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.um,
	.am {
		font-size: var(--fs-meta);
		color: var(--text-faint);
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.a {
		max-width: 70ch;
		font-size: var(--fs-body);
		line-height: 1.6;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.error {
		color: var(--danger-text);
		font-size: var(--fs-meta);
	}
</style>
