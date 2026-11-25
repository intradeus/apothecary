<script lang="ts">
	// Limits (System → Provisioner): the instance's concurrency and environment
	// caps, and the sandbox sizes (the default a workshop that sets none gets,
	// and the ceiling every workshop's is held to). One setting for everyone —
	// they protect the machine — stored by the engine and changed here by an
	// admin; everyone else reads them.
	import { errorMessage } from '$lib/core/errors';
	import { fleet } from '$lib/app/fleet.svelte';
	import { session } from '$lib/app/session.svelte';
	import { api } from '$lib/api';
	import { aboveCeiling, outOfBounds, unsetSize, type Limits, type SandboxSize } from '$lib/domain';
	import SizeFields from '$lib/ui/SizeFields.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import SaveBar from '$lib/ui/SaveBar.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';

	const cap = (n: number | null | undefined) =>
		n === null ? t('page.limits.unlimited') : n === undefined ? '…' : String(n);

	// The admin's draft: a count per cap, or null for "no cap".
	let draft = $state<Limits | null>(null);
	let busy = $state(false);
	let error = $state('');
	let savedAt = $state<number | null>(null);

	// A draft owns its sizes: binding the fields must never write through to
	// the stored limits.
	const copy = (l: Limits): Limits => ({
		...l,
		sandbox: {
			default: { ...(l.sandbox?.default ?? unsetSize()) },
			ceiling: { ...(l.sandbox?.ceiling ?? unsetSize()) },
			idleMinutes: l.sandbox?.idleMinutes ?? 0
		}
	});
	const idleText = (m: number | undefined) =>
		m ? t('page.limits.idleIs', { m }) : t('page.limits.idleNever');
	const sizeText = (s: SandboxSize | undefined) =>
		s && (s.cpus !== null || s.memoryGiB !== null)
			? t('page.limits.sizeIs', {
					cpus: s.cpus ?? t('page.limits.sizeAny'),
					memory: s.memoryGiB ?? t('page.limits.sizeAny')
				})
			: t('page.limits.sizeUnset');

	$effect(() => {
		if (session.admin && fleet.limits && draft === null) draft = copy(fleet.limits);
	});

	const dirty = $derived(
		!!draft && !!fleet.limits && JSON.stringify(draft) !== JSON.stringify(copy(fleet.limits))
	);
	const sandbox = $derived(
		draft?.sandbox ?? { default: unsetSize(), ceiling: unsetSize(), idleMinutes: 0 }
	);
	const overCeiling = $derived(aboveCeiling(sandbox.default, sandbox.ceiling));
	const defaultOut = $derived(outOfBounds(sandbox.default));
	const ceilingOut = $derived(outOfBounds(sandbox.ceiling));
	const sizesValid = $derived(defaultOut.length === 0 && ceilingOut.length === 0);
	const valid = $derived(
		!!draft &&
			[draft.maxConcurrentRuns, draft.maxEnvironments].every(
				(v) => v === null || (Number.isInteger(v) && v > 0)
			) &&
			sizesValid &&
			Number.isInteger(sandbox.idleMinutes) &&
			sandbox.idleMinutes >= 0 &&
			overCeiling.length === 0
	);

	async function save() {
		if (!draft || !valid) return;
		busy = true;
		error = '';
		try {
			const saved = await api.system.setLimits(draft);
			fleet.limits = saved;
			draft = copy(saved);
			savedAt = Date.now();
			toast(t('page.limits.saved'));
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	function discard() {
		if (fleet.limits) draft = copy(fleet.limits);
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
		</div>
	{/if}
{/snippet}

<form id="limits" class="lim" onsubmit={(e) => e.preventDefault()}>
	<section class="glass pane">
		<div class="pane-head">
			<h2>{t('page.limits.head')}</h2>
			<span class="muted small">{t('page.limits.headSub')}</span>
		</div>
		<SettingRow
			label={t('page.limits.concLabel')}
			forId="lim-runs"
			help={t('page.limits.concHelp')}
		>
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
		<SettingRow
			label={t('page.limits.envsLabel')}
			forId="lim-envs"
			help={t('page.limits.envsHelp')}
		>
			{#snippet note()}{t('page.limits.envsNow', { count: fleet.envs.length })}{/snippet}
			{#if session.admin && draft}
				{@render capField('lim-envs', t('page.limits.envsLabel'), 'maxEnvironments')}
			{:else}
				<span class="mono val">{cap(fleet.limits?.maxEnvironments)}</span>
			{/if}
		</SettingRow>
	</section>

	<section class="glass pane">
		<div class="pane-head">
			<h2>{t('page.limits.sizeHead')}</h2>
			<span class="muted small">{t('page.limits.sizeSub')}</span>
		</div>
		<SettingRow label={t('page.limits.sizeDefault')} help={t('page.limits.sizeDefaultHelp')}>
			{#if session.admin && draft?.sandbox}
				<SizeFields
					idPrefix="lim-size-default"
					bind:size={draft.sandbox.default}
					placeholder={{ cpus: t('page.limits.sizeNone'), memoryGiB: t('page.limits.sizeNone') }}
					invalid={[...overCeiling, ...defaultOut]}
				/>
			{:else}
				<span class="mono val">{sizeText(fleet.limits?.sandbox?.default)}</span>
			{/if}
		</SettingRow>
		<SettingRow label={t('page.limits.sizeCeiling')} help={t('page.limits.sizeCeilingHelp')}>
			{#if session.admin && draft?.sandbox}
				<SizeFields
					idPrefix="lim-size-ceiling"
					bind:size={draft.sandbox.ceiling}
					placeholder={{ cpus: t('page.limits.sizeNone'), memoryGiB: t('page.limits.sizeNone') }}
					invalid={ceilingOut}
				/>
			{:else}
				<span class="mono val">{sizeText(fleet.limits?.sandbox?.ceiling)}</span>
			{/if}
		</SettingRow>
		<SettingRow label={t('page.limits.idle')} forId="lim-idle" help={t('page.limits.idleHelp')}>
			{#if session.admin && draft?.sandbox}
				<input
					id="lim-idle"
					aria-label={t('page.limits.idle')}
					class="mono"
					type="number"
					min="0"
					step="1"
					placeholder={t('page.limits.idleNever')}
					value={draft.sandbox.idleMinutes || ''}
					oninput={(e) => {
						if (draft?.sandbox)
							draft.sandbox.idleMinutes =
								e.currentTarget.value === '' ? 0 : Number(e.currentTarget.value);
					}}
				/>
			{:else}
				<span class="mono val">{idleText(fleet.limits?.sandbox?.idleMinutes)}</span>
			{/if}
		</SettingRow>
		{#if overCeiling.length > 0}
			<p class="error small">{t('page.limits.sizeOverCeiling')}</p>
		{/if}
		{#if !sizesValid}
			<p class="error small">{t('ui.size.bounds')}</p>
		{/if}
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
		flex: 1;
		max-width: var(--settings-max);
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
		bottom: var(--foot-stick);
		margin-top: auto;
	}
</style>
