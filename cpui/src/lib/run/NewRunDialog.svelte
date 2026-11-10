<script lang="ts">
	// The Runs page's "New run" (DESIGN 7.4). A run starts on one of the two
	// work items — a ticket or a merge request — on its page, where its flow
	// and parameters are chosen: this only picks which, and takes you there.
	// A merge request is no ticket: none is made for it.
	import { errorMessage } from '$lib/core/errors';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { MergeRequestRow, Ticket } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';

	let {
		open = $bindable(false),
		projectId = ''
	}: {
		open: boolean;
		/** Only this workshop's tickets and merge requests, when the page is
		 *  filtered to one. */
		projectId?: string;
	} = $props();

	type Mode = 'ticket' | 'mr';
	let mode = $state<Mode>('ticket');

	// --- a ticket ---
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

	// --- a merge request ---
	// Merge requests live in each workshop's repository: one workshop at a
	// time, the page's when it is filtered to one.
	let mrProject = $state('');
	let mrs = $state<MergeRequestRow[] | null>(null);
	let mrError = $state('');
	let pickedMR = $state('');

	const projectOptions = $derived<SelectOption[]>(
		fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	);
	$effect(() => {
		if (!open) return;
		const want = projectId || untrack(() => mrProject) || fleet.projects[0]?.id || '';
		untrack(() => {
			if (mrProject !== want && (projectId || !mrProject)) mrProject = want;
		});
	});

	async function loadMRs(pid: string) {
		mrs = null;
		mrError = '';
		pickedMR = '';
		try {
			const rows = await api.projects.mergeRequests(pid);
			if (pid !== mrProject) return;
			mrs = rows;
			pickedMR = rows[0] ? String(rows[0].mr.iid) : '';
		} catch (e) {
			if (pid === mrProject) mrError = errorMessage(e);
		}
	}

	$effect(() => {
		const pid = mrProject;
		if (!open || mode !== 'mr' || !pid) return;
		untrack(() => loadMRs(pid));
	});

	const mrOptions = $derived<SelectOption[]>(
		(mrs ?? []).map((r) => ({
			value: String(r.mr.iid),
			label: `!${r.mr.iid} ${r.mr.title}`,
			detail: r.mr.author
		}))
	);

	function next() {
		if (mode === 'ticket') {
			if (picked) void goto(to.ticket(picked));
		} else if (pickedMR && mrProject) {
			void goto(to.mergeRequest(mrProject, Number(pickedMR)));
		}
	}
	const ready = $derived(mode === 'ticket' ? !!picked : !!pickedMR && !!mrProject);
</script>

<Modal bind:open title={t('page.runs.new')}>
	<Tabs
		label={t('page.runs.newMode')}
		bind:value={mode}
		items={[
			{ value: 'ticket', label: t('page.runs.newModeTicket') },
			{ value: 'mr', label: t('page.runs.newModeMR') }
		]}
	/>
	{#if mode === 'ticket'}
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
	{:else}
		<p class="intro">{t('page.runs.newIntroMR')}</p>
		{#if !projectId}
			<div class="field">
				<label for="new-run-workshop">{t('page.runs.newPickWorkshop')}</label>
				<Select id="new-run-workshop" options={projectOptions} bind:value={mrProject} />
			</div>
		{/if}
		{#if mrError}
			<ErrorCard
				cause={t('page.runs.newMRLoadError')}
				detail={mrError}
				onretry={() => loadMRs(mrProject)}
			/>
		{:else if mrs === null}
			<Loading />
		{:else if mrs.length === 0}
			<p class="intro">{t('page.runs.newNoMR')}</p>
		{:else}
			<div class="field">
				<label for="new-run-mr">{t('page.runs.newPickMR')}</label>
				<Select id="new-run-mr" options={mrOptions} bind:value={pickedMR} />
			</div>
		{/if}
	{/if}
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={() => (open = false)}
			>{t('common.cancel')}</button
		>
		{#if mode === 'ticket' && tickets?.length === 0}
			<a class="btn btn-primary" href={to.newTicket(projectId ? { project: projectId } : undefined)}
				>{t('page.desk.newTicket')}</a
			>
		{:else}
			<button class="btn-primary" disabled={!ready} onclick={next}
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
