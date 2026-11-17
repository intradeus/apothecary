<script lang="ts">
	// The Runs page's "New run" (DESIGN 7.4). A run starts on a work item — a
	// ticket, a merge request (WORK_ITEMS) — on that item's page, where its
	// flow and parameters are chosen: this only picks which, and takes you
	// there. One tab per kind; a kind whose items live in a workshop's
	// repository asks which workshop first.
	//
	// The last tab is a run on nothing (operates_on: nothing): there is no
	// item and no page of one, so the run is started here — the workshop, a
	// flow whose runs start on nothing, and the few things such a start may
	// say (NothingRunForm).
	import { errorMessage } from '$lib/core/errors';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import NewTicketLink from '$lib/tickets/NewTicketLink.svelte';
	import { WORK_ITEMS, type WorkItemOption } from '$lib/app/workItems';
	import { shortId } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import type { Integration, SkillsetRef } from '$lib/domain';
	import type { OperatesOn } from '$lib/domain/flowdoc';
	import { t } from '$lib/i18n/index.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { branchTail, manualRunName } from './manualRunName';
	import NothingRunForm from './NothingRunForm.svelte';

	let {
		open = $bindable(false),
		projectId = ''
	}: {
		open: boolean;
		/** Only this workshop's items, when the page is filtered to one. */
		projectId?: string;
	} = $props();

	let kind = $state<OperatesOn>(WORK_ITEMS[0].kind);
	/** The kind of work item picked; none on the "nothing" tab. */
	const item = $derived(WORK_ITEMS.find((w) => w.kind === kind));

	// The workshop a per-workshop kind reads from: the page's, else picked.
	let workshop = $state('');
	const projectOptions = $derived<SelectOption[]>(
		fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	);
	// Where an empty workshop's next item is written (its ticket source);
	// read once per opening.
	let integrations = $state<Integration[] | null>(null);
	$effect(() => {
		if (!open) return;
		const first = fleet.projects[0]?.id ?? '';
		untrack(() => {
			if (projectId) workshop = projectId;
			else if (!workshop) workshop = first;
			api.integrations
				.list()
				.then((l) => (integrations = l))
				.catch(() => (integrations = []));
		});
	});
	/** Where the current kind reads its items: '' = every workshop. */
	const scope = $derived(projectId || (!item || item.perWorkshop ? workshop : ''));
	const scopeProject = $derived(fleet.projectById.get(scope));

	let options = $state<WorkItemOption[] | null>(null);
	let error = $state('');
	let picked = $state('');
	let reading = 0;

	async function load() {
		const which = ++reading;
		options = null;
		picked = '';
		error = '';
		if (!item) return;
		try {
			const got = await item.options(scope);
			if (which !== reading) return;
			options = got;
			picked = got[0]?.value ?? '';
		} catch (e) {
			if (which === reading) error = errorMessage(e);
		}
	}

	// Read again on each opening, on a tab switch and on a workshop pick.
	$effect(() => {
		if (!open) return;
		void [kind, scope];
		if (!item || (item.perWorkshop && !scope)) return;
		untrack(load);
	});

	// A run on nothing: what its start says, fresh on each opening and for
	// each workshop (a flow's options are that workshop's).
	let flow = $state('');
	let title = $state('');
	let branch = $state('');
	let onBranch = $state(false);
	let newBranch = $state('');
	let blocked = $state(false);
	let params = $state<Record<string, string>>({});
	let skillsets = $state<SkillsetRef[]>([]);
	let starting = $state(false);
	let startError = $state('');
	let unnamed = $state('');
	let tail = $state('');
	$effect(() => {
		if (!open) return;
		void scope;
		untrack(() => {
			flow = '';
			title = '';
			onBranch = false;
			newBranch = '';
			blocked = false;
			params = {};
			skillsets = [];
			startError = '';
			unnamed = manualRunName();
			tail = branchTail();
		});
	});

	async function startOnNothing() {
		if (!scope || starting || blocked) return;
		starting = true;
		startError = '';
		try {
			const { runId } = await api.projects.startRunOnNothing(scope, {
				flow: flow.trim() || undefined,
				// As the form showed it: the name the fresh branch was written after.
				title: title.trim() || unnamed,
				branch: branch || undefined,
				onBranch,
				// On the branch itself there is no fresh branch to name.
				newBranch: onBranch ? undefined : newBranch.trim() || undefined,
				params,
				skillsets
			});
			open = false;
			void fleet.refresh();
			toast(t('page.ticket.runStarted', { id: shortId(runId) }));
			await goto(to.run(runId));
		} catch (e) {
			startError = errorMessage(e);
		} finally {
			starting = false;
		}
	}

	const selectOptions = $derived<SelectOption[]>(
		(options ?? []).map((o) => ({ value: o.value, label: o.label, detail: o.detail }))
	);
	const chosen = $derived(options?.find((o) => o.value === picked));
</script>

<Modal bind:open title={t('page.runs.new')}>
	<Tabs
		label={t('page.runs.newMode')}
		bind:value={kind}
		items={[
			...WORK_ITEMS.map((w) => ({ value: w.kind, label: w.one })),
			{ value: 'nothing' as const, label: t('page.runs.newModeNothing') }
		]}
	/>
	{#if (!item || item.perWorkshop) && !projectId}
		<div class="field">
			<label for="new-run-workshop">{t('page.runs.newPickWorkshop')}</label>
			<Select id="new-run-workshop" options={projectOptions} bind:value={workshop} />
		</div>
	{/if}
	{#if !item}
		<p class="intro">{t('page.runs.newIntroNothing')}</p>
		{#if scope}
			<!-- Afresh for each workshop and each opening (the tail is drawn then). -->
			{#key `${scope} ${tail}`}
				<NothingRunForm
					projectId={scope}
					bind:flow
					bind:title
					bind:branch
					bind:onBranch
					bind:newBranch
					bind:blocked
					bind:params
					bind:skillsets
					namePlaceholder={unnamed}
					{tail}
				/>
			{/key}
		{/if}
		{#if startError}<ErrorCard cause={t('page.runs.nothingStartFail')} detail={startError} />{/if}
	{:else if error}
		<ErrorCard cause={item.loadError} detail={error} onretry={load} />
	{:else if options === null}
		<Loading />
	{:else if options.length === 0}
		<p class="intro">{item.none}</p>
	{:else}
		<p class="intro">{item.intro}</p>
		<div class="field">
			<label for="new-run-item">{item.one}</label>
			<Select id="new-run-item" options={selectOptions} bind:value={picked} />
		</div>
	{/if}
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={() => (open = false)}
			>{t('common.cancel')}</button
		>
		{#if !item}
			<button
				class="btn-primary"
				disabled={!scope || starting || blocked}
				aria-busy={starting}
				onclick={startOnNothing}>{t('page.ticket.startRunBtn')}</button
			>
		{:else if options?.length === 0 && item?.create && scopeProject}
			<NewTicketLink
				project={scopeProject}
				{integrations}
				label={item.create.label}
				cls="btn-primary"
			/>
		{:else}
			<button class="btn-primary" disabled={!chosen} onclick={() => chosen && goto(chosen.href)}
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
