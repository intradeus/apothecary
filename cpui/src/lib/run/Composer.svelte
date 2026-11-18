<script lang="ts">
	// The composer (DESIGN 4.12). Nobody talks to an agent without taking it
	// over, so until then it is only the Take over button. Take over holds the
	// agent's session: while held (sky), its turns wait for your next message
	// instead of ending the stage, and Release hands the task back — the stage
	// can only end after that. By default a take-over stops the agent's turn
	// in flight, so it waits on you at once; unchecking "Stop the agent now"
	// takes over passively, the agent finishing its turn first.
	//
	// ↵ sends, delivered when the agent's turn ends; ⇧↵ is a newline. Sending
	// is optimistic: the box clears and the bubble shows at once; if the engine
	// refuses, the text comes back with the reason.
	//
	// While the run is in your hands, lines picked in the Files viewer land
	// here as chips (the registry's hold target); a send folds them into the
	// message ahead of the words, and a refusal gives them back too.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import { fmtClockDuration } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import { session } from '$lib/app/session.svelte';
	import type { RunHold } from '$lib/domain';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import AnnotationChip from './AnnotationChip.svelte';
	import { annotationText, type Annotation } from './annotation';
	import type { ComposerRegistry } from './composerTarget.svelte';

	let {
		runId,
		composers,
		onsent,
		onfailed,
		hold,
		releasing = false,
		now = Date.now(),
		ontakeover,
		onrelease
	}: {
		runId: string;
		/** Where the Files viewer sends annotations: this composer is its hold
		 *  target while the run is in the signed-in user's hands. */
		composers: ComposerRegistry;
		/** Fired as the message leaves (before the engine confirms). */
		onsent: (text: string) => void;
		/** The engine refused it: the bubble should go. */
		onfailed?: (text: string) => void;
		/** The take-over in force, if any. */
		hold?: RunHold;
		/** Release clicked, not yet on the record. */
		releasing?: boolean;
		/** A ticking clock, for the hold's duration. */
		now?: number;
		/** force: stop the agent's turn in flight. */
		ontakeover?: (force: boolean) => Promise<boolean>;
		onrelease?: () => Promise<boolean>;
	} = $props();

	let draft = $state('');
	let sending = $state(false);
	/** The message's files are going up: the box and its files are locked,
	 *  so what is sent is what was on screen at Enter. */
	let uploading = $state(false);
	let holding = $state(false);
	let force = $state(true);
	let error = $state('');
	let ta = $state<HTMLTextAreaElement | null>(null);
	let files = $state<File[]>([]);
	let picker = $state<ReturnType<typeof AttachFiles> | null>(null);
	/** Annotations from the Files viewer, waiting for the next message. */
	let chips = $state<Annotation[]>([]);

	const held = $derived(!!hold);
	// A hold without its user (an older record) is nobody's in particular,
	// so not yours.
	const mine = $derived(!!hold?.userId && hold.userId === session.user?.id);
	$effect(() => {
		if (!mine) return;
		return composers.register({ kind: 'hold', add: (a) => chips.push(a) });
	});
	const heldFor = $derived(hold ? fmtClockDuration(now - new Date(hold.since).getTime()) : '');
	// Checked by default, not remembered: the next take-over (the composer
	// stays mounted through a hold and its release) starts from a stop.
	$effect(() => {
		if (held) force = true;
	});

	async function takeOver() {
		if (!ontakeover || holding) return;
		holding = true;
		error = '';
		try {
			await ontakeover(force);
		} finally {
			holding = false;
		}
	}
	async function release() {
		if (!onrelease || holding || releasing) return;
		holding = true;
		error = '';
		try {
			await onrelease();
		} finally {
			holding = false;
		}
	}

	async function send() {
		const typed = draft.trim();
		if ((!typed && !files.length && !chips.length) || sending) return;
		sending = true;
		error = '';
		// Files go up first; if they cannot, nothing is sent and the draft,
		// its files and its chips stay put.
		let ids: string[] = [];
		if (files.length) {
			uploading = true;
			try {
				ids = (await api.files.upload(files)).map((a) => a.id);
			} catch (e) {
				error = errorMessage(e);
				sending = false;
				return;
			} finally {
				uploading = false;
			}
		}
		const sent = chips;
		const text = [annotationText(sent), typed].filter(Boolean).join('\n\n');
		draft = '';
		files = [];
		chips = [];
		onsent(text);
		try {
			await api.runs.sendMessage(runId, text, ids);
		} catch (e) {
			error = errorMessage(e);
			onfailed?.(text);
			// Give the words back unless something new was typed meanwhile;
			// the chips come back ahead of any that landed since.
			if (!draft.trim()) draft = typed;
			chips = [...sent, ...chips];
		} finally {
			sending = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void send();
		}
	}

	// A pasted screenshot (or copied files) joins the message; pasted text
	// is left to the browser.
	function onPaste(e: ClipboardEvent) {
		const pasted = e.clipboardData?.files;
		if (!pasted?.length) return;
		e.preventDefault();
		picker?.add(pasted);
	}

	// Grow with the text, up to a third of the window.
	$effect(() => {
		void draft;
		if (!ta) return;
		ta.style.height = 'auto';
		ta.style.height = `${Math.min(ta.scrollHeight, 220)}px`;
	});
