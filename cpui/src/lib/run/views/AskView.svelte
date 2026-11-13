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
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	let pane = $state<AskState | null>(null);
	let loadError = $state('');
	let error = $state('');
	let draft = $state('');
	let sending = $state(false);
	let pickingModel = $state(false);
	let creating = $state(false);
	/** The session on screen ('': none yet — the run has no session). */
	let selected = $state('');
	/** Each session's answer being written: its text so far, and what the
	 *  agent is doing. */
	let lives = $state<Record<string, { text: string; tool: string }>>({});
	let scroller = $state<HTMLDivElement | null>(null);
	let ta = $state<HTMLTextAreaElement | null>(null);

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

	async function load() {
		try {
			const got = await api.ask.state(rs.runId);
			// The session on screen stays while the run still has it; else the
			// latest.
			if (!got.sessions.some((s) => s.id === selected)) selected = got.sessions.at(-1)?.id ?? '';
			pane = got;
			loadError = '';
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

	async function send() {
		const text = draft.trim();
		if (!text || sending || busy || !enabled) return;
		sending = true;
		error = '';
		draft = '';
		try {
			// No session yet: the first question opens one, and the pane
			// follows it there.
			const into = selected;
			const q = await api.ask.send(rs.runId, text, into || undefined);
			if (!session(q.sessionId)) upsert({ id: q.sessionId, busy: true, createdAt: q.at });
			if (!into) selected = q.sessionId;
			const s = session(q.sessionId);
			if (s) s.busy = true;
			lives[q.sessionId] = { text: '', tool: '' };
			toBottom();
		} catch (e) {
			error = errorMessage(e);
			if (!draft.trim()) draft = text;
		} finally {
			sending = false;
		}
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

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void send();
		}
	}

	$effect(() => {
		void draft;
		if (!ta) return;
		ta.style.height = 'auto';
		ta.style.height = `${Math.min(ta.scrollHeight, 220)}px`;
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
			{#if enabled}
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
		<div class="scroll" bind:this={scroller}>
			{#if !enabled}
				<EmptyState compact inline title={t('runview.ask.off')} text={pane.reason ?? ''} />
			{:else if messages.length === 0 && !live}
				<div class="hint muted">{t('runview.ask.empty')}</div>
			{/if}
			{#each messages as m (m.seq)}
				{#if m.role === 'question'}
					<div class="q">
						<div class="bubble">{m.text}</div>
						<div class="um">{asker(m)} · {fmtClock(m.at)}</div>
					</div>
				{:else}
					<div class="a">
						{#if m.error}
							<div class="error">{t('runview.ask.failed', { error: m.error })}</div>
						{/if}
						{#if m.text}<Markdown src={m.text} />{/if}
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
					{#if live.text}<Markdown src={live.text} />{/if}
					<div class="am">
						<span class="live-dot sm pulse hue-running"></span>
						{live.tool ? t('runview.ask.doing', { tool: live.tool }) : t('runview.ask.thinking')}
					</div>
				</div>
			{/if}
		</div>
		{#if enabled}
			<div class="comp inset">
				<textarea
					class="bare"
					bind:this={ta}
					bind:value={draft}
					rows="1"
					placeholder={t('runview.ask.placeholder')}
					onkeydown={onKey}
					disabled={busy}
					aria-label={t('runview.ask.title')}></textarea>
				<div class="foot">
					<span class="keys"
						><span><span class="k">↵</span> {t('runview.ask.keySend')}</span>
						<span><span class="k">⇧↵</span> {t('runview.ask.keyNewline')}</span></span
					>
					{#if error}<span class="error" title={error}>{error}</span>{/if}
					<span class="spacer"></span>
					<button
						class="btn-primary btn-md"
						onclick={send}
						aria-busy={sending}
						disabled={sending || busy || !draft.trim()}
						>{t(busy ? 'runview.ask.answering' : 'runview.ask.send')}</button
					>
				</div>
			</div>
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
	.comp {
		flex: none;
		margin: 0 calc(12px + var(--fab-clear, 0px)) 12px 12px;
		border-radius: var(--r-btn);
		background: var(--code-bg);
		display: flex;
		flex-direction: column;
	}
	textarea {
		min-height: 44px;
		padding: 11px 14px;
		overflow-wrap: anywhere;
		resize: none;
	}
	.comp:focus-within {
		border-color: var(--brand-line);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 14px;
		padding: 6px 6px 6px 14px;
		font-size: var(--fs-meta);
		color: var(--text-faint);
		min-width: 0;
	}
	.keys {
		display: flex;
		gap: 14px;
		white-space: nowrap;
		overflow: hidden;
		min-width: 0;
	}
	.k {
		font-family: var(--font-code);
		color: var(--text-muted);
	}
	.foot .error {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
</style>
