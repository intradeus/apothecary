<script lang="ts">
	// Where a workshop's next ticket is written: here on the internal tracker,
	// in the tracker itself for a remote source (owner rule). The source comes
	// with the workshop, so whoever it is shared with sees it too. Nothing when
	// there is no telling where the tracker is, nor when the caller's role may
	// not write the workshop's tickets.
	import { t } from '$lib/i18n/index.svelte';
	import { projectCan, type Project } from '$lib/domain';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { isInternal } from '$lib/tickets/tracker';

	let {
		project,
		label,
		cls = ''
	}: {
		project: Pick<Project, 'id' | 'ticketSource' | 'access'>;
		/** The New ticket wording of the place it sits in. */
		label: string;
		/** Button classes, e.g. 'btn-sm btn-primary'. */
		cls?: string;
	} = $props();

	const source = $derived(project.ticketSource);
</script>

{#if isInternal(source)}
	{#if projectCan(project, 'ticket:write')}
		<a class="btn {cls}" href={to.newTicket({ project: project.id })}>{I.add} {label}</a>
	{/if}
{:else if source?.home}
	<a class="btn {cls}" href={source.home} target="_blank" rel="noreferrer"
		>{t('tickets.openIn', { name: source.name })} {I.external}</a
	>
{/if}
