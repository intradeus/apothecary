<script lang="ts">
	// The global assistant's conversation, as the drawer shows it
	// (docs/assistant-plan.md): the answer as it is written, the requests it
	// asks the person to confirm, the presets, the model, the repositories.
	// It reads everything and changes nothing on its own: every act is a
	// request the person confirms. The drawer's owner loads the conversation
	// and follows its stream; this only shows it.
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import type { AssistantMessage, AssistantRequest } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import type { Conversation } from './conversation.svelte';

	let { convo }: { convo: Conversation } = $props();

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

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void ask();
		}
	}
</script>

{#snippet requestCard(r: AssistantRequest, m: AssistantMessage | null)}
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
		{#if !m}
			<span class="muted small">{t('assistant.requestWaits')}</span>
		{:else if r.state === 'running'}
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

<section class="assistant col" aria-label={t('assistant.title')}>
	{#if convo.session && convo.status?.enabled}
		<span class="model">
			<Select
				id="assistant-model"
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
			{#if convo.page?.repos && convo.workshop}
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
					<p class="muted">{t('assistant.drawerEmpty')}</p>
					<div class="presets col">
						{#each convo.page?.presets ?? [] as p (p)}
							<button class="preset" onclick={() => ask(p)} disabled={convo.switching}>{p}</button>
						{/each}
					</div>
				{/if}
				{#each convo.messages as m (m.seq)}
					<div class="msg" class:mine={m.role === 'person'}>
						<span class="who muted small"
							>{m.role === 'person' ? t('assistant.you') : t('assistant.it')}</span
						>
						{#if m.text}<p class="text">{m.text}</p>{/if}
						{#if m.request}
							{@render requestCard(m.request, m)}
						{/if}
						{#if m.error}<p class="error">{m.error}</p>{/if}
					</div>
				{/each}
				{#if convo.busy}
					<div class="msg">
						<span class="who muted small">{t('assistant.it')}</span>
						{#if convo.live}<p class="text">{convo.live}</p>{/if}
						{#if convo.liveRequest}{@render requestCard(convo.liveRequest, null)}{/if}
						<span class="muted small">{convo.tool || convo.note || t('assistant.thinking')}</span>
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
					oninput={() => convo.typing(draft)}
					disabled={convo.busy || convo.switching}></textarea>
				<div class="foot row">
					{#if convo.error}<span class="error" title={convo.error}>{convo.error}</span>{/if}
					{#if convo.busy}
						<button class="btn-sm" onclick={() => convo.cancel()}>{t('assistant.stop')}</button>
					{:else}
						<button
							class="btn-primary btn-sm"
							onclick={() => ask()}
							disabled={!draft.trim() || convo.switching}>{t('assistant.send')}</button
						>
					{/if}
				</div>
			</div>
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
	.request {
		gap: 6px;
		padding: 8px 10px;
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
