<script lang="ts">
	// The local assistant (docs/assistant-plan.md): a page's fill button
	// opens it on a fresh conversation about the page. It asks with the page
	// as it stands; while the answer brews it shows only that it is brewing,
	// and once a fill lands it closes — the page holds the fill, unsaved,
	// with the guard's Undo beside its button. A page whose fill goes
	// elsewhere (a run's flow) gives a handoff: the modal stays to show it.
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Brewing from '$lib/ui/Brewing.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import { Conversation, type FillHook } from './conversation.svelte';
	import type { PageGuard } from './fillGuard.svelte';

	let {
		open = $bindable(false),
		kind,
		subject,
		workshop = '',
		label,
		guard,
		pageState,
		workshopPicker,
		handoff
	}: {
		open: boolean;
		kind: 'flow' | 'ticket' | 'skill' | 'dashboard' | 'run';
		/** The flow's name, the ticket's key, the skill's id, or a draft id. */
		subject: string;
		/** The workshop it reads ('' for none). */
		workshop?: string;
		/** The page's word in the title (default the kind's). */
		label?: string;
		/** The page's guard: what a fill goes through. */
		guard: PageGuard;
		/** The page as it stands, unsaved edits included, for each question. */
		pageState: () => unknown;
		/** The skill page's workshop choice, shown on top. */
		workshopPicker?: Snippet;
		/** What to do with a fill that belongs elsewhere: shown after one. */
		handoff?: Snippet;
	} = $props();

	const page = $derived(label ?? t(`assistant.page.${kind}`));

	let convo = $state<Conversation | null>(null);
	/** A fill of this opening's landed in the page (or is held there). */
	let landed = $state(false);
	let draft = $state('');

	/** What one conversation fills the page through: the page's guard as it
	 *  was when the conversation began, until it ends. An ended one's late
	 *  frames ask nothing of the page: they fill nothing, nor end a newer
	 *  conversation's question. */
	function hookFor(g: PageGuard): FillHook & { end(): void } {
		let ended = false;
		return {
			get asking() {
				return !ended && g.asking;
			},
			ask: () => g.ask(),
			answered() {
				if (!ended) g.answered();
			},
			offer(fill) {
				const ok = g.offer(fill);
				if (ok) landed = true;
				return ok;
			},
			end() {
				g.answered();
				ended = true;
			}
		};
	}

	// Each opening is its own conversation: a fresh session, nothing of the
	// last one's answers, an empty question.
	$effect(() => {
		if (open) untrack(() => (draft = ''));
	});

	// The conversation is the opening's, the page's and the workshop's: a
	// workshop chosen in the open modal starts a fresh one that reads its
	// repositories, keeping the question typed so far. The picker is hidden
	// while an answer brews; a workshop changed from outside then stops it,
	// as closing does. So does another page in the same component (Back to
	// another skill): the old page's session never asks with the new page,
	// nor fills it. Closing stops the answer and drops a late fill.
	$effect(() => {
		if (!open) return;
		const ws = workshop;
		const k = kind;
		const subj = subject;
		const { c, hook } = untrack(() => {
			const hook = hookFor(guard);
			const c = new Conversation({
				kind: k,
				subject: subj,
				workshop: ws,
				pageState: () => pageState(),
				fill: hook
			});
			convo = c;
			landed = false;
			return { c, hook };
		});
		c.connect();
		void c.load({ fresh: true });
		return () => {
			if (c.busy) void c.cancel();
			c.disconnect();
			hook.end();
		};
	});

	// The answer is in and filled the page: the page shows it.
	$effect(() => {
		if (open && landed && convo && !convo.busy && !handoff) open = false;
	});

	/** The last answer, when it filled nothing: the person may reply to it. */
	const reply = $derived.by(() => {
		const last = convo?.messages.findLast((m) => m.role === 'assistant');
		return last && last.fill === undefined ? last : null;
	});

	async function ask(text: string) {
		if (!convo) return;
		const typed = text === draft;
		if (typed) draft = '';
		if (!(await convo.ask(text)) && typed) draft = text;
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void ask(draft);
		}
	}
</script>

{#snippet actions()}
	{#if convo?.busy}
		<button type="button" class="btn-sm" onclick={() => convo?.cancel()}
			>{t('assistant.stop')}</button
		>
	{:else if convo?.status?.enabled}
		<button
			type="button"
			class="btn-primary btn-sm"
			onclick={() => ask(draft)}
			disabled={!convo.session || !draft.trim() || convo.switching}>{t('assistant.send')}</button
		>
	{/if}
{/snippet}

<Modal
	bind:open
	title={t('assistant.fillTitle', { page })}
	icon={I.sparkle}
	width={560}
	footer={actions}
>
	{#if convo}
		<div class="fa col">
			{#if !convo.busy}{@render workshopPicker?.()}{/if}
			{#if convo.loadError}
				<ErrorCard
					cause={t('assistant.failed')}
					detail={convo.loadError}
					onretry={() => convo?.load({ fresh: true })}
				/>
			{:else if !convo.status}
				<Brewing size="sm" label={t('assistant.loading')} />
			{:else if !convo.status.enabled}
				<Callout hue="attention" glyph={I.info}>
					<div class="col">
						<strong>{t('assistant.off')}</strong>
						{#if convo.status.reason}<span>{convo.status.reason}</span>{/if}
						<a href={to.system('assistant')} onclick={() => (open = false)}
							>{t('assistant.toSettings')}</a
						>
					</div>
				</Callout>
			{:else if convo.busy}
				<!-- The answer's words and tools are the assistant's business:
				     the page shows what it filled. -->
				<div class="brew">
					<Brewing size="lg" label={t('assistant.brewing', { page: page.toLocaleLowerCase() })} />
				</div>
			{:else}
				{#if reply}
					<div class="reply col" aria-live="polite">
						{#if reply.text}<p class="text">{reply.text}</p>{/if}
						{#if reply.error}<p class="error">{reply.error}</p>{/if}
					</div>
				{:else if !convo.messages.length}
					<p>{t('assistant.fillEmpty')}</p>
				{/if}
				{#if landed && handoff}{@render handoff()}{/if}
				{#if convo.error}<p class="error">{convo.error}</p>{/if}
				{#if convo.page?.presets.length}
					<div class="presets col">
						{#each convo.page.presets as p (p)}
							<button
								type="button"
								class="preset btn-sm"
								onclick={() => ask(p)}
								disabled={convo.switching}>{p}</button
							>
						{/each}
					</div>
				{/if}
				<textarea
					class="inset"
					bind:value={draft}
					rows="3"
					placeholder={t('assistant.fillPlaceholder')}
					aria-label={t('assistant.fillPlaceholder')}
					oninput={() => convo?.typing(draft)}
					onkeydown={onKey}></textarea>
			{/if}
		</div>
	{/if}
</Modal>

<style>
	.fa {
		gap: 12px;
	}
	.brew {
		display: flex;
		justify-content: center;
		padding: 24px 0;
	}
	.text {
		margin: 0;
		color: var(--text);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.error {
		margin: 0;
		color: var(--danger-text);
	}
	.presets {
		gap: 6px;
	}
	.preset {
		text-align: left;
	}
	textarea {
		width: 100%;
		resize: vertical;
		padding: 8px 10px;
	}
</style>
