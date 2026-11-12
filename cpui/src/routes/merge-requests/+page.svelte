<!-- Merge requests (DESIGN 7.6): the other work item runs start on, beside
     tickets — every workshop's open ones. A row opens its page, where a run
     is started on it. ?project= keeps to one workshop. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import MergeRequestList from '$lib/mrs/MergeRequestList.svelte';
	import WorkshopFilter from '$lib/shell/WorkshopFilter.svelte';

	let q = $state('');
	let count = $state(0);
	const projectFilter = $derived(page.url.searchParams.get('project') ?? '');

	function setProject(id: string) {
		void goto(to.mergeRequests(id ? { project: id } : undefined), {
			replaceState: true,
			keepFocus: true,
			noScroll: true
		});
	}
</script>

<svelte:head><title>{t('page.mr.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		...(projectFilter
			? [{ label: fleet.projectName(projectFilter), href: to.workshop(projectFilter) }]
			: []),
		{ label: t('page.mr.title') }
	]}
	title={t('page.mr.title')}
	accent={t('page.mr.accent')}
>
	{#snippet meta()}
		<span>{t('page.mr.count', { count })}</span>
		<span>{t('page.mr.meta')}</span>
	{/snippet}
</PageHeader>

<div class="page glass">
	<div class="filters">
		<SearchInput bind:value={q} placeholder={t('page.mr.search')} />
		<WorkshopFilter value={projectFilter} onchange={setProject} />
	</div>
	<MergeRequestList
		projectId={projectFilter}
		query={q}
		bind:count
		onclear={() => {
			q = '';
			setProject('');
		}}
	/>
</div>
