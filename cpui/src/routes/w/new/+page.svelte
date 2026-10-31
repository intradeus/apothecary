<script lang="ts">
	// New workshop (DESIGN 7.9, create).
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Flow, Integration } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/core/format';
	import ProjectForm from '$lib/workshop/ProjectForm.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';

	let integrations = $state<Integration[]>([]);
	let flows = $state<Flow[]>([]);
	let loaded = $state(false);
	let error = $state('');

	async function load() {
		error = '';
		try {
			// A workshop that does not exist yet owns no flows: it starts on a
			// template and authors or copies its own afterwards.
			[integrations, flows] = await Promise.all([api.integrations.list(), api.flows.templates()]);
			loaded = true;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(load);

	const missing = $derived(
		(['repository', 'provisioner', 'harness'] as const).filter(
			(p) => !integrations.some((i) => i.port === p)
		)
	);
</script>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.workshops.title'), href: to.workshops() },
		{ label: t('page.newTicket.crumb') }
	]}
	title={t('page.newWorkshop.title')}
	accent={t('page.newWorkshop.accent')}
>
	{#snippet meta()}
		<span>{t('page.newWorkshop.meta')}</span>
	{/snippet}
</PageHeader>

<div class="page glass">
	{#if error}
		<ErrorCard cause={t('page.newWorkshop.loadError')} detail={error} onretry={load} />
	{:else if !loaded}
		<Skeleton pane lines={6} />
	{:else if missing.length}
		<EmptyState
			title={t('page.newWorkshop.integrationsFirst')}
			text={t('page.newWorkshop.integrationsText', {
				count: missing.length,
				list: missing.map(portLabel).join(', ')
			})}
		>
			<a class="btn btn-primary" href={to.system('integrations')}
				>{t('page.newWorkshop.openIntegrations')}</a
			>
		</EmptyState>
	{:else}
		<ProjectForm
			{integrations}
			{flows}
			oncancel={() => goto(to.workshops())}
			onsaved={async (p, envcheck) => {
				await fleet.refresh();
				await goto(to.workshop(p.id, envcheck ? 'health' : undefined));
			}}
		/>
	{/if}
</div>
