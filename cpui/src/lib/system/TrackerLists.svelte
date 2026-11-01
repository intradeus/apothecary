<script lang="ts">
	// A ticket tracker's statuses and types. On a remote tracker the rows are
	// the tracker's own: only Done, the flow step and the change type are
	// ours. On the internal tracker the lists are the user's to edit whole.
	// Deleting a row tickets still use is refused until the user says which
	// row their tickets move to.
	import { onMount } from 'svelte';
	import { api, isInUse } from '$lib/api';
	import {
		CHANGE_TYPES,
		INTERNAL_TRACKER,
		type ChangeType,
		type Integration,
		type TrackerStatus,
		type TrackerType
	} from '$lib/domain';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import Select from '$lib/ui/Select.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import {
		NO_STEP,
		moveRow,
		namesProblem,
		removedRows,
		statusListProblem,
		stepOptions,
		type StepChoice
	} from './trackerRows';

	let { integration: i }: { integration: Integration } = $props();

	const internal = $derived(i.kind === INTERNAL_TRACKER);
	const changeTypes = CHANGE_TYPES.map((c) => ({ value: c, label: c }));

	type List = 'statuses' | 'types';

	let loaded = $state(false);
	let error = $state('');
	let busy = $state<'' | 'refresh' | 'sync' | List>('');
	let warnings = $state<string[]>([]);
	let statuses = $state<TrackerStatus[]>([]);
	let savedStatuses = $state<TrackerStatus[]>([]);
	let types = $state<TrackerType[]>([]);
	let savedTypes = $state<TrackerType[]>([]);
	/** A save refused because deleted rows are in use: what their tickets move to. */
	let replacing = $state<{ list: List; with: Record<string, string> } | null>(null);

	const statusesDirty = $derived(JSON.stringify(statuses) !== JSON.stringify(savedStatuses));
	const typesDirty = $derived(JSON.stringify(types) !== JSON.stringify(savedTypes));
	const statusProblem = $derived(statusListProblem(statuses));
	const typeProblem = $derived(internal ? namesProblem(types) : '');

	async function load(refresh = false) {
		error = '';
		try {
			const [s, ty] = await Promise.all([
				api.integrations.statuses(i.id, refresh),
				api.integrations.types(i.id, refresh)
			]);
			statuses = s.statuses;
			savedStatuses = s.statuses;
			warnings = s.warnings;
			types = ty;
			savedTypes = ty;
			replacing = null;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			loaded = true;
		}
	}
	onMount(() => void load());

	async function refreshFromTracker() {
		busy = 'refresh';
		await load(true);
		busy = '';
	}

	async function syncNow() {
		busy = 'sync';
		error = '';
		try {
			const r = await api.integrations.sync(i.id);
			toast(t('tracker.synced', { ...r }));
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	async function save(list: List) {
		busy = list;
		error = '';
		const replaceWith = replacing?.list === list ? replacing.with : undefined;
		try {
			if (list === 'statuses') {
				const r = await api.integrations.saveStatuses(i.id, statuses, replaceWith);
				statuses = r.statuses;
				savedStatuses = r.statuses;
				warnings = r.warnings;
			} else {
				const r = await api.integrations.saveTypes(i.id, types, replaceWith);
				types = r;
				savedTypes = r;
			}
			replacing = null;
			toast(t('tracker.saved', { name: i.name }));
		} catch (e) {
			if (isInUse(e) && !replaceWith) replacing = { list, with: {} };
			else error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	const setStep = (index: number, step: StepChoice) =>
		(statuses[index].flowStep = step === NO_STEP ? undefined : step);

	const addStatus = () =>
		(statuses = [
			...statuses,
			{
				id: '',
				integrationId: i.id,
				remoteId: '',
				name: '',
				remoteCategory: '',
				position: statuses.length,
				isDone: false
			}
		]);
	const addType = () =>
		(types = [
			...types,
			{
				id: '',
				integrationId: i.id,
				remoteId: '',
				name: '',
				changeType: 'feat' as ChangeType,
				position: types.length
			}
		]);

	// The deleted rows a refused save asks about, and what they may become:
	// a saved row still in the list.
	const removed = $derived(
		replacing?.list === 'statuses'
			? removedRows(savedStatuses, statuses)
			: replacing?.list === 'types'
				? removedRows(savedTypes, types)
				: []
	);
	const keptOptions = $derived(
		(replacing?.list === 'statuses' ? statuses : types)
			.filter((r) => r.id)
			.map((r) => ({ value: r.id, label: r.name }))
	);
	const replacementsChosen = $derived(
		replacing !== null && removed.every((r) => replacing?.with[r.id])
	);
</script>

{#snippet replace(list: List)}
	{#if replacing?.list === list && replacing}
		<div class="replace" role="group" aria-label={t('tracker.replaceTitle')}>
			<p>{t('tracker.replaceText')}</p>
			{#each removed as r (r.id)}
				<div class="replace-row">
					<span class="truncate">{r.name}</span>
					<span aria-hidden="true">{I.forward}</span>
					<Select
						size="sm"
						options={keptOptions}
						value={replacing.with[r.id]}
						label={t('tracker.replaceFor', { name: r.name })}
						placeholder={t('tracker.replacePick')}
						onchange={(v) => replacing && (replacing.with[r.id] = v)}
					/>
				</div>
			{/each}
			<div class="acts">
				<button
					class="btn-sm btn-primary"
					disabled={!replacementsChosen || busy !== ''}
					onclick={() => save(list)}>{t('tracker.replaceSave')}</button
				>
				<button class="btn-sm" onclick={() => (replacing = null)}>{t('tracker.cancel')}</button>
			</div>
		</div>
	{/if}
{/snippet}

{#snippet rowTools(list: List, index: number, count: number)}
	<td class="tools">
		<button
			class="btn-icon btn-sm"
			disabled={index === 0}
			aria-label={t('tracker.moveUp')}
			onclick={() =>
				list === 'statuses'
					? (statuses = moveRow(statuses, index, -1))
					: (types = moveRow(types, index, -1))}>{I.up}</button
		>
		<button
			class="btn-icon btn-sm"
			disabled={index === count - 1}
			aria-label={t('tracker.moveDown')}
			onclick={() =>
				list === 'statuses'
					? (statuses = moveRow(statuses, index, 1))
					: (types = moveRow(types, index, 1))}>{I.down}</button
		>
		<button
			class="btn-icon btn-sm"
			aria-label={t('tracker.deleteRow')}
			onclick={() =>
				list === 'statuses'
					? (statuses = statuses.filter((_, k) => k !== index))
					: (types = types.filter((_, k) => k !== index))}>{I.close}</button
		>
	</td>
{/snippet}

<div class="lists">
	{#if !internal}
		<div class="acts">
			<button
				class="btn-sm"
				onclick={refreshFromTracker}
				disabled={busy !== ''}
				aria-busy={busy === 'refresh'}>{t('tracker.refresh')}</button
			>
			<button class="btn-sm" onclick={syncNow} disabled={busy !== ''} aria-busy={busy === 'sync'}
				>{t('tracker.syncNow')}</button
			>
		</div>
	{/if}
	{#if error}<div class="error">{error}</div>{/if}
	{#each warnings as w (w)}<div class="warn small">{w}</div>{/each}

	{#if !loaded}
		<Loading />
	{:else}
		<section aria-label={t('tracker.statuses')}>
			<h4>{t('tracker.statuses')}</h4>
			<table>
				<thead>
					<tr>
						<th>{t('tracker.name')}</th>
						<th>{t('tracker.done')}</th>
						<th>{t('tracker.step')}</th>
						{#if internal}<th aria-label={t('tracker.rowTools')}></th>{/if}
					</tr>
				</thead>
				<tbody>
					{#each statuses as s, k (s.id || `new-${k}`)}
						<tr>
							<td>
								{#if internal}
									<input
										class="name"
										aria-label={t('tracker.name')}
										bind:value={statuses[k].name}
									/>
								{:else}{s.name}{/if}
							</td>
							<td>
								<!-- The column says "Done"; the row's name only for assistive tech. -->
								<Checkbox bind:checked={statuses[k].isDone}>
									<span class="vh">{t('tracker.doneFor', { name: s.name })}</span>
								</Checkbox>
							</td>
							<td>
								<Select
									size="sm"
									options={stepOptions(statuses, k)}
									value={s.flowStep ?? NO_STEP}
									label={t('tracker.stepFor', { name: s.name })}
									onchange={(v) => setStep(k, v)}
								/>
							</td>
							{#if internal}{@render rowTools('statuses', k, statuses.length)}{/if}
						</tr>
					{/each}
				</tbody>
			</table>
			{#if statusProblem}<div class="error small">{statusProblem}</div>{/if}
			{@render replace('statuses')}
			<div class="acts">
				{#if internal}<button class="btn-sm" onclick={addStatus}>{t('tracker.addStatus')}</button
					>{/if}
				{#if statusesDirty}
					<button
						class="btn-sm btn-primary"
						disabled={!!statusProblem || busy !== '' || replacing?.list === 'statuses'}
						aria-busy={busy === 'statuses'}
						onclick={() => save('statuses')}>{t('tracker.saveStatuses')}</button
					>
				{/if}
			</div>
		</section>

		<section aria-label={t('tracker.types')}>
			<h4>{t('tracker.types')}</h4>
			<table>
				<thead>
					<tr>
						<th>{t('tracker.name')}</th>
						<th>{t('tracker.changeType')}</th>
						{#if internal}<th aria-label={t('tracker.rowTools')}></th>{/if}
					</tr>
				</thead>
				<tbody>
					{#each types as ty, k (ty.id || `new-${k}`)}
						<tr>
							<td>
								{#if internal}
									<input class="name" aria-label={t('tracker.name')} bind:value={types[k].name} />
								{:else}{ty.name}{/if}
							</td>
							<td>
								<Select
									size="sm"
									mono
									options={changeTypes}
									value={ty.changeType}
									label={t('tracker.changeTypeFor', { name: ty.name })}
									onchange={(v) => (types[k].changeType = v)}
								/>
							</td>
							{#if internal}{@render rowTools('types', k, types.length)}{/if}
						</tr>
					{/each}
				</tbody>
			</table>
			{#if typeProblem}<div class="error small">{typeProblem}</div>{/if}
			{@render replace('types')}
			<div class="acts">
				{#if internal}<button class="btn-sm" onclick={addType}>{t('tracker.addType')}</button>{/if}
				{#if typesDirty}
					<button
						class="btn-sm btn-primary"
						disabled={!!typeProblem || busy !== '' || replacing?.list === 'types'}
						aria-busy={busy === 'types'}
						onclick={() => save('types')}>{t('tracker.saveTypes')}</button
					>
				{/if}
			</div>
		</section>
	{/if}
</div>

<style>
	/* Visually hidden, still read out. */
	.vh {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
		white-space: nowrap;
	}
	.lists {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	h4 {
		margin: 0 0 6px;
		font-size: var(--fs-label);
	}
	table {
		width: 100%;
	}
	.acts {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
		margin-top: 6px;
	}
	.tools {
		white-space: nowrap;
		text-align: right;
	}
	input.name {
		width: 100%;
		min-width: 14ch;
	}
	.warn {
		color: var(--warning-text);
	}
	.replace {
		border: 1px solid var(--warning-line);
		background: var(--warning-bg);
		border-radius: var(--r-sm);
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 8px;
		padding: 8px 10px;
	}
	.replace p {
		margin: 0;
	}
	.replace-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
		align-items: center;
		gap: 8px;
	}
</style>
