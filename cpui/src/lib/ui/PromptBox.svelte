<script lang="ts">
	// The prompt box: where a person writes to an agent (a run's Ask pane, the
	// assistant). The box is the field: it grows with its words up to a
	// limit, ↵ submits and ⇧↵ is a newline, and its foot line holds what rides
	// along (files), the keys, its trouble and the caller's buttons. What sits
	// above the words (annotation chips) and the buttons are the caller's.
	import type { Snippet } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		value = $bindable(''),
		label,
		placeholder = label,
		sendKey = t('ui.prompt.keySend'),
		disabled = false,
		readonly = false,
		accent = false,
		error = '',
		onsubmit,
		oninput,
		onpaste,
		top,
		extras,
		actions
	}: {
		value?: string;
		/** The field's accessible name. */
		label: string;
		placeholder?: string;
		/** What ↵ does, in the keys' line. */
		sendKey?: string;
		disabled?: boolean;
		/** Locked while what was submitted goes up: kept, not editable. */
		readonly?: boolean;
		/** Wears the accent: what it asks lands on the page. */
		accent?: boolean;
		/** The box's line of trouble. */
		error?: string;
		/** ↵ was pressed (never mid-composition, never with ⇧). */
		onsubmit: () => void;
		oninput?: (value: string) => void;
		onpaste?: (e: ClipboardEvent) => void;
		/** Above the words: what the next prompt carries (chips). */
		top?: Snippet;
		/** The foot's leading end: attachments. */
		extras?: Snippet;
		/** The foot's trailing end: the buttons. */
		actions?: Snippet;
	} = $props();

	let ta = $state<HTMLTextAreaElement | null>(null);

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			onsubmit();
		}
	}

	// Grow with the text, up to a limit.
	$effect(() => {
		void value;
		if (!ta) return;
		ta.style.height = 'auto';
		ta.style.height = `${Math.min(ta.scrollHeight, 220)}px`;
	});
</script>

<div class="comp inset" class:accent>
	{#if top}<div class="top">{@render top()}</div>{/if}
	<textarea
		class="bare"
		bind:this={ta}
		bind:value
		rows="1"
		{placeholder}
		onkeydown={onKey}
		oninput={() => oninput?.(value)}
		{onpaste}
		{disabled}
		{readonly}
		aria-label={label}></textarea>
	<div class="foot">
		{#if extras}<span class="extras">{@render extras()}</span>{/if}
		<span class="keys"
			><span><span class="k">↵</span> {sendKey}</span>
			<span class="nl"><span class="k">⇧↵</span> {t('ui.prompt.keyNewline')}</span></span
		>
		{#if error}<span class="error" title={error}>{error}</span>{/if}
		<span class="spacer"></span>
		{@render actions?.()}
	</div>
</div>

<style>
	.comp {
		flex: none;
		container-type: inline-size;
		border-radius: var(--r-btn);
		background: var(--code-bg);
		display: flex;
		flex-direction: column;
	}
	.comp:focus-within {
		border-color: var(--brand-line);
	}
	.comp.accent {
		border-color: var(--accent-line);
	}
	.comp.accent:focus-within {
		border-color: var(--accent);
	}
	.top {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 14px 0;
	}
	/* .bare: the box is the field. */
	textarea {
		min-height: 44px;
		padding: 11px 14px;
		overflow-wrap: anywhere;
		resize: none;
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
	.extras {
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
	/* A narrow box (a drawer) keeps the send key only. */
	@container (max-width: 420px) {
		.keys .nl {
			display: none;
		}
	}
</style>
