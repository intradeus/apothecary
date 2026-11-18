<!-- Tickets (DESIGN 7.6): one workshop's source, read live. A ticket belongs
     to no workshop, so the list needs one picked (?project=, the first one
     by default). -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import NewWorkshopLink from '$lib/workshop/NewWorkshopLink.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { Integration } from '$lib/domain';
	import { sourceOf } from '$lib/tickets/tracker';
	import NewTicketLink from '$lib/tickets/NewTicketLink.svelte';
	import TicketList from '$lib/tickets/TicketList.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

	let integrations = $state<Integration[] | null>(null);
	onMount(() => {
		api.integrations
			.list()
			.then((l) => (integrations = l))
			.catch(() => (integrations = []));
	});

	const asked = $derived(page.url.searchParams.get('project') ?? '');
	const project = $derived(fleet.projectById.get(asked) ?? fleet.projects[0]);
	const source = $derived(project ? sourceOf(project, integrations) : undefined);
	const options = $derived<SelectOption[]>(
		fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	);

	function setProject(id: string) {
		void goto(to.tickets({ project: id }), { replaceState: true, keepFocus: true, noScroll: true });
	}
</script>

<svelte:head><title>{t('page.tickets.title')}</title></svelte:head>

{#snippet action(cls: string)}
	{#if project}
		<NewTicketLink
			{project}
			{integrations}
			label={t('page.desk.newTicket')}
			cls="btn-primary {cls}"
		/>
	{/if}
{/snippet}

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		...(project ? [{ label: project.name, href: to.workshop(project.id) }] : []),
		{ label: t('page.tickets.title') }
	]}
	title={t('page.tickets.title')}
	accent={t('page.tickets.accent')}
>
	{#snippet meta()}
		{#if source}<span>{t('page.tickets.from', { name: source.name })}</span>{/if}
		<span>{t('page.tickets.meta')}</span>
	{/snippet}
	{#snippet actions()}{@render action('btn-lg')}{/snippet}
</PageHeader>

<div class="page glass">
	{#if fleet.updatedAt === null && fleet.error}
		<ErrorCard
			cause={t('page.tickets.loadError')}
			detail={fleet.error}
			onretry={() => void fleet.refresh()}
		/>
	{:else if fleet.updatedAt === null}
		<Skeleton pane lines={6} />
	{:else if !project}
		<EmptyState title={t('page.tickets.noWorkshop')} text={t('page.tickets.noWorkshopText')}>
			<NewWorkshopLink label={t('page.tickets.newWorkshop')} />
		</EmptyState>
	{:else}
		<TicketList projectId={project.id}>
			{#snippet filters()}
				<span class="sel">
					<Select
						label={t('page.tickets.workshop')}
						{options}
						value={project.id}
						onchange={setProject}
					/>
				</span>
			{/snippet}
			{#snippet empty()}{@render action('')}{/snippet}
		</TicketList>
	{/if}
</div>

<style>
	.sel {
		display: inline-block;
		min-width: 160px;
	}
	@media (max-width: 760px) {
		.sel {
			display: block;
			width: 100%;
		}
	}
</style>
