<script lang="ts">
	// Workshop setup (DESIGN 7.9, edit).
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { Flow, Integration, Project } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import ProjectForm from '$lib/workshop/ProjectForm.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	const projectId = $derived(page.params.workshop!);
	let project = $state<Project | null>(null);
	let integrations = $state<Integration[]>([]);
	let flows = $state<Flow[]>([]);
	let error = $state('');

	async function load() {
		error = '';
		try {
			[project, integrations, flows] = await Promise.all([
				api.projects.get(projectId),
				api.integrations.list(),
				// This workshop's own flows, plus the templates it has not adopted:
				// the default-flow picker offers exactly what a run could use.
				api.flows.list(projectId)
			]);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(load);

	const name = $derived(project?.name ?? fleet.projectById.get(projectId)?.name ?? '…');
</script>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.workshops.title'), href: to.workshops() },
		{ label: name, href: to.workshop(projectId) },
		{ label: t('page.settings.crumb') }
	]}
	title={name}
	accent={t('page.settings.accent')}
>
	{#snippet meta()}
		<span>{t('page.settings.meta')}</span>
	{/snippet}
</PageHeader>

<div class="page glass">
	{#if error}
		<ErrorCard cause={t('page.settings.loadError')} detail={error} onretry={load} />
	{:else if !project}
		<Skeleton pane lines={6} />
	{:else}
		{#key project.id}
			<ProjectForm
				{integrations}
				{flows}
				existing={project}
				oncancel={() => goto(to.workshop(projectId))}
				onsaved={async (p, envcheck) => {
					await fleet.refresh();
					await goto(to.workshop(p.id, envcheck ? 'health' : undefined));
				}}
			/>
		{/key}
	{/if}
</div>
