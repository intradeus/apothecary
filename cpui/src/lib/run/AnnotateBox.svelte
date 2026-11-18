<script lang="ts">
	// The Files viewer's box for a selection: a comment on the picked lines,
	// sent as a chip into the chat that can take it (the held session's
	// composer, else Ask's). With neither open it is off, and says why. ↵
	// sends, ⇧↵ is a newline, Escape lets the selection go.
	import { t } from '$lib/i18n/index.svelte';
	import type { FileLoc, RunCheckout } from '$lib/domain';
	import { annotationRef, makeAnnotation } from './annotation';
	import type { ComposerKind, ComposerRegistry } from './composerTarget.svelte';

	let {
		registry,
		loc,
		from,
		to,
		quote,
		checkouts,
		onsent,
		oncancel
	}: {
		registry: ComposerRegistry;
		loc: FileLoc;
		/** The selected lines, 1-based and inclusive. */
		from: number;
		to: number;
		/** Their text. */
		quote: string;
		checkouts: RunCheckout[];
		/** The annotation is in that chat's composer. */
		onsent: (kind: ComposerKind) => void;
		oncancel?: () => void;
	} = $props();

	let comment = $state('');
	const target = $derived(registry.active);
	const ref = $derived(annotationRef(loc, from, to, checkouts));

	function send() {
		if (!target) return;
		target.add(makeAnnotation(loc, from, to, quote, comment, checkouts));
		comment = '';
		onsent(target.kind);
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			oncancel?.();
		} else if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			send();
		}
	}
</script>

<div class="annotate" role="group" aria-label={ref}>
	<textarea
		class="bare"
		bind:value={comment}
		rows="2"
		placeholder={t('run.annotate.placeholder')}
		aria-label={t('run.annotate.comment', { ref })}
		onkeydown={onKey}></textarea>
	<div class="foot">
		{#if !target}<span class="reason">{registry.reason}</span>{/if}
		<span class="spacer"></span>
		{#if oncancel}
			<button class="btn-ghost btn-sm" onclick={oncancel}>{t('run.annotate.cancel')}</button>
		{/if}
		<button class="btn-primary btn-sm" disabled={!target} onclick={send}
			>{t(target?.kind === 'ask' ? 'run.annotate.toAsk' : 'run.annotate.toSession')}</button
		>
	</div>
</div>

<style>
	.annotate {
		display: flex;
		flex-direction: column;
		width: min(420px, 100%);
		border: 1px solid var(--island-line);
		border-radius: var(--r-btn);
		background: var(--island-bg);
		filter: var(--island-drop);
	}
	textarea {
		padding: 8px 10px;
		resize: none;
		overflow-wrap: anywhere;
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 6px 6px 10px;
		font-size: var(--fs-meta);
		min-width: 0;
	}
	.reason {
		color: var(--text-muted);
		min-width: 0;
	}
</style>
