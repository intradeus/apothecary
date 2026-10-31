<script lang="ts">
	// The composer (DESIGN 4.12). Nobody talks to an agent without taking it
	// over, so until then it is only the Take over button. Take over holds the
	// agent's session: while held (sky), its turns wait for your next message
	// instead of ending the stage, and Release hands the task back — the stage
	// can only end after that.
	//
	// ↵ sends, delivered when the agent's turn ends; ⇧↵ is a newline. Sending
	// is optimistic: the box clears and the bubble shows at once; if the engine
	// refuses, the text comes back with the reason.
	import { api } from '$lib/api';
	import { fmtClockDuration } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { RunHold } from '$lib/domain';

	let {
		runId,
		onsent,
		onfailed,
		hold,
		releasing = false,
		now = Date.now(),
		ontakeover,
		onrelease
	}: {
		runId: string;
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
		ontakeover?: () => Promise<boolean>;
		onrelease?: () => Promise<boolean>;
	} = $props();

	let draft = $state('');
	let sending = $state(false);
	let holding = $state(false);
	let error = $state('');
	let ta = $state<HTMLTextAreaElement | null>(null);

	const held = $derived(!!hold);
	const heldFor = $derived(hold ? fmtClockDuration(now - new Date(hold.since).getTime()) : '');

	async function takeOver() {
		if (!ontakeover || holding) return;
		holding = true;
		error = '';
		try {
			await ontakeover();
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
		const text = draft.trim();
		if (!text || sending) return;
		sending = true;
		error = '';
		draft = '';
		onsent(text);
		try {
			await api.runs.sendMessage(runId, text);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			onfailed?.(text);
			// Give the words back unless something new was typed meanwhile.
			if (!draft.trim()) draft = text;
		} finally {
			sending = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			void send();
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

<div class="comp in" class:held>
	{#if held}
		<div class="hold">
			<span class="ldot sm pulse hue-sky"></span>
			<span class="hue-sky">{t('run.composer.held', { duration: heldFor })}</span>
			{#if hold && hold.messages}
				<span class="mut">· {t('run.composer.heldMessages', { count: hold.messages })}</span>
			{/if}
		</div>
		<textarea
			class="bare"
			bind:this={ta}
			bind:value={draft}
			rows="1"
			placeholder={t('run.composer.placeholder')}
			onkeydown={onKey}
			aria-label={t('run.composer.aria')}></textarea>
	{/if}
	<div class="foot">
		{#if held}
			<span class="keys">
				<span><span class="k">↵</span> {t('run.composer.keySend')}</span>
				<span class="nl"><span class="k">⇧↵</span> {t('run.composer.keyNewline')}</span>
			</span>
		{:else}
			<span class="keys">{t('run.composer.takeOverHint')}</span>
		{/if}
		{#if error}<span class="err" title={error}>{error}</span>{/if}
		<span class="spacer"></span>
		{#if held}
			<button
				class="btn-md hue-sky"
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
				disabled={sending || !draft.trim()}
				title={t('run.composer.sendTitle')}>{t('run.composer.send')}</button
			>
		{:else}
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
		margin: 0 calc(12px + var(--fabClear, 0px)) 12px 12px;
		border-radius: var(--r-btn);
		background: var(--code);
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
		border-color: var(--brandLn);
	}
	/* Held: the agent is in your hands — the attention colour, as a
	 * waiting ping's card. */
	.comp.held {
		border-color: var(--skyLn);
		background: var(--skyBg);
		box-shadow: 0 0 var(--glow-lg) var(--skyGlow);
	}
	.comp.held:focus-within {
		border-color: var(--sky);
	}
	.hold {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 14px 0;
		font-size: var(--fs-meta);
		font-weight: 600;
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
		color: var(--rosTx);
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
