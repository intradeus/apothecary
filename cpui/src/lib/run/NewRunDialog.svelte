<script lang="ts">
	// The Runs page's "New run" (DESIGN 7.4). A run starts from a ticket, on the
	// ticket's page, where its flow and parameters are chosen: this only picks
	// which ticket, and takes you there.
	import { errorMessage } from '$lib/core/errors';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Ticket } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';

	let {
		open = $bindable(false),
		projectId = ''
	}: {
		open: boolean;
		/** Only this workshop's tickets, when the page is filtered to one. */
		projectId?: string;
	} = $props();

	let tickets = $state<Ticket[] | null>(null);
	let error = $state('');
	let picked = $state('');

	async function load() {
		tickets = null;
		picked = '';
		error = '';
		try {
			const all = await api.tickets.list();
			tickets = all
				.filter((tk) => !projectId || tk.projectId === projectId)
				.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
			picked = tickets[0]?.id ?? '';
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		if (open) untrack(load);
	});

	const options = $derived<SelectOption[]>(
		(tickets ?? []).map((tk) => ({
			value: tk.id,
			label: tk.title,
			detail: projectId ? undefined : fleet.projectName(tk.projectId)
		}))
	);
</script>

<Modal bind:open title={t('page.runs.new')}>
	{#if error}
		<ErrorCard cause={t('page.runs.newLoadError')} detail={error} onretry={load} />
	{:else if tickets === null}
		<Loading />
	{:else if tickets.length === 0}
		<p class="intro">{t('page.runs.newNone')}</p>
	{:else}
		<p class="intro">{t('page.runs.newIntro')}</p>
		<div class="field">
			<label for="new-run-ticket">{t('page.runs.newPick')}</label>
			<Select id="new-run-ticket" {options} bind:value={picked} />
		</div>
	{/if}
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={() => (open = false)}
			>{t('common.cancel')}</button
		>
		{#if tickets?.length === 0}
			<a class="btn btn-primary" href={to.newTicket(projectId ? { project: projectId } : undefined)}
				>{t('page.desk.newTicket')}</a
			>
		{:else}
			<button class="btn-primary" disabled={!picked} onclick={() => goto(to.ticket(picked))}
				>{t('page.runs.newContinue')}</button
			>
		{/if}
	{/snippet}
</Modal>

<style>
	.intro {
		margin: 0;
		color: var(--text-muted);
		font-size: var(--fs-ui);
		text-wrap: pretty;
	}
</style>
