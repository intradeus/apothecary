<script lang="ts">
	// What a workspace-only view shows when there is no live workspace behind
	// it (DESIGN 7.1): the Record keeps every snapshot.
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import { ago } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { RunStore } from './store.svelte';

	let { rs, onrecord }: { rs: RunStore; onrecord: () => void } = $props();

	const title = $derived(
		rs.destroyedAt
			? t('run.noWorkspace.destroyed', { ago: ago(rs.destroyedAt) })
			: rs.run?.state === 'archived'
				? t('run.noWorkspace.archived')
				: rs.run && !rs.run.envId && !rs.terminal
					? t('run.noWorkspace.none')
					: t('run.noWorkspace.noLive')
	);
	const text = $derived(
		rs.run && !rs.run.envId && !rs.terminal
			? t('run.noWorkspace.appears')
			: t('run.noWorkspace.recordKeeps')
	);
</script>

<div class="nw">
	<EmptyState {title} {text} compact>
		<button onclick={onrecord}>{t('run.noWorkspace.openRecord')}</button>
	</EmptyState>
</div>

<style>
	.nw {
		flex: 1;
		min-height: 0;
		padding: 12px;
		display: flex;
		flex-direction: column;
		justify-content: center;
		overflow: auto;
	}
</style>
