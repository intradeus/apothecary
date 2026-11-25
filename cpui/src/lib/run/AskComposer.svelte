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
	import PromptBox from '$lib/ui/PromptBox.svelte';
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

	// A pasted screenshot (or copied files) joins the question; pasted text
	// is left to the browser.
	function onPaste(e: ClipboardEvent) {
		const pasted = e.clipboardData?.files;
		if (!pasted?.length) return;
		e.preventDefault();
		picker?.add(pasted);
	}
</script>

<div class="ask-box">
	<PromptBox
		bind:value={draft}
		label={t('runview.ask.title')}
		placeholder={t('runview.ask.placeholder')}
		sendKey={t('runview.ask.keySend')}
		disabled={busy}
		readonly={uploading}
		{error}
		onsubmit={send}
		onpaste={onPaste}
	>
		{#snippet top()}
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
		{/snippet}
		{#snippet extras()}
			<AttachFiles
				bind:this={picker}
				bind:files
				compact
				disabled={uploading}
				label={t('runview.ask.attach')}
			/>
		{/snippet}
		{#snippet actions()}
			<button
				class="btn-primary btn-md"
				onclick={send}
				aria-busy={sending}
				disabled={sending || busy || (!draft.trim() && !files.length && !chips.length)}
				>{t(busy ? 'runview.ask.answering' : 'runview.ask.send')}</button
			>
		{/snippet}
	</PromptBox>
</div>

<style>
	.ask-box {
		flex: none;
		display: flex;
		flex-direction: column;
		margin: 0 calc(12px + var(--fab-clear, 0px)) 12px 12px;
	}
	.chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
</style>
