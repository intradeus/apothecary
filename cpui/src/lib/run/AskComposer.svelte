<script lang="ts">
	// Ask's question box (DESIGN §6): words, files and annotations, asked in
	// the session on screen. ↵ asks, ⇧↵ is a newline. Files go up first; the
	// box then clears, and a refusal gives the words (and annotations) back
	// with the reason.
	//
	// While it is on screen it is the registry's Ask target: lines picked in
	// the Files viewer land here as chips, folded into the question ahead of
	// the words.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { AskMessage } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import AnnotationChip from './AnnotationChip.svelte';
	import { annotationText, type Annotation } from './annotation';
	import type { RunStore } from './store.svelte';

	let {
		rs,
		session,
		busy,
		onasked,
		error = $bindable('')
	}: {
		rs: RunStore;
		/** The session on screen ('': none yet, the question opens one). */
		session: string;
		/** That session is answering: nothing more is asked of it. */
		busy: boolean;
		/** The engine took the question, asked in `into` (the session on
		 *  screen at Enter). */
		onasked: (q: AskMessage, into: string) => void;
		/** The box's line of trouble, shared with the pane's own actions. */
		error?: string;
	} = $props();

	let draft = $state('');
	let sending = $state(false);
	/** The question's files are going up: the box and its files are locked,
	 *  so what is asked is what was on screen at Enter. */
	let uploading = $state(false);
	let ta = $state<HTMLTextAreaElement | null>(null);
	let files = $state<File[]>([]);
	let picker = $state<ReturnType<typeof AttachFiles> | null>(null);
	/** Annotations from the Files viewer, waiting for the next question. */
	let chips = $state<Annotation[]>([]);

	$effect(() => rs.composers.register({ kind: 'ask', add: (a) => chips.push(a) }));

	async function send() {
		const typed = draft.trim();
		if ((!typed && !files.length && !chips.length) || sending || busy) return;
		sending = true;
		error = '';
		// The session it is asked in is the one on screen now, whichever is
		// picked while the files go up.
		const into = session;
		// Files go up first; if they cannot, nothing is asked and the draft,
		// its files and its chips stay put.
		let ids: string[] = [];
		const sentFiles = files;
		if (sentFiles.length) {
			uploading = true;
			try {
				ids = (await api.files.upload(sentFiles)).map((a) => a.id);
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
		chips = [];
		try {
			const q = await api.ask.send(rs.runId, text, into || undefined, ids);
			// Only the files it carried: any picked while it was on its way
			// are the next question's.
			files = files.filter((f) => !sentFiles.includes(f));
			// The question's files join the run's record.
			if (ids.length) void rs.refreshFiles();
			onasked(q, into);
		} catch (e) {
			error = errorMessage(e);
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

	// A pasted screenshot (or copied files) joins the question; pasted text
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

<div class="comp inset">
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
		placeholder={t('runview.ask.placeholder')}
		onkeydown={onKey}
		onpaste={onPaste}
		disabled={busy}
		readonly={uploading}
		aria-label={t('runview.ask.title')}></textarea>
	<div class="foot">
		<span class="files">
			<AttachFiles
				bind:this={picker}
				bind:files
				compact
				disabled={uploading}
				label={t('runview.ask.attach')}
			/>
		</span>
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
			disabled={sending || busy || (!draft.trim() && !files.length && !chips.length)}
			>{t(busy ? 'runview.ask.answering' : 'runview.ask.send')}</button
		>
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
	.chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 14px 0;
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
</style>
