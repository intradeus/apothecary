<script lang="ts">
	// What a run started on nothing is given (FLOWLANG §1 operates_on:
	// nothing), in the New run dialog: its flow, a name, the branch it starts
	// from, and either the name of its fresh branch or that it checks the
	// branch out itself, then the flow's options. Everything is optional: left
	// alone, the run is named for the time and gets a fresh branch, named
	// after it, off the repository's default.
	//
	// The fresh branch's name is written for the person — after the run's
	// name, following it — until they write their own; ticking or unticking
	// the checkbox puts the written one back.
	import { untrack } from 'svelte';
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import type { Branch, SkillsetRef } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import FlowSelect from '$lib/flow/FlowSelect.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import { manualRunBranch } from './manualRunName';
	import StartRunOptions from './StartRunOptions.svelte';

	let {
		projectId,
		flow = $bindable(''),
		title = $bindable(''),
		branch = $bindable(''),
		onBranch = $bindable(false),
		newBranch = $bindable(''),
		blocked = $bindable(false),
		params = $bindable({}),
		skillsets = $bindable([]),
		namePlaceholder,
		tail
	}: {
		projectId: string;
		/** The flow picked; '' = the workshop's default, when it fits. */
		flow?: string;
		title?: string;
		/** The branch the run starts from; '' = the repository's default. */
		branch?: string;
		/** Work on that branch itself, not on a fresh one forked from it. */
		onBranch?: boolean;
		/** What the fresh branch is called: after the run until the person
		 *  writes another; '' = the engine names it. Not read when the run
		 *  works on the branch itself. */
		newBranch?: string;
		/** Out: the start cannot be sent as it stands. */
		blocked?: boolean;
		params?: Record<string, string>;
		skillsets?: SkillsetRef[];
		/** What an unnamed run will be called. */
		namePlaceholder: string;
		/** What the fresh branch's written name ends on (branchTail). */
		tail: string;
	} = $props();

	const project = $derived(fleet.projectById.get(projectId));

	// The repository's branches, its default picked first. A repository host
	// that cannot be asked leaves the pick to the engine: the default branch.
	let branches = $state<Branch[]>([]);
	let branchesErr = $state('');
	$effect(() => {
		const pid = projectId;
		branches = [];
		branchesErr = '';
		branch = '';
		api.projects.branches(pid).then(
			(list) => {
				if (pid !== projectId) return;
				branches = list;
				branch = (list.find((b) => b.isDefault) ?? list[0])?.name ?? '';
			},
			(e: unknown) => {
				if (pid === projectId) branchesErr = errorMessage(e);
			}
		);
	});
	const branchOptions = $derived<SelectOption[]>(
		branches.map((b) => ({ value: b.name, label: b.isDefault ? `${I.star} ${b.name}` : b.name }))
	);
	const shownBranch = $derived(branch || project?.repo.defaultBranch || '');

	// The fresh branch's name, written here until the person writes their
	// own. A form shown again (another tab and back) keeps what they wrote.
	const written = $derived(manualRunBranch(title.trim() || namePlaceholder, tail));
	let own = $state(untrack(() => newBranch !== '' && newBranch !== written));
	$effect(() => {
		if (!own) newBranch = written;
	});

	// A fresh branch is one the repository does not hold: the name of one it
	// does would have the run work on that branch, which is the checkbox's to
	// say.
	const taken = $derived.by(() => {
		const typed = newBranch.trim();
		const held = typed === shownBranch || branches.some((b) => b.name === typed);
		return !onBranch && typed && held ? typed : '';
	});
	/** Starting a run in the workshop needs run:operate there. */
	const canOperate = $derived(fleet.can(projectId, 'run:operate'));
	$effect(() => {
		blocked = !!taken || !canOperate;
	});
</script>

{#if !canOperate}<p class="error">{t('common.needsPermission')}</p>{/if}

<div class="field">
	<label for="nothing-flow">{t('page.ticket.flow')}</label>
	<FlowSelect
		id="nothing-flow"
		{projectId}
		operatesOn="nothing"
		fallbackFlow={project?.defaultFlow}
		bind:value={flow}
		fallbackLabel={t('page.ticket.defaultFlow', {
			flow: project?.defaultFlow || t('page.ticket.projectDefault')
		})}
	/>
</div>
<div class="field">
	<label for="nothing-name">{t('page.runs.nothingName')}</label>
	<input id="nothing-name" bind:value={title} placeholder={namePlaceholder} maxlength="200" />
	<span class="help">{t('page.runs.nothingNameHelp')}</span>
</div>
<div class="field">
	<label for="nothing-branch">{t('page.runs.nothingBranch')}</label>
	{#if branches.length}
		<Select id="nothing-branch" mono options={branchOptions} bind:value={branch} />
	{:else}
		<input
			id="nothing-branch"
			class="mono"
			disabled
			placeholder={branchesErr
				? t('page.ticket.branchesUnavailable')
				: t('page.ticket.loadingBranches')}
			value={branchesErr ? (project?.repo.defaultBranch ?? '') : ''}
		/>
	{/if}
	<Checkbox
		bind:checked={onBranch}
		label={shownBranch
			? t('page.runs.nothingOnBranch', { branch: shownBranch })
			: t('page.runs.nothingOnDefaultBranch')}
		onchange={() => (own = false)}
	/>
	<span class="help"
		>{#if branchesErr}{t('page.runs.nothingBranchesFailed')}
		{/if}{onBranch
			? t('page.runs.nothingOnBranchHelp', { branch: shownBranch })
			: t('page.runs.nothingForkHelp', { branch: shownBranch })}</span
	>
</div>
{#if !onBranch}
	<div class="field">
		<label for="nothing-new-branch">{t('page.runs.nothingNewBranch')}</label>
		<input
			id="nothing-new-branch"
			class="mono"
			bind:value={newBranch}
			oninput={() => (own = true)}
			placeholder={t('page.runs.nothingNewBranchPlaceholder')}
			maxlength="200"
			aria-invalid={!!taken}
		/>
		{#if taken}
			<div class="error">{t('page.runs.nothingNewBranchExists', { branch: taken })}</div>
		{:else}
			<span class="help">{t('page.runs.nothingNewBranchHelp')}</span>
		{/if}
	</div>
{/if}
{#if project && (flow.trim() || project.defaultFlow)}
	<StartRunOptions
		{projectId}
		flowName={flow.trim() || project.defaultFlow}
		bind:params
		bind:skillsets
		askMR={false}
		idPrefix="nothing"
	/>
{/if}
