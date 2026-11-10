<script lang="ts">
	// The Runs page's "New run" (DESIGN 7.4). A run starts on a work item — a
	// ticket, a merge request (WORK_ITEMS) — on that item's page, where its
	// flow and parameters are chosen: this only picks which, and takes you
	// there. One tab per kind; a kind whose items live in a workshop's
	// repository asks which workshop first.
	import { errorMessage } from '$lib/core/errors';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { fleet } from '$lib/app/fleet.svelte';
	import { WORK_ITEMS, type WorkItemOption } from '$lib/app/workItems';
	import type { OperatesOn } from '$lib/domain/flowdoc';
	import { t } from '$lib/i18n/index.svelte';
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
		/** Only this workshop's items, when the page is filtered to one. */
		projectId?: string;
	} = $props();

	let kind = $state<OperatesOn>(WORK_ITEMS[0].kind);
	const item = $derived(WORK_ITEMS.find((w) => w.kind === kind)!);

	// The workshop a per-workshop kind reads from: the page's, else picked.
	let workshop = $state('');
	const projectOptions = $derived<SelectOption[]>(
		fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	);
	$effect(() => {
		if (!open) return;
		const first = fleet.projects[0]?.id ?? '';
		untrack(() => {
			if (projectId) workshop = projectId;
			else if (!workshop) workshop = first;
		});
	});
	/** Where the current kind reads its items: '' = every workshop. */
	const scope = $derived(projectId || (item.perWorkshop ? workshop : ''));

	let options = $state<WorkItemOption[] | null>(null);
	let error = $state('');
	let picked = $state('');
	let reading = 0;

	async function load() {
		const which = ++reading;
		options = null;
		picked = '';
		error = '';
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
		if (item.perWorkshop && !scope) return;
		untrack(load);
	});

	const selectOptions = $derived<SelectOption[]>(
		(options ?? []).map((o) => ({ value: o.value, label: o.label, detail: o.detail }))
	);
	const chosen = $derived(options?.find((o) => o.value === picked));
</script>

<Modal bind:open title={t('page.runs.new')}>
	<Tabs
		label={t('page.runs.newMode')}
		bind:value={kind}
		items={WORK_ITEMS.map((w) => ({ value: w.kind, label: w.one }))}
	/>
	{#if item.perWorkshop && !projectId}
		<div class="field">
			<label for="new-run-workshop">{t('page.runs.newPickWorkshop')}</label>
			<Select id="new-run-workshop" options={projectOptions} bind:value={workshop} />
		</div>
	{/if}
	{#if error}
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
		{#if options?.length === 0 && item.create}
			<a class="btn btn-primary" href={item.create.href(projectId)}>{item.create.label}</a>
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
