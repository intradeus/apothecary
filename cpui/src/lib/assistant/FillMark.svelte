<script lang="ts">
	// What the assistant's last fill did to the page, on the page: the fields
	// it changed, with Undo, or a fill that would overwrite newer edits held
	// until the person picks. Nothing while there is nothing to say; the
	// assistant's panel says the same beside its answer.
	import { t } from '$lib/i18n/index.svelte';
	import type { PageGuard } from './fillGuard.svelte';

	let { guard }: { guard: PageGuard } = $props();
</script>

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

<style>
	.mark {
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
		font-size: var(--fs-meta);
		color: var(--text-muted);
	}
</style>
