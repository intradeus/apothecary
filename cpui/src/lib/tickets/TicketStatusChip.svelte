<script lang="ts">
	// A ticket's status in its tracker — the tracker's own name, tinted by the
	// flow step it stands for. Not a run state: that is StatusChip's.
	import type { FlowStep, TicketStatusRef } from '$lib/domain';
	import type { Hue } from '$lib/design/hues';
	import { t } from '$lib/i18n/index.svelte';

	let { status, size = 'sm' }: { status?: TicketStatusRef; size?: 'sm' | 'md' } = $props();

	const STEP_HUE: Record<FlowStep, Hue> = {
		todo: 'muted',
		in_progress: 'running',
		in_review: 'attention',
		done: 'success',
		blocked: 'danger'
	};
	const hue = $derived<Hue>(
		status?.flowStep ? STEP_HUE[status.flowStep] : status?.isDone ? 'success' : 'muted'
	);
</script>

{#if status}
	<span
		class="pill flat tint-{hue}"
		class:sm={size === 'sm'}
		title={status.isDone
			? t('tickets.statusDone', { name: status.name })
			: t('tickets.status', { name: status.name })}>{status.name}</span
	>
{:else}
	<span class="faint" title={t('tickets.notSyncedHelp')}>{t('tickets.notSynced')}</span>
{/if}
