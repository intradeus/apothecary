<!-- New ticket (DESIGN 7.6), written on a workshop's internal tracker.
     ?project= preselects the workshop; one on a remote tracker takes new
     issues there, so the page links to it instead of a form. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { isInternal } from '$lib/tickets/tracker';
	import TicketForm from '$lib/tickets/TicketForm.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { I } from '$lib/design/icons';

	const asked = $derived(page.url.searchParams.get('project') ?? '');
	const project = $derived(fleet.projectById.get(asked) ?? fleet.projects[0]);
	const source = $derived(project?.ticketSource);
	const options = $derived<SelectOption[]>(
		fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	);
	const setProject = (id: string) =>
		void goto(to.newTicket({ project: id }), {
			replaceState: true,
			keepFocus: true,
			noScroll: true
		});
</script>

<svelte:head><title>{t('page.newTicket.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{
			label: t('page.tickets.title'),
			href: to.tickets(project ? { project: project.id } : undefined)
		},
		{ label: t('page.newTicket.crumb') }
	]}
	title={t('page.newTicket.crumb')}
	accent={t('page.newTicket.accent')}
>
	{#snippet meta()}
		<span>{t('page.newTicket.meta')}</span>
	{/snippet}
</PageHeader>

<div class="page glass">
	{#if fleet.updatedAt === null && fleet.error}
		<ErrorCard
			cause={t('page.newTicket.loadError')}
			detail={fleet.error}
			onretry={() => void fleet.refresh()}
		/>
	{:else if fleet.updatedAt === null}
		<Skeleton pane lines={8} />
	{:else if !project}
		<Callout hue="warning" glyph={I.warn}>
			{@html th('page.newTicket.noWorkshops', {
				integrationsHref: to.system('integrations'),
				workshopHref: to.newWorkshop()
			})}
		</Callout>
	{:else}
		<div class="field ws">
			<label for="t-project">{t('page.newTicket.workshop')}</label>
			<Select id="t-project" {options} value={project.id} onchange={setProject} />
		</div>
		{#if isInternal(source)}
			{#key project.id}
				<TicketForm
					{project}
					onsaved={(tk) => goto(to.workshopTicket(project.id, tk.key))}
					oncancel={() => goto(to.tickets({ project: project.id }))}
				/>
			{/key}
		{:else}
			<Callout hue="accent" glyph={I.info}>
				{t('page.newTicket.remote', { name: source?.name ?? '' })}
				{#if source?.home}
					<a href={source.home} target="_blank" rel="noreferrer"
						>{t('tickets.openIn', { name: source.name })} {I.external}</a
					>
				{/if}
			</Callout>
		{/if}
	{/if}
</div>

<style>
	.ws {
		max-width: 320px;
	}
</style>
