<script lang="ts">
	// A page's assistant button (the local assistant): it opens the fill
	// modal, and beside it the page's guard says what the last fill did —
	// the fields it changed, with Undo — or holds a fill that would overwrite
	// newer edits until the person picks.
	import { SPARKLE_PATH } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import type { PageGuard } from './fillGuard.svelte';

	let {
		guard,
		onclick,
		disabled = false
	}: {
		guard: PageGuard;
		/** Open the fill modal. */
		onclick: () => void;
		disabled?: boolean;
	} = $props();
</script>

<span class="fb row">
	<button type="button" class="btn-sm fill" {onclick} {disabled}>
		<svg viewBox="0 0 24 24" aria-hidden="true"><path d={SPARKLE_PATH} /></svg>
		{t('assistant.fillButton')}
	</button>
	{#if guard.pending}
		<span class="mark row" role="status">
			<span>{t('assistant.stale')}</span>
			<button type="button" class="btn-sm" onclick={() => guard.applyPending()}
				>{t('assistant.applyAnyway')}</button
			>
			<button type="button" class="btn-sm" onclick={() => guard.discardPending()}
				>{t('assistant.keepMine')}</button
			>
		</span>
	{:else if guard.changed.length}
		<span class="mark row" role="status">
			<span>{t('assistant.changed', { fields: guard.changed.join(', ') })}</span>
			<button type="button" class="btn-sm" onclick={() => guard.revert()}
				>{t('assistant.undo')}</button
			>
		</span>
	{/if}
</span>

<style>
	.fb {
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.fill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	svg {
		width: 14px;
		height: 14px;
		fill: var(--accent);
	}
	.mark {
		gap: 6px;
		align-items: center;
		font-size: var(--fs-meta);
		color: var(--text-muted);
	}
</style>
