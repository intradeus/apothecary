<script lang="ts">
	// An annotation waiting in a composer (DESIGN 4.12): its reference, its
	// lines folded away until opened, its comment (still yours to change) and
	// a remove. Sent, it becomes part of the message's text.
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import type { Annotation } from './annotation';

	let {
		annotation = $bindable(),
		onremove,
		disabled = false
	}: {
		annotation: Annotation;
		onremove: () => void;
		/** The message carrying it is on its way: nothing moves. */
		disabled?: boolean;
	} = $props();

	let open = $state(false);
	const ref = $derived(annotation.ref);
	const count = $derived(annotation.quote.replace(/\n$/, '').split('\n').length);
</script>

<div class="chip" role="group" aria-label={ref}>
	<div class="top">
		<button
			class="linklike toggle"
			aria-expanded={open}
			title={t(open ? 'run.annotate.hide' : 'run.annotate.show', { ref })}
			onclick={() => (open = !open)}
		>
			<span>{open ? I.menu : I.caret}</span>
			<span class="ref mono">{ref}</span>
			<span class="faint">{t('run.annotate.lines', { count })}</span>
		</button>
		<span class="spacer"></span>
		<button
			class="btn-icon btn-sm"
			aria-label={t('run.annotate.remove', { ref })}
			title={t('run.annotate.remove', { ref })}
			{disabled}
			onclick={onremove}>{I.close}</button
		>
	</div>
	{#if open}<pre class="quote">{annotation.quote}</pre>{/if}
	<input
		class="comment"
		bind:value={annotation.comment}
		readonly={disabled}
		placeholder={t('run.annotate.placeholder')}
		aria-label={t('run.annotate.comment', { ref })}
	/>
</div>

<style>
	.chip {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 4px 6px 6px 8px;
		border: 1px solid var(--line);
		border-radius: var(--r-row);
		background: var(--fill-1);
		font-size: var(--fs-meta);
		min-width: 0;
	}
	.top {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.toggle {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		padding: 2px 0;
		color: var(--text-muted);
	}
	.ref {
		color: var(--text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.quote {
		margin: 0;
		padding: 6px 8px;
		max-height: 160px;
		overflow: auto;
		border-radius: var(--r-xs);
		background: var(--code-bg);
		color: var(--text-code);
		font-family: var(--font-code);
		font-size: var(--fs-meta);
	}
	.comment {
		font-size: var(--fs-meta);
	}
</style>
