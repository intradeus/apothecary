<!-- New ticket (DESIGN 7.6): a two-column form — the ticket on the left,
     "Import from tracker" on the right. ?project= preselects the workshop. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/urls';
	import { fleet } from '$lib/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import TicketForm from '$lib/TicketForm.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

	const initialProject = $derived(page.url.searchParams.get('project') ?? '');
</script>

<svelte:head><title>{t('page.newTicket.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.tickets.title'), href: to.tickets() },
		{ label: t('page.newTicket.crumb') }
	]}
	title={t('page.newTicket.crumb')}
	accent={t('page.newTicket.accent')}
>
	{#snippet meta()}
		<span>{t('page.newTicket.meta')}</span>
	{/snippet}
</PageHeader>

<div class="page c">
	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard
			cause={t('page.newTicket.loadError')}
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<Skeleton pane lines={8} />
	{:else}
		<TicketForm
			projects={fleet.projects}
			{initialProject}
			onsaved={(tk) => goto(to.ticket(tk.id))}
			oncancel={() => goto(to.tickets(initialProject ? { project: initialProject } : undefined))}
		/>
	{/if}
</div>
