<script lang="ts">
	// One of the assistant's conversations, as its panel shows it
	// (docs/assistant-plan.md): a loader while it answers, the answer once it
	// is in (never its steps or tool calls), the presets, the model, the
	// repositories. The panel's owner loads the conversation and follows its
	// stream; this only shows it.
	//
	// Everywhere (no target): it reads everything and changes nothing on its
	// own: every act is a request the person confirms.
	// This page (target): it fills the page through the page's guard. Each
	// fill shows what it changed, with Undo; an earlier one can be applied
	// again. An answer that filled nothing can be asked again in Everywhere.
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import type { AssistantMessage, AssistantRequest } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Brewing from '$lib/ui/Brewing.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import PromptBox from '$lib/ui/PromptBox.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { assistant, type PageAssist } from './assistant.svelte';
	import type { Conversation } from './conversation.svelte';

	let {
		convo,
		target,
		compact = false,
		onexpand
	}: {
		convo: Conversation;
		target?: PageAssist;
		/** The card: the latest exchange only, no model or repositories. */
		compact?: boolean;
		/** Show the whole conversation (the card's "earlier messages"). */
		onexpand?: () => void;
	} = $props();

	/** What the card shows: from the person's last question on. */
	const shown = $derived.by(() => {
		if (!compact) return convo.messages;
		const at = convo.messages.findLastIndex((m) => m.role === 'person');
		return at < 0 ? convo.messages : convo.messages.slice(at);
	});

	let draft = $state('');

	// The standing choice's models are its harness's. A conversation opened
	// before the harness changed keeps its own: it is offered only its
	// current model and the default, never another harness's ids.
	const ownHarness = $derived(convo.session?.integration === convo.status?.choice.integration);
	const modelOptions = $derived<SelectOption[]>([
		{ value: '', label: t('assistant.modelDefault') },
		...(ownHarness
			? (convo.status?.models ?? [])
			: convo.session?.model
				? [convo.session.model]
				: []
		).map((m) => ({ value: m, label: m }))
	]);

	/** Ask the draft, or a preset (the draft stays). */
	async function ask(preset?: string) {
		if (preset !== undefined) return void convo.ask(preset);
		const q = draft;
		draft = '';
		// Refused: the question comes back to be sent again.
		if (!(await convo.ask(q))) draft = q;
	}

	/** The page's word: "ticket", "flow". */
	const word = $derived(target ? t(`assistant.page.${target.kind}`).toLocaleLowerCase() : '');

	// A question carried from the page's mode lands in Everywhere's box, to
	// be sent as it is or reworded.
	$effect(() => {
		if (target || !assistant.carried) return;
		draft = assistant.carried;
		assistant.carried = '';
	});

	/** The newest answer that filled the page: its card has the page's Undo. */
	const lastFill = $derived(
		target ? (convo.messages.findLast((m) => m.fill !== undefined)?.seq ?? -1) : -1
	);
	/** The newest answer, when it filled nothing: it may be asked in Everywhere. */
	const lastPlain = $derived.by(() => {
		if (!target || convo.busy) return -1;
		const m = convo.messages.at(-1);
		return m?.role === 'assistant' && m.fill === undefined && !m.error ? m.seq : -1;
	});

	/** Ask in Everywhere what was asked here before answer m. */
	function carry(m: AssistantMessage) {
		const q = convo.messages.findLast((x) => x.role === 'person' && x.seq < m.seq);
		if (q) assistant.carry(q.text);
	}

	/** What an act's ids name, for the person: a workshop's name, a run's
	 *  title. An id alone hides which run an abort stops. */
	function named(args: unknown): string[] {
		if (typeof args !== 'object' || args === null) return [];
		const out: string[] = [];
		for (const [k, v] of Object.entries(args)) {
			if (typeof v !== 'string') continue;
			const ws = fleet.projectById.get(v);
			const run = fleet.runById.get(v);
			if (ws) out.push(t('assistant.namesWorkshop', { arg: k, name: ws.name }));
			else if (run) out.push(t('assistant.namesRun', { arg: k, name: run.title }));
		}
		return out;
	}
</script>

