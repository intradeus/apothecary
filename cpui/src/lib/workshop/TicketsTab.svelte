<script lang="ts">
	// Workshop tickets tab (DESIGN 7.6): the workshop's source, read live.
	// Tickets are written here only on the internal tracker; a remote one
	// takes new issues itself, so the tab links to it instead.
	import { t } from '$lib/i18n/index.svelte';
	import type { Project } from '$lib/domain';
	import TicketList from '$lib/tickets/TicketList.svelte';
	import NewTicketLink from '$lib/tickets/NewTicketLink.svelte';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';

	let { project }: { project: Project } = $props();
</script>

<HeaderAction>
	<NewTicketLink {project} label={t('workshop.tickets.new')} cls="btn-primary btn-lg" />
</HeaderAction>

<TicketList projectId={project.id}>
	{#snippet empty()}
		<NewTicketLink {project} label={t('workshop.tickets.new')} cls="btn-primary" />
	{/snippet}
</TicketList>
