<script lang="ts">
	// A run's merge request link(s): "MR ↗", "MR ×2 ↗", or one "!iid ↗" per
	// merge request with `detail`; a dash before any is opened. Older runs
	// carry only mrUrl (no list), which still links.
	import type { Run } from '$lib/domain';
	import { I } from '$lib/ui/icons';
	import { t } from '$lib/i18n/index.svelte';

	let { run, detail = false }: { run: Run; detail?: boolean } = $props();

	const first = $derived(run.mrs[0]?.url ?? run.mrUrl);
</script>

{#if detail && run.mrs.length > 1}
	<span class="mrs over-stretch">
		{#each run.mrs as mr (mr.url)}
			<a
				href={mr.url}
				target="_blank"
				rel="noreferrer"
				title="{mr.repo !== 'repo' ? `${mr.repo}: ` : ''}{mr.branch}{mr.target
					? ` → ${mr.target}`
					: ''}">!{mr.iid} {I.external}</a
			>
		{/each}
	</span>
{:else if first}
	<a class="mr over-stretch" href={first} target="_blank" rel="noreferrer" title={first}
		>{run.mrs.length > 1 ? t('run.mr.multi', { count: run.mrs.length }) : t('run.mr.one')}
		{I.external}</a
	>
{:else}
	<span class="fnt" title={t('run.mr.none')}>—</span>
{/if}

<style>
	a {
		color: var(--link);
		font-weight: 600;
		font-size: var(--fs-label);
		white-space: nowrap;
	}
	.mrs {
		display: inline-flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.mrs a {
		font-family: var(--mono);
		font-size: var(--fs-help);
	}
</style>
