<script lang="ts" module>
	export interface CodeLine {
		/** add / del / hunk / meta, or plain context. */
		kind?: 'add' | 'del' | 'ctx' | 'hunk' | 'meta';
		/** Line number (one gutter), or old/new (two). */
		n?: number;
		old?: number;
		text: string;
	}
</script>

<script lang="ts">
	// Code and diffs (DESIGN 4.10): JetBrains Mono on --code-bg, a right-aligned
	// gutter (two for side-aware diffs), add/del tints, a truncation footer
	// and an honest binary state. Parsing lives in $lib/diff; this renders.
	import { t } from '$lib/i18n/index.svelte';

	let {
		lines,
		gutters = 1,
		truncated,
		binary,
		maxHeight
	}: {
		lines: CodeLine[];
		gutters?: 1 | 2;
		/** "showing 400 of 1,204 lines" — shown under the code. */
		truncated?: string;
		/** "Binary file · 24 KB" — shown instead of the code. */
		binary?: string;
		maxHeight?: number;
	} = $props();
</script>

{#if binary}
	<div class="bin">{binary}</div>
{:else}
	<div
		class="diff"
		class:two={gutters === 2}
		style:max-height={maxHeight ? `${maxHeight}px` : null}
	>
		{#each lines as l, i (i)}
			<div class="dl {l.kind ?? ''}">
				{#if gutters === 2}<span class="ln">{l.old ?? ''}</span>{/if}
				<span class="ln">{l.n ?? ''}</span><span>{l.text}</span>
			</div>
		{/each}
	</div>
	{#if truncated}<div class="diff-foot">{t('ui.code.truncated', { detail: truncated })}</div>{/if}
{/if}

<style>
	.bin {
		padding: 28px 16px;
		text-align: center;
		color: var(--text-muted);
		font-size: var(--fs-label);
		background: var(--code-bg);
		border-radius: var(--r-sm);
	}
</style>
