<script lang="ts">
	// Limits (DESIGN 7.10): the instance's concurrency and environment caps.
	// One setting for everyone — they protect the machine — stored by the
	// engine and changed here by an admin; everyone else reads them.
	import { fleet } from '$lib/fleet.svelte';
	import { session } from '$lib/session.svelte';
	import { api } from '$lib/api';
	import type { Limits } from '$lib/domain';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import SaveBar from '$lib/ui/SaveBar.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';

	const cap = (n: number | null | undefined) =>
		n === null ? t('page.limits.unlimited') : n === undefined ? '…' : String(n);

	// The admin's draft: a count per cap, or null for "no cap".
	let draft = $state<Limits | null>(null);
	let busy = $state(false);
	let error = $state('');
	let savedAt = $state<number | null>(null);

	$effect(() => {
		if (session.admin && fleet.limits && draft === null) draft = { ...fleet.limits };
	});

	const dirty = $derived(
		!!draft &&
			!!fleet.limits &&
			(draft.maxConcurrentRuns !== fleet.limits.maxConcurrentRuns ||
				draft.maxEnvironments !== fleet.limits.maxEnvironments)
	);
	const valid = $derived(
		!!draft &&
			[draft.maxConcurrentRuns, draft.maxEnvironments].every(
				(v) => v === null || (Number.isInteger(v) && v > 0)
			)
	);

	async function save() {
		if (!draft || !valid) return;
		busy = true;
		error = '';
		try {
			const saved = await api.system.setLimits(draft);
			fleet.limits = saved;
			draft = { ...saved };
			savedAt = Date.now();
			toast(t('page.limits.saved'));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	function discard() {
		if (fleet.limits) draft = { ...fleet.limits };
		error = '';
	}
</script>

{#snippet capField(id: string, label: string, key: 'maxConcurrentRuns' | 'maxEnvironments')}
	{#if draft}
		<div class="field">
			<input
				{id}
				aria-label={label}
				class="mono"
				type="number"
				min="1"
				step="1"
				disabled={draft[key] === null}
				value={draft[key] ?? ''}
				oninput={(e) => {
					const n = Number(e.currentTarget.value);
					if (draft) draft[key] = e.currentTarget.value === '' ? 0 : n;
				}}
			/>
			<Toggle
				label={t('page.limits.noCap')}
				checked={draft[key] === null}
				onchange={(on) => {
					if (draft) draft[key] = on ? null : (fleet.limits?.[key] ?? null) || 1;
				}}
			/>
			<span class="mut small">{t('page.limits.noCap')}</span>
		</div>
	{/if}
{/snippet}

<form class="lim" onsubmit={(e) => e.preventDefault()}>
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('page.limits.head')}</h2>
			<span class="mut small">{t('page.limits.headSub')}</span>
		</div>
		<SettingRow label={t('page.limits.concLabel')} forId="lim-runs" help={t('page.limits.concHelp')}>
			{#snippet note()}
				{t('page.limits.nowRunning', { count: fleet.slots.length })}{#if fleet.queued.length}
					· {t('page.limits.queued', { count: fleet.queued.length })}{/if}
			{/snippet}
			{#if session.admin && draft}
				{@render capField('lim-runs', t('page.limits.concLabel'), 'maxConcurrentRuns')}
			{:else}
				<span class="mono val">{cap(fleet.limits?.maxConcurrentRuns)}</span>
			{/if}
		</SettingRow>
		<SettingRow label={t('page.limits.envsLabel')} forId="lim-envs" help={t('page.limits.envsHelp')}>
			{#snippet note()}{t('page.limits.envsNow', { count: fleet.envs.length })}{/snippet}
			{#if session.admin && draft}
				{@render capField('lim-envs', t('page.limits.envsLabel'), 'maxEnvironments')}
			{:else}
				<span class="mono val">{cap(fleet.limits?.maxEnvironments)}</span>
			{/if}
		</SettingRow>
	</section>

	<Callout hue="brand" glyph={I.info}
		>{t('page.limits.envNote')}{#if !session.admin}
			{' '}{t('page.limits.adminOnly')}{/if}</Callout
	>

	{#if error}<ErrorCard cause={t('page.limits.saveFail')} detail={error} />{/if}

	{#if session.admin && draft}
		<div class="bar">
			<SaveBar
				dirty={dirty && valid}
				{busy}
				{savedAt}
				note={t('page.limits.applyNote')}
				onsave={save}
				ondiscard={discard}
			/>
		</div>
	{/if}
</form>

<style>
	.lim {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-height: 100%;
	}
	.val {
		font-size: var(--fs-lead);
	}
	.field {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.field input {
		width: 7em;
	}
	.bar {
		position: sticky;
		bottom: 12px;
		margin-top: auto;
	}
</style>