</script>

<div class="comp inset" class:held>
	{#if held}
		<div class="hold">
			<span class="live-dot sm pulse hue-attention"></span>
			<span class="hue-attention">{t('run.composer.held', { duration: heldFor })}</span>
			{#if hold && hold.messages}
				<span class="muted">· {t('run.composer.heldMessages', { count: hold.messages })}</span>
			{/if}
		</div>
		{#if chips.length}
			<div class="chips" role="group" aria-label={t('run.annotate.chips')}>
				{#each chips as chip, i (chip.id)}
					<AnnotationChip
						bind:annotation={chips[i]}
						disabled={uploading}
						onremove={() => (chips = chips.filter((c) => c.id !== chip.id))}
					/>
				{/each}
			</div>
		{/if}
		<textarea
			class="bare"
			bind:this={ta}
			bind:value={draft}
			rows="1"
			readonly={uploading}
			placeholder={t('run.composer.placeholder')}
			onkeydown={onKey}
			onpaste={onPaste}
			aria-label={t('run.composer.aria')}></textarea>
	{/if}
	<div class="foot">
		{#if held}
			<span class="files">
				<AttachFiles
					bind:this={picker}
					bind:files
					compact
					disabled={uploading}
					label={t('run.composer.attach')}
				/>
			</span>
			<span class="keys">
				<span><span class="k">↵</span> {t('run.composer.keySend')}</span>
				<span class="nl"><span class="k">⇧↵</span> {t('run.composer.keyNewline')}</span>
			</span>
		{:else}
			<span class="keys">{t('run.composer.takeOverHint')}</span>
		{/if}
		{#if error}<span class="error" title={error}>{error}</span>{/if}
		<span class="spacer"></span>
		{#if held}
			<button
				class="btn-md hue-attention"
				onclick={release}
				aria-busy={holding || releasing}
				disabled={holding || releasing}
				title={t('run.composer.releaseTitle')}
				>{t(releasing ? 'run.composer.releasing' : 'run.composer.release')}</button
			>
			<button
				class="btn-primary btn-md"
				onclick={send}
				aria-busy={sending}
				disabled={sending || (!draft.trim() && !files.length && !chips.length)}
				title={t('run.composer.sendTitle')}>{t('run.composer.send')}</button
			>
		{:else}
			<Checkbox
				size="sm"
				bind:checked={force}
				disabled={holding}
				label={t('run.composer.stopNow')}
				title={t('run.composer.stopNowTitle')}
			/>
			<button
				class="btn-md"
				onclick={takeOver}
				aria-busy={holding}
				disabled={holding}
				title={t('run.composer.takeOverTitle')}>{t('run.composer.takeOver')}</button
			>
		{/if}
	</div>
</div>

<style>
	.comp {
		flex: none;
		margin: 0 calc(12px + var(--fab-clear, 0px)) 12px 12px;
		border-radius: var(--r-btn);
		background: var(--code-bg);
		display: flex;
		flex-direction: column;
	}
	/* .bare: the composer is the field. */
	textarea {
		min-height: 44px;
		padding: 11px 14px;
		overflow-wrap: anywhere;
		resize: none;
	}
	.comp:focus-within {
		border-color: var(--brand-line);
	}
	/* Held: the agent is in your hands — the attention colour, as a
	 * waiting ping's card. */
	.comp.held {
		border-color: var(--attention-line);
		background: var(--attention-bg);
		box-shadow: 0 0 var(--glow-lg) var(--attention-glow);
	}
	.comp.held:focus-within {
		border-color: var(--attention);
	}
	.hold {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 14px 0;
		font-size: var(--fs-meta);
		font-weight: 600;
	}
	.chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 14px 0;
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
	.files {
		flex: 0 1 auto;
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
	.error {
		color: var(--danger-text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	/* Drop key hints as the Session window narrows (a container query on
	 * the window, so its own threshold). */
	@container (max-width: 520px) {
		.keys .nl {
			display: none;
		}
	}
</style>