{#snippet requestCard(r: AssistantRequest, m: AssistantMessage)}
	<div class="request inset col" aria-label={t('assistant.request')}>
		<strong class="small">{r.why}</strong>
		<ol class="acts col">
			{#each r.acts as a, i (i)}
				<li class="col">
					<span class="row nowrap">
						<span class="mono small">{a.tool}</span>
						{#if a.status}
							<span class="pill sm mono" class:tint-danger={a.status >= 400}>{a.status}</span>
						{:else if r.state === 'done'}
							<span class="pill sm">{t('assistant.notRun')}</span>
						{/if}
					</span>
					{#if a.title}<span class="muted small">{a.title}</span>{/if}
					{#each named(a.arguments) as n (n)}<span class="small">{n}</span>{/each}
					<pre class="args mono">{JSON.stringify(a.arguments, null, 2)}</pre>
					{#if a.result}<span class="result small" title={a.result}>{a.result}</span>{/if}
				</li>
			{/each}
		</ol>
		{#if r.state === 'running'}
			<span class="pill sm tint-attention"
				>{convo.deciding === m.seq ? t('assistant.running') : t('assistant.interrupted')}</span
			>
		{:else if r.state === 'pending'}
			<div class="row">
				<button
					class="btn-primary btn-sm"
					onclick={() => convo.decide(m, true)}
					disabled={convo.deciding !== null || convo.switching}
					aria-busy={convo.deciding === m.seq}>{t('assistant.confirm')}</button
				>
				<button
					class="btn-sm"
					onclick={() => convo.decide(m, false)}
					disabled={convo.deciding !== null || convo.switching}>{t('assistant.decline')}</button
				>
			</div>
		{:else}
			<span class="pill sm" class:tint-accent={r.state === 'done'}
				>{r.state === 'done' ? t('assistant.confirmed') : t('assistant.declined')}</span
			>
		{/if}
	</div>
{/snippet}

{#snippet fillCard(m: AssistantMessage, g: PageAssist)}
	<div class="fill inset col" aria-label={t('assistant.filled', { page: word })}>
		<strong class="small">{I.thisPage} {t('assistant.filled', { page: word })}</strong>
		{#if m.seq === lastFill && g.guard.pending}
			<span class="small">{t('assistant.stale')}</span>
			<div class="row">
				<button class="btn-primary btn-sm" onclick={() => g.guard.applyPending()}
					>{t('assistant.applyAnyway')}</button
				>
				<button class="btn-sm" onclick={() => g.guard.discardPending()}
					>{t('assistant.keepMine')}</button
				>
			</div>
		{:else if m.seq === lastFill && g.guard.changed.length}
			<span class="row">
				<span class="small">{t('assistant.changed', { fields: g.guard.changed.join(', ') })}</span>
				<button class="btn-sm" onclick={() => g.guard.revert()}>{t('assistant.undo')}</button>
			</span>
		{:else if m.seq === lastFill && g.guard.held && g.handoff}
			{@render g.handoff()}
		{/if}
		{#if m.seq !== lastFill || (!g.guard.pending && !g.guard.changed.length && !g.guard.held)}
			<button
				class="btn-sm again"
				onclick={() => g.guard.applyNow(m.fill)}
				disabled={convo.busy}
				title={t('assistant.applyAgainTip')}>{t('assistant.applyAgain')}</button
			>
		{/if}
	</div>
{/snippet}

<section class="assistant col" aria-label={t('assistant.title')}>
	{#if target?.workshopPicker && !convo.busy}{@render target.workshopPicker()}{/if}
	{#if convo.session && convo.status?.enabled && !compact}
		<span class="model">
			<Select
				id="assistant-model-{target ? 'page' : 'general'}"
				size="sm"
				label={t('assistant.model')}
				options={modelOptions}
				value={convo.session.model ?? ''}
				onchange={(m) => convo.pickModel(m)}
				disabled={convo.switching}
				title={ownHarness ? undefined : t('assistant.modelOldHarness')}
			/>
		</span>
	{/if}
	<div class="body col">
		{#if convo.loadError}
			<ErrorCard
				cause={t('assistant.failed')}
				detail={convo.loadError}
				onretry={() => convo.load()}
			/>
		{:else if !convo.status}
			<Skeleton lines={3} />
		{:else if !convo.status.enabled}
			<Callout hue="attention" glyph={I.info}>
				<div class="col">
					<strong>{t('assistant.off')}</strong>
					<span>{convo.status.reason}</span>
					{#if !convo.status.integrations.length}
						<span class="muted small">{t('assistant.noHarness')}</span>
					{/if}
					{#if convo.status.integrations.length || !convo.status.choice.integration}
						<a class="btn-sm" href={to.system('assistant')}>{t('assistant.toSettings')}</a>
					{/if}
				</div>
			</Callout>
		{:else}
			{#if convo.status.pod.error}
				<Callout hue="warning" glyph={I.warn} ondismiss={() => convo.dismissPodError()}>
					<div class="col">
						<strong>{t('assistant.podFailed')}</strong>
						<span class="muted small">{convo.status.pod.error}</span>
					</div>
				</Callout>
			{/if}
			{#if convo.page?.repos && convo.workshop && !compact}
				<div class="repos row" aria-label={t('assistant.repos')}>
					{#each convo.status.repos as r (r.name)}
						<span
							class="pill sm"
							class:tint-danger={!r.ok}
							title={r.ok
								? r.head
								: t('assistant.repoFailed', { name: r.name, error: r.error ?? '' })}>{r.name}</span
						>
					{/each}
					{#if convo.fetching}<span class="muted small">{t('assistant.fetching')}</span>{/if}
					<button
						class="btn-sm"
						onclick={() => convo.refresh()}
						disabled={convo.fetching}
						aria-busy={convo.fetching}>{I.retry} {t('assistant.refresh')}</button
					>
				</div>
			{/if}

			<!-- Not .log: that is LogOutput's monospace (components.css). -->
			<div class="thread col" aria-live="polite" aria-busy={convo.switching}>
				{#if !convo.messages.length && !convo.busy}
					<p class="muted">
						{target ? t('assistant.fillEmpty', { page: word }) : t('assistant.drawerEmpty')}
					</p>
					<div class="presets col">
						{#each convo.page?.presets ?? [] as p (p)}
							<button class="preset" onclick={() => ask(p)} disabled={convo.switching}>{p}</button>
						{/each}
					</div>
				{/if}
				{#if shown.length < convo.messages.length}
					<button class="btn-ghost btn-sm earlier" onclick={() => onexpand?.()}
						>{I.up}
						{t('assistant.earlier', { count: convo.messages.length - shown.length })}</button
					>
				{/if}
				{#each shown as m (m.seq)}
					<div class="msg" class:mine={m.role === 'person'}>
						<span class="who muted small"
							>{m.role === 'person' ? t('assistant.you') : t('assistant.it')}</span
						>
						{#if m.text}<p class="text">{m.text}</p>{/if}
						{#if m.request}
							{@render requestCard(m.request, m)}
						{/if}
						{#if target && m.fill !== undefined}
							{@render fillCard(m, target)}
						{/if}
						{#if m.error}<p class="error">{m.error}</p>{/if}
						{#if m.seq === lastPlain}
							<button
								class="btn-ghost btn-sm carry"
								onclick={() => carry(m)}
								title={t('assistant.carryTip')}>{I.sparkle} {t('assistant.carry')}</button
							>
						{/if}
					</div>
				{/each}
				{#if convo.busy}
					<div class="msg">
						<span class="who muted small">{t('assistant.it')}</span>
						<Brewing
							size="sm"
							label={convo.note ||
								(target ? t('assistant.brewing', { page: word }) : t('assistant.thinking'))}
						/>
					</div>
				{/if}
			</div>

			<PromptBox
				bind:value={draft}
				label={target ? t('assistant.fillPlaceholder', { page: word }) : t('assistant.placeholder')}
				accent={!!target}
				disabled={convo.busy || convo.switching}
				error={convo.error}
				onsubmit={() => void ask()}
				oninput={(v) => convo.typing(v)}
			>
				{#snippet actions()}
					{#if convo.busy}
						<button class="btn-md" onclick={() => convo.cancel()}>{t('assistant.stop')}</button>
					{:else}
						<button
							class="btn-primary btn-md"
							onclick={() => ask()}
							disabled={!draft.trim() || convo.switching}>{t('assistant.send')}</button
						>
					{/if}
				{/snippet}
			</PromptBox>
		{/if}
	</div>
</section>

<style>
	.assistant {
		flex: 1;
		min-height: 0;
		gap: 10px;
	}
	.model {
		align-self: flex-end;
		min-width: 0;
		max-width: 60%;
	}
	.body {
		flex: 1;
		min-height: 0;
		gap: 10px;
	}
	.thread {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
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
	.repos {
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	.request {
		gap: 6px;
		padding: 8px 10px;
	}
	/* A fill: what it did to the page, in the page's mode's colour. */
	.fill {
		gap: 6px;
		padding: 8px 10px;
		margin-top: 4px;
		border-color: var(--accent-line);
		background: var(--accent-bg);
	}
	.fill .row {
		gap: 8px;
		align-items: center;
		flex-wrap: wrap;
	}
	.again,
	.carry,
	.earlier {
		align-self: flex-start;
	}
	.carry {
		margin-top: 4px;
	}
	.acts {
		margin: 0;
		padding-left: 18px;
		gap: 6px;
	}
	.args {
		margin: 0;
		max-height: 120px;
		overflow: auto;
		font-size: var(--fs-meta);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.result {
		overflow-wrap: anywhere;
		max-height: 4.5em;
		overflow: hidden;
	}
</style>
