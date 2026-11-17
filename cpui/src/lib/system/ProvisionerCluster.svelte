<script lang="ts">
	// The instance's provisioner (System → Provisioner): where every workshop's
	// workspaces are built. Not a person's integration — the deployment fills
	// it (APOTHECARY_K8S_*), an admin overrides a field, everyone else reads.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { api, isNotFound } from '$lib/api';
	import { session } from '$lib/app/session.svelte';
	import type { Integration, KindDescriptor } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import IntegrationCard from './IntegrationCard.svelte';

	let provisioner = $state<Integration | null>(null);
	let descriptor = $state<KindDescriptor | undefined>(undefined);
	let phase = $state<'loading' | 'ready' | 'none' | 'error'>('loading');
	let error = $state('');

	async function load() {
		try {
			const [p, kinds] = await Promise.all([api.system.provisioner(), api.system.kinds()]);
			provisioner = p;
			descriptor = kinds.find((d) => d.port === 'provisioner' && d.kind === p.kind);
			phase = 'ready';
		} catch (e) {
			if (isNotFound(e)) phase = 'none';
			else {
				error = errorMessage(e);
				phase = 'error';
			}
		}
	}
	onMount(load);
</script>

<section class="glass pane" aria-labelledby="prov-cluster">
	<div class="pane-head">
		<h2 id="prov-cluster">{t('page.provisioner.clusterHead')}</h2>
		<span class="muted small">{t('page.provisioner.clusterSub')}</span>
	</div>
	<div class="pane-body">
		{#if phase === 'loading'}
			<Skeleton lines={3} />
		{:else if phase === 'none'}
			<EmptyState inline title={t('page.provisioner.none')} text={t('page.provisioner.noneText')} />
		{:else if phase === 'error'}
			<ErrorCard cause={t('page.provisioner.loadError')} detail={error} onretry={load} />
		{:else if provisioner}
			<IntegrationCard
				integration={provisioner}
				{descriptor}
				instance
				editable={session.admin}
				onchanged={load}
			/>
			{#if !session.admin}<p class="muted small">{t('page.provisioner.adminOnly')}</p>{/if}
		{/if}
	</div>
</section>

<style>
	.pane-body p {
		margin: 10px 0 0;
	}
</style>
