<script lang="ts">
	// The assistant beside a page (docs/assistant-plan.md): the person's
	// session about this subject, its answers as they are written, and the
	// one thing it does — fill the page, through the page's guard (undo, the
	// changed marker, the click before overwriting newer edits). Nothing is
	// saved here: the page's own Save does that.
	import { onDestroy, onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import type {
		AssistantFrame,
		AssistantKind,
		AssistantMessage,
		AssistantSession,
		AssistantStatus
	} from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import type { PageGuard } from './fillGuard.svelte';

	let {
		kind,
		subject,
		workshop = '',
		guard,
		pageState,
		onsession
	}: {
		kind: AssistantKind;
		/** The flow's name, the ticket's key, the skill's id, or a draft id. */
		subject: string;
		/** The workshop the subject lives in ('' for a skill). */
		workshop?: string;
		/** The page's guard: what a fill goes through. */
		guard: PageGuard;
		/** The page as it stands, unsaved edits included, for the question. */
		pageState: () => unknown;
		/** The session, once open: a draft's page moves it onto what it saves. */
		onsession?: (id: string) => void;
	} = $props();

	let status = $state<AssistantStatus | null>(null);
	let session = $state<AssistantSession | null>(null);
	let messages = $state<AssistantMessage[]>([]);
	let busy = $state(false);
	let live = $state('');
	let tool = $state('');
	let note = $state('');
	let draft = $state('');
	let error = $state('');
	let loadError = $state('');
	let fetching = $state(false);
	let choice = $state('');
	let stop: (() => void) | null = null;

	const page = $derived(status?.pages[kind]);
	const modelOptions = $derived<SelectOption[]>([
		{ value: '', label: t('assistant.modelDefault') },
		...(status?.models ?? []).map((m) => ({ value: m, label: m }))
	]);
	const harnessOptions = $derived<SelectOption[]>(
		(status?.integrations ?? []).map((i) => ({ value: i.id, label: `${i.name} · ${i.kind}` }))
	);

	async function load() {
		loadError = '';
		try {
			status = await api.assistant.status(workshop || undefined);
			choice = status.choice.integration || status.integrations[0]?.id || '';
			if (!status.enabled) return;
			const open = await api.assistant.open(kind, subject, workshop || undefined);
			session = open.session;
			messages = open.messages;
			busy = open.busy;
			onsession?.(open.session.id);
		} catch (e) {
			loadError = errorMessage(e);
		}
	}

	function onFrame(f: AssistantFrame) {
		if (f.kind === 'gap') {
			// Frames were lost (or the engine restarted): the record says
			// where the session is.
			if (session) void reopen();
			return;
		}
		if (f.kind === 'repos') {
			if (f.workshop !== workshop || !status) return;
			fetching = !f.repos;
			if (f.repos) status = { ...status, repos: f.repos };
			return;
		}
		if (!session || f.sessionId !== session.id) return;
		switch (f.kind) {
			case 'message':
				if (!messages.some((m) => m.seq === f.message.seq)) messages = [...messages, f.message];
				if (f.message.role === 'assistant') {
					live = '';
					tool = '';
				}
				break;
			case 'delta':
				live = f.text;
				tool = f.tool ?? '';
				break;
			case 'state':
				busy = f.busy;
				if (!f.busy) {
					note = '';
					guard.answered();
				}
				break;
			case 'status':
				note = f.text;
				break;
			case 'fill':
				// Only an answer to this page's question fills it: not one
				// replayed, nor one asked from another tab.
				if (guard.asking) guard.offer(f.fill);
				break;
		}
	}

	async function reopen() {
		if (!session) return;
		try {
			const open = await api.assistant.open(kind, session.subject, workshop || undefined);
			messages = open.messages;
			busy = open.busy;
			if (!busy) {
				live = '';
				tool = '';
				note = '';
				guard.answered();
			}
		} catch (e) {
			error = errorMessage(e);
		}
	}

	onMount(() => {
		void load();
		stop = api.assistant.stream(onFrame);
	});
	onDestroy(() => stop?.());

	// A draft saved as a ticket or a skill: the conversation follows it.
	// Any other change of subject is another conversation: open that one.
	$effect(() => {
		const s = session;
		if (!s || !subject || s.subject === subject) return;
		if (s.subject.startsWith('draft-')) {
			api.assistant.setSubject(s.id, subject).then(
				(v) => (session = v),
				(e) => (error = errorMessage(e))
			);
		} else {
			untrack(() => {
				session = null;
				messages = [];
				busy = false;
				guard.answered();
				void load();
			});
		}
	});

	async function ask(text = draft) {
		const q = text.trim();
		if (!q || !session || busy) return;
		error = '';
		guard.ask();
		busy = true;
		draft = '';
		try {
			const m = await api.assistant.send(session.id, q, pageState());
			if (!messages.some((x) => x.seq === m.seq)) messages = [...messages, m];
		} catch (e) {
			busy = false;
			guard.answered();
			draft = q;
			error = errorMessage(e);
		}
	}

	async function cancel() {
		if (!session) return;
		try {
			await api.assistant.cancel(session.id);
		} catch (e) {
			error = errorMessage(e);
		}
	}

	async function pickModel(model: string) {
		if (!session) return;
		try {
			session = await api.assistant.setModel(session.id, model);
		} catch (e) {
			error = errorMessage(e);
		}
	}

	async function useHarness() {
		try {
			await api.assistant.setChoice(choice);
			await load();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	async function refresh() {
		if (!workshop || !status) return;
		fetching = true;
		error = '';
		try {
			status = { ...status, repos: await api.assistant.refresh(workshop) };
		} catch (e) {
			error = errorMessage(e);
		} finally {
			fetching = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void ask();
		}
	}
</script>

<section class="assistant glass pane" aria-label={t('assistant.title')}>
	<div class="pane-head">
		<h2>{t('assistant.title')}</h2>
		{#if session && status?.enabled}
			<span class="model">
				<Select
					id="assistant-model"
					size="sm"
					label={t('assistant.model')}
					options={modelOptions}
					value={session.model ?? ''}
					onchange={pickModel}
				/>
			</span>
		{/if}
	</div>
	<div class="pane-body col">
		{#if loadError}
			<ErrorCard cause={t('assistant.failed')} detail={loadError} onretry={load} />
		{:else if !status}
			<Skeleton lines={3} />
		{:else if !status.enabled}
			<Callout hue="attention" glyph={I.info}>
				<div class="col">
					<strong>{t('assistant.off')}</strong>
					<span>{status.reason}</span>
					{#if status.integrations.length}
						<span>{t('assistant.chooseHarness')}</span>
						<div class="row nowrap">
							<Select
								id="assistant-harness"
								size="sm"
								label={t('assistant.chooseHarness')}
								options={harnessOptions}
								bind:value={choice}
							/>
							<button class="btn-sm" onclick={useHarness} disabled={!choice}
								>{t('assistant.useHarness')}</button
							>
						</div>
					{:else if status.reason && status.pod.state !== 'running'}
						<span class="muted small">{t('assistant.noHarness')}</span>
					{/if}
				</div>
			</Callout>
		{:else}
			{#if page?.repos && workshop}
				<div class="repos row" aria-label={t('assistant.repos')}>
					{#each status.repos as r (r.name)}
						<span
							class="pill sm"
							class:tint-danger={!r.ok}
							title={r.ok
								? r.head
								: t('assistant.repoFailed', { name: r.name, error: r.error ?? '' })}>{r.name}</span
						>
					{/each}
					{#if fetching}<span class="muted small">{t('assistant.fetching')}</span>{/if}
					<button class="btn-sm" onclick={refresh} disabled={fetching} aria-busy={fetching}
						>{I.retry} {t('assistant.refresh')}</button
					>
				</div>
			{/if}

			{#if guard.pending}
				<Callout hue="attention" glyph={I.warn}>
					<div class="col">
						<span>{t('assistant.stale')}</span>
						<span class="muted small"
							>{t('assistant.changed', { fields: guard.pending.changed.join(', ') })}</span
						>
						<div class="row">
							<button class="btn-sm" onclick={() => guard.applyPending()}
								>{t('assistant.applyAnyway')}</button
							>
							<button class="btn-sm" onclick={() => guard.discardPending()}
								>{t('assistant.keepMine')}</button
							>
						</div>
					</div>
				</Callout>
			{:else if guard.changed.length}
				<div class="filled row" role="status">
					<span>{t('assistant.changed', { fields: guard.changed.join(', ') })}</span>
					<button class="btn-sm" onclick={() => guard.revert()}>{t('assistant.undo')}</button>
				</div>
			{/if}

			<div class="log col" aria-live="polite">
				{#if !messages.length && !busy}
					<p class="muted">{t('assistant.empty')}</p>
					<div class="presets col">
						{#each page?.presets ?? [] as p (p)}
							<button class="preset" onclick={() => ask(p)}>{p}</button>
						{/each}
					</div>
				{/if}
				{#each messages as m (m.seq)}
					<div class="msg" class:mine={m.role === 'person'}>
						<span class="who muted small"
							>{m.role === 'person' ? t('assistant.you') : t('assistant.it')}</span
						>
						{#if m.text}<p class="text">{m.text}</p>{/if}
						{#if m.fill !== undefined}
							<span class="row">
								<span class="pill sm tint-accent">{t('assistant.filledNote')}</span>
								<button class="btn-sm" onclick={() => guard.applyNow(m.fill)} disabled={busy}
									>{t('assistant.fillAgain')}</button
								>
							</span>
						{/if}
						{#if m.error}<p class="error">{m.error}</p>{/if}
					</div>
				{/each}
				{#if busy}
					<div class="msg">
						<span class="who muted small">{t('assistant.it')}</span>
						{#if live}<p class="text">{live}</p>{/if}
						<span class="muted small">{tool || note || t('assistant.thinking')}</span>
					</div>
				{/if}
			</div>

			<div class="comp inset">
				<textarea
					class="bare"
					bind:value={draft}
					rows="2"
					placeholder={t('assistant.placeholder')}
					aria-label={t('assistant.placeholder')}
					onkeydown={onKey}
					disabled={busy}></textarea>
				<div class="foot row">
					{#if error}<span class="error" title={error}>{error}</span>{/if}
					{#if busy}
						<button class="btn-sm" onclick={cancel}>{t('assistant.stop')}</button>
					{:else}
						<button class="btn-primary btn-sm" onclick={() => ask()} disabled={!draft.trim()}
							>{t('assistant.send')}</button
						>
					{/if}
				</div>
			</div>
		{/if}
	</div>
</section>

<style>
	.assistant {
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.model {
		margin-left: auto;
		min-width: 0;
		max-width: 60%;
	}
	.log {
		overflow-y: auto;
		max-height: 50vh;
		gap: 10px;
	}
	.msg {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.msg.mine .text {
		color: var(--text-muted);
	}
	.text {
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.error {
		color: var(--danger-text);
		margin: 0;
	}
	.preset {
		text-align: left;
	}
	.comp textarea {
		width: 100%;
		resize: vertical;
	}
	.foot {
		justify-content: flex-end;
	}
	.repos {
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	.filled {
		gap: 8px;
		align-items: center;
		font-size: var(--fs-meta);
	}
</style>
