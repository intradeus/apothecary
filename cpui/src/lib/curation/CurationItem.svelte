<script lang="ts">
	// One item of a review document at the gate: kept or dropped (the box),
	// its kind, where it sits, the text as it will be posted (editable, with
	// the way back to the agent's words), the thread it answers beside it,
	// and — folded — why the agent says so, which is never posted.
	import type { ReviewThread } from '$lib/domain';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import ThreadCard from './ThreadCard.svelte';
	import {
		kindHue,
		kindLabel,
		rowsFor,
		shortThread,
		whereOf,
		type CurItem,
		type ItemState
	} from './curation';

	let {
		item,
		state = $bindable(),
		thread,
		isReply = false,
		disabled = false,
		onchange
	}: {
		item: CurItem;
		state: ItemState;
		/** The thread the item is in, when the gate showed the threads. */
		thread?: ReviewThread;
		/** A round's reply (sent) rather than a review's note (posted). */
		isReply?: boolean;
		disabled?: boolean;
		onchange: () => void;
	} = $props();

	const reworded = $derived(state.body.trim() !== item.text.trim());
	const blank = $derived(state.keep && !state.body.trim());
	const where = $derived(whereOf(item));
	const why = $derived(!!(item.rationale || item.evidence || item.example));

	function restore() {
		state.body = item.text;
		onchange();
	}
</script>

<div class="it card-in" class:off={!state.keep} class:invalid={blank}>
	<div class="ih">
		<Checkbox
			bind:checked={state.keep}
			{disabled}
			title={isReply ? t('cur.keepReply') : t('cur.keep')}
			{onchange}
		/>
		{#if item.kind}<span class="pill sm t-{kindHue(item.kind)}">{kindLabel(item.kind)}</span>{/if}
		{#if where}<span class="mono where ell" title={where}>{where}</span>{/if}
		{#if item.thread}
			<span class="pill sm t-brand" title={item.thread}
				>{isReply ? t('cur.answers') : t('cur.continues')} {shortThread(item.thread)}</span
			>
		{/if}
		<span class="spacer"></span>
		{#if reworded}
			<span class="pill sm t-gold">{t('cur.reworded')}</span>
			<button class="linklike sm" onclick={restore} {disabled}>{t('cur.restore')}</button>
		{/if}
	</div>

	{#if item.thread}
		{#if thread}
			<ThreadCard {thread} />
		{:else}
			<p class="help">{t('cur.threadMissing', { id: item.thread })}</p>
		{/if}
	{/if}

	<textarea
		class="txt"
		aria-label={t('cur.textLabel')}
		bind:value={state.body}
		oninput={onchange}
		rows={rowsFor(state.body)}
		{disabled}></textarea>
	{#if blank}<div class="err">{t('cur.blank')}</div>{/if}

	{#if item.canResolve}
		<Checkbox
			size="sm"
			bind:checked={state.resolve}
			disabled={disabled || !state.keep}
			label={t('cur.resolve')}
			{onchange}
		/>
	{/if}

	{#if why}
		<details class="why">
			<summary class="disclosure">{t('cur.why')}</summary>
			<dl class="kv">
				{#if item.rationale}
					<dt>{t('cur.rationale')}</dt>
					<dd><Markdown src={item.rationale} quiet /></dd>
				{/if}
				{#if item.evidence}
					<dt>{t('cur.evidence')}</dt>
					<dd><Markdown src={item.evidence} quiet /></dd>
				{/if}
				{#if item.example}
					<dt>{t('cur.example')}</dt>
					<dd><Markdown src={item.example} quiet /></dd>
				{/if}
			</dl>
		</details>
	{/if}
</div>

<style>
	.it {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 8px 10px 10px;
		transition:
			opacity var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
	}
	.it.off {
		opacity: 0.55;
	}
	.it.off:hover,
	.it.off:focus-within {
		opacity: 0.9;
	}
	.it.invalid {
		border-color: var(--rosLn);
	}
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		flex-wrap: wrap;
		row-gap: 4px;
	}
	.where {
		font-size: var(--fs-id);
		color: var(--mut);
	}
	.txt {
		width: 100%;
		resize: vertical;
		font-size: var(--fs-ui);
		line-height: 1.45;
	}
	.off .txt {
		text-decoration: line-through;
		color: var(--mut);
	}
	.linklike.sm {
		font-size: var(--fs-meta);
	}
	.why {
		font-size: var(--fs-label);
	}
	.why summary {
		color: var(--mut);
		font-weight: 500;
	}
	.why dl {
		margin: 6px 0 0;
	}
	.err {
		margin: 0;
	}
	.help {
		margin: 0;
	}
</style>
