<script lang="ts">
	// Flow picker (DESIGN 4.14): "Default · <name>" first (the '' value — the
	// caller's fallbackLabel says what it means), then the workshop's own
	// flows, then the built-in templates it has not adopted (both run as-is) —
	// only those whose runs are started on what the caller starts them on,
	// when it says (an automation's kind). If the list cannot be loaded it
	// degrades to free text.
	import { api } from '$lib/api';
	import type { Flow } from '$lib/domain';
	import type { OperatesOn } from '$lib/domain/flowdoc';
	import { t } from '$lib/i18n/index.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';

	let {
		projectId,
		value = $bindable(''),
		fallbackLabel,
		id,
		operatesOn,
		fallbackFlow
	}: {
		projectId: string;
		value?: string;
		/** What '' means here, e.g. "Default · full-integration". */
		fallbackLabel: string;
		id?: string;
		/** Only the flows whose runs are started on this (operates_on). */
		operatesOn?: OperatesOn;
		/** The flow '' stands for, when the caller knows it: left out when its
		 *  runs are not started on operatesOn. */
		fallbackFlow?: string;
	} = $props();

	let flows = $state<Flow[] | null>(null);
	let failed = $state(false);

	$effect(() => {
		const pid = projectId;
		if (!pid) return;
		flows = null;
		failed = false;
		api.flows
			.list(pid)
			.then((fs) => {
				if (pid === projectId) flows = fs;
			})
			.catch(() => {
				if (pid === projectId) failed = true;
			});
	});

	const fits = $derived((flows ?? []).filter((f) => !operatesOn || f.operatesOn === operatesOn));
	const fallbackFits = $derived(
		!operatesOn || !fallbackFlow || fits.some((f) => f.name === fallbackFlow)
	);
	// A choice the kind no longer fits moves to one it does.
	$effect(() => {
		if (!flows || !operatesOn) return;
		const ok = value === '' ? fallbackFits : fits.some((f) => f.name === value);
		if (!ok) value = fallbackFits ? '' : (fits[0]?.name ?? '');
	});
	const owned = $derived(
		fits.filter((f) => !f.builtin).sort((a, b) => a.name.localeCompare(b.name))
	);
	const templates = $derived(
		fits.filter((f) => f.builtin).sort((a, b) => a.name.localeCompare(b.name))
	);
	const options = $derived<SelectOption[]>(
		!flows
			? []
			: [
					...(fallbackFits ? [{ value: '', label: fallbackLabel }] : []),
					...owned.map((f) => ({
						value: f.name,
						label: `${f.name}${f.shadowsBuiltin ? t('flowselect.customised') : ''}`,
						group: t('flowselect.workshopFlows')
					})),
					...templates.map((f) => ({
						value: f.name,
						label: f.name,
						group: t('flowselect.templates')
					}))
				]
	);
</script>

{#if failed}
	<div class="fs">
		<input {id} bind:value placeholder={fallbackLabel} class="mono" />
		<span class="help">{t('flowselect.failed')}</span>
	</div>
{:else}
	<Select
		{id}
		{options}
		bind:value
		mono
		disabled={!flows}
		placeholder={flows ? undefined : t('flowselect.loading')}
	/>
{/if}

<style>
	.fs {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
</style>
