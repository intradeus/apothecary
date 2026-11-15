<script lang="ts">
	// A ticket's status in its tracker — the tracker's own name, tinted by
	// whether the tracker counts it done. Not a run state: that is StatusChip's.
	import { t } from '$lib/i18n/index.svelte';

	let {
		status,
		done,
		size = 'sm'
	}: { status: string; done: boolean; size?: 'sm' | 'md' } = $props();
</script>

{#if status}
	<span
		class="pill flat {done ? 'tint-success' : 'tint-muted'}"
		class:sm={size === 'sm'}
		title={done ? t('tickets.statusDone', { name: status }) : t('tickets.status', { name: status })}
		>{status}</span
	>
{:else}
	<span class="faint" title={t('tickets.noStatusHelp')}>—</span>
{/if}
