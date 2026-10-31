<script lang="ts">
	// Ask (DESIGN §6): a read-only session beside the run that answers
	// questions about the code as it stands. Its conversation is the run's,
	// kept with it; the model is picked here. One question at a time: the
	// answer streams in, then the box is yours again.
	import { tick } from 'svelte';
	import { api } from '$lib/api';
	import type { AskMessage, AskState } from '$lib/domain';
	import { fmtClock, fmtCost } from '$lib/format';
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
	/** The answer being written: its text so far, and what the agent is doing. */
	let live = $state<{ text: string; tool: string } | null>(null);
	let scroller = $state<HTMLDivElement | null>(null);
	let ta = $state<HTMLTextAreaElement | null>(null);

	const messages = $derived(pane?.messages ?? []);
	const enabled = $derived(!!pane?.enabled);
	const busy = $derived(!!pane?.busy);
	const modelOptions = $derived((pane?.models ?? []).map((m) => ({ value: m, label: m })));

	async function load() {
		try {
			pane = await api.ask.state(rs.runId);
			loadError = '';
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		}
	}

	function toBottom() {
		tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
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
					if (f.message.role === 'answer') live = null;
					toBottom();
				} else if (f.kind === 'delta') {
					live = { text: f.text, tool: f.tool ?? '' };
					toBottom();
				} else if (f.kind === 'state') {
					pane.busy = f.busy;
					if (f.model) pane.model = f.model;
					if (!f.busy) live = null;
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
			await api.ask.send(rs.runId, text);
			if (pane) pane.busy = true;
			live = { text: '', tool: '' };
			toBottom();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			if (!draft.trim()) draft = text;
		} finally {
			sending = false;
		}
	}

	async function pickModel(model: string) {
		if (!pane || model === pane.model) return;
		pickingModel = true;
		error = '';
		try {
			await api.ask.setModel(rs.runId, model);
			pane.model = model;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			pickingModel = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
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
			{#if pane.harness}<span class="mono fnt">{pane.harness}</span>{/if}
			<span class="spacer"></span>
			{#if enabled}
				{#if modelOptions.length}
					<Select
						id="ask-model"
						size="sm"
						value={pane.model ?? ''}
						options={modelOptions}
						disabled={pickingModel || busy}
						onchange={(v) => pickModel(v)}
					/>
				{:else}
					<input
						class="model mono"
						value={pane.model ?? ''}
						placeholder={t('runview.ask.modelPlaceholder')}
						disabled={pickingModel || busy}
						onchange={(e) => pickModel(e.currentTarget.value.trim())}
					/>
				{/if}
			{/if}
		</div>
		<div class="scroll" bind:this={scroller}>
			{#if !enabled}
				<EmptyState compact inline title={t('runview.ask.off')} text={pane.reason ?? ''} />
			{:else if messages.length === 0 && !live}
				<div class="hint mut">{t('runview.ask.empty')}</div>
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
							<div class="err">{t('runview.ask.failed', { error: m.error })}</div>
						{/if}
						{#if m.text}<Markdown src={m.text} />{/if}
						<div class="am mut">
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
						<span class="ldot sm pulse hue-emb"></span>
						{live.tool ? t('runview.ask.doing', { tool: live.tool }) : t('runview.ask.thinking')}
					</div>
				</div>
			{/if}
		</div>
		{#if enabled}
			<div class="comp in">
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
					{#if error}<span class="err" title={error}>{error}</span>{/if}
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
		font-size: 17px;
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
		background: var(--brandBg);
		border: 1px solid var(--brandLn);
		border-radius: 10px 14px 4px 14px;
		padding: 8px 12px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.um,
	.am {
		font-size: var(--fs-meta);
		color: var(--fnt);
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.a {
		max-width: 70ch;
		font-size: 13.5px;
		line-height: 1.6;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.err {
		color: var(--rosTx);
		font-size: var(--fs-meta);
	}
	.comp {
		flex: none;
		margin: 0 calc(12px + var(--fabClear, 0px)) 12px 12px;
		border-radius: var(--r-btn);
		background: var(--code);
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
		border-color: var(--brandLn);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 14px;
		padding: 6px 6px 6px 14px;
		font-size: var(--fs-meta);
		color: var(--fnt);
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
		font-family: var(--mono);
		color: var(--mut);
	}
	.foot .err {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
</style>
