<script lang="ts">
	// The composer (DESIGN 4.12): ↵ queues a message for the end of the
	// agent's turn, ⌘↵ sends it now (interrupting the turn), ⇧↵ is a newline.
	// Sending is optimistic: the box clears and the bubble shows at once; if
	// the engine refuses, the text comes back with the reason.
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';

	let {
		runId,
		onsent,
		onfailed
	}: {
		runId: string;
		/** Fired as the message leaves (before the engine confirms). */
		onsent: (text: string, interrupt: boolean) => void;
		/** The engine refused it: the bubble should go. */
		onfailed?: (text: string) => void;
	} = $props();

	let draft = $state('');
	let sending = $state<'' | 'queue' | 'now'>('');
	let error = $state('');
	let ta = $state<HTMLTextAreaElement | null>(null);

	async function send(interrupt: boolean) {
		const text = draft.trim();
		if (!text || sending) return;
		sending = interrupt ? 'now' : 'queue';
		error = '';
		draft = '';
		onsent(text, interrupt);
		try {
			await api.runs.sendMessage(runId, text, interrupt);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			onfailed?.(text);
			// Give the words back unless something new was typed meanwhile.
			if (!draft.trim()) draft = text;
		} finally {
			sending = '';
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			void send(e.metaKey || e.ctrlKey);
		}
	}

	// Grow with the text, up to a third of the window.
	$effect(() => {
		void draft;
		if (!ta) return;
		ta.style.height = 'auto';
		ta.style.height = `${Math.min(ta.scrollHeight, 220)}px`;
	});
</script>

<div class="comp lg">
	<textarea
		class="bare"
		bind:this={ta}
		bind:value={draft}
		rows="1"
		placeholder={t('run.composer.placeholder')}
		onkeydown={onKey}
		aria-label={t('run.composer.aria')}></textarea>
	<div class="foot">
		<span class="keys">
			<span><span class="k">↵</span> {t('run.composer.keyQueue')}</span>
			<span><span class="k">⌘↵</span> {t('run.composer.keyNow')}</span>
			<span class="nl"><span class="k">⇧↵</span> {t('run.composer.keyNewline')}</span>
		</span>
		{#if error}<span class="err" title={error}>{error}</span>{/if}
		<span class="spacer"></span>
		<button
			class="btn-md"
			onclick={() => send(true)}
			aria-busy={sending === 'now'}
			disabled={!!sending || !draft.trim()}
			title={t('run.composer.sendNowTitle')}>{t('run.composer.sendNow')}</button
		>
		<button
			class="btn-primary btn-md"
			onclick={() => send(false)}
			aria-busy={sending === 'queue'}
			disabled={!!sending || !draft.trim()}
			title={t('run.composer.sendTitle')}>{t('run.composer.send')}</button
		>
	</div>
</div>

<style>
	.comp {
		flex: none;
		margin: 0 12px 12px;
		border-radius: var(--r-float);
		display: flex;
		flex-direction: column;
	}
	/* .bare: the composer's glass is the field. */
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
	.err {
		color: var(--ros);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	/* Drop key hints as the Session window narrows (a container query on
	 * the window, so its own threshold). */
	@container (max-width: 520px) {
		.keys .nl,
		.keys > span:nth-child(2) {
			display: none;
		}
	}
</style>
