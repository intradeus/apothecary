<script lang="ts">
	// Where a workshop's next ticket is written: here on the internal tracker,
	// in the tracker itself for a remote source (owner rule). Nothing while
	// the integrations load, or when there is no telling where the tracker is.
	import { t } from '$lib/i18n/index.svelte';
	import type { Integration, Project } from '$lib/domain';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { isInternal, sourceOf, trackerHome } from '$lib/tickets/tracker';

	let {
		project,
		integrations,
		label,
		cls = ''
	}: {
		project: Pick<Project, 'id' | 'ticketSourceId'>;
		integrations: Integration[] | null;
		/** The New ticket wording of the place it sits in. */
		label: string;
		/** Button classes, e.g. 'btn-sm btn-primary'. */
		cls?: string;
	} = $props();

	const source = $derived(sourceOf(project, integrations));
	const home = $derived(trackerHome(source));
</script>

{#if isInternal(source)}
	<a class="btn {cls}" href={to.newTicket({ project: project.id })}>{I.add} {label}</a>
{:else if source && home}
	<a class="btn {cls}" href={home} target="_blank" rel="noreferrer"
		>{t('tickets.openIn', { name: source.name })} {I.external}</a
	>
{/if}
