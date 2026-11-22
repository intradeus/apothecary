<script lang="ts">
	// The assistant's settings: the harness, model and skillsets a new
	// conversation starts with. Picking a harness saves it at once, since
	// the models on offer are that harness's; the skillsets it saves are the
	// saved ones, a pick not yet saved waits for Save.
	import { onMount } from 'svelte';
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { AssistantSkillsetRef, AssistantStatus } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import Select from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';
	import SkillsetPicker from '$lib/skills/SkillsetPicker.svelte';

	let status = $state<AssistantStatus | null>(null);
	let loadError = $state('');
	let error = $state('');
	let busy = $state(false);
	let integration = $state('');
	let model = $state('');
	let skillsets = $state<AssistantSkillsetRef[]>([]);

	async function load() {
		try {
			const s = await api.assistant.status();
			status = s;
			integration = s.choice.integration;
			model = s.choice.model;
			skillsets = s.choice.skillsets;
			loadError = '';
		} catch (e) {
			loadError = errorMessage(e);
		}
	}
	onMount(load);

	async function save(
		choice: { integration: string; model: string; skillsets: AssistantSkillsetRef[] },
		saved?: string
	) {
		busy = true;
		error = '';
		try {
			await api.assistant.setChoice(choice);
			if (saved) toast(saved);
			const kept = skillsets;
			await load();
			// The page's picks stand over the status echo until reloaded.
			skillsets = kept;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	const pickHarness = (id: string) =>
		save({ integration: id, model: '', skillsets: status?.choice.skillsets ?? [] });

	const harnesses = $derived(
		(status?.integrations ?? []).map((i) => ({ value: i.id, label: `${i.name} · ${i.kind}` }))
	);
	const models = $derived([
		{ value: '', label: t('assistant.modelDefault') },
		...(status?.models ?? []).map((m) => ({ value: m, label: m }))
	]);
	// "Choose a harness" is what this page is for; any other reason is shown.
	const offReason = $derived(
		status && !status.enabled && status.reason && !(harnesses.length && !status.choice.integration)
			? status.reason
			: ''
	);
</script>

{#if loadError}
	<ErrorCard cause={t('assistant.off')} detail={loadError} onretry={load} />
{:else if !status}
	<Skeleton lines={4} />
{:else}
	{#if offReason}
		<Callout hue="warning" glyph={I.info}>
			<strong>{t('assistant.off')}</strong> — {offReason}
		</Callout>
	{/if}
	{#if !harnesses.length}
		<p class="help">
			{t('assistant.noHarness')}
			<a href={to.system('integrations')}>{t('page.system.integrations.label')}</a>
		</p>
	{:else}
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				save({ integration, model, skillsets }, t('ui.save.saved'));
			}}
		>
			<div class="field">
				<label for="as-harness">{t('assistant.harness')}</label>
				<Select
					id="as-harness"
					options={harnesses}
					bind:value={integration}
					onchange={pickHarness}
					disabled={busy}
				/>
			</div>
			<div class="field">
				<label for="as-model">{t('assistant.model')}</label>
				<Select id="as-model" options={models} bind:value={model} disabled={busy} />
			</div>
			<div class="field">
				<SkillsetPicker bind:value={skillsets} idPrefix="as" />
			</div>
			<p class="help">{t('assistant.settingsApply')}</p>
			{#if error}<p class="help hue-warning" role="alert">{error}</p>{/if}
			<div>
				<button class="btn-primary" type="submit" disabled={busy} aria-busy={busy}
					>{t('common.save')}</button
				>
			</div>
		</form>
	{/if}
{/if}

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
	}
</style>
