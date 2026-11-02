<script lang="ts">
	// Observability, for the instance admin: a link out to the operator's
	// Grafana (APOTHECARY_GRAFANA_URL), where the platform dashboards live —
	// requests, the database pool, the scheduler, LLM spend, every user's
	// run facts. The CPUI shows none of that itself: it is the operator's
	// view of the instance, not anyone's view of their work.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	let url = $state<string | null>(null);
	let error = $state('');

	const load = async () => {
		error = '';
		try {
			url = await api.system.observability();
		} catch (err) {
			error = errorMessage(err);
		}
	};
	onMount(load);
</script>

{#if error}
	<ErrorCard cause={t('page.observability.loadError')} detail={error} onretry={load} />
{:else if url === null}
	<Skeleton pane lines={2} />
{:else if url}
	<section class="glass pane">
		<div class="pane-head">
			<h2>{t('page.observability.head')}</h2>
			<span class="muted small">{t('page.observability.headSub')}</span>
		</div>
		<div class="link">
			<a class="btn-primary" href={url} target="_blank" rel="noopener"
				>{t('page.observability.open')} {I.external}</a
			>
			<span class="mono muted small truncate">{url}</span>
		</div>
	</section>
{:else}
	<EmptyState title={t('page.observability.unsetTitle')} text={t('page.observability.unsetText')} />
{/if}

<style>
	.link {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 14px 16px;
		min-width: 0;
	}
</style>
