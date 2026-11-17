<script lang="ts" module>
	import type { Integration } from '$lib/domain';
	import { INTERNAL_TRACKER } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

	/** How a ticket source is named in the workshop's setup and its
	 *  automations: the internal one as the built-in tracker. */
	export const ticketSourceLabel = (i: Integration) =>
		i.kind === INTERNAL_TRACKER ? t('form.project.ticketSource.builtIn') : `${i.name} (${i.kind})`;
</script>

<script lang="ts">
	// Inside 2 · Workshop: the ticket source its ticket pages, new-ticket form
	// and tracker-query automations read, live — one of the user's
	// ticket-source integrations, the built-in tracker included. Empty is the
	// built-in tracker on create (the engine makes it on first use), the
	// current source on edit.
	import { to } from '$lib/core/urls';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import { th } from '$lib/i18n/index.svelte';
	import FormSub from './FormSub.svelte';

	let {
		sources,
		ticketSourceId = $bindable()
	}: {
		/** The user's ticket-source integrations. */
		sources: Integration[];
		ticketSourceId: string;
	} = $props();

	// The built-in tracker first; until it exists, '' stands for it.
	const options = $derived<SelectOption[]>([
		...(sources.some((s) => s.kind === INTERNAL_TRACKER)
			? []
			: [{ value: '', label: t('form.project.ticketSource.builtIn') }]),
		...[...sources]
			.sort((a, b) => Number(b.kind === INTERNAL_TRACKER) - Number(a.kind === INTERNAL_TRACKER))
			.map((s) => ({ value: s.id, label: ticketSourceLabel(s) }))
	]);
</script>

<FormSub>
	<h3>{t('form.project.ticketSource.head')}</h3>
	<div class="field">
		<label for="p-tickets">{t('form.project.ticketSource.label')}</label>
		<Select id="p-tickets" {options} bind:value={ticketSourceId} />
		<div class="help">
			{@html th('form.project.ticketSource.help', { href: to.system('integrations') })}
		</div>
	</div>
</FormSub>
