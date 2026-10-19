<script lang="ts">
	// Flow picker (DESIGN 4.14): "Default · <name>" first (the '' value — the
	// caller's fallbackLabel says what it means), then the workshop's own
	// flows, then the built-in templates it has not adopted (both run as-is).
	// If the list cannot be loaded it degrades to free text.
	import { api } from '$lib/api';
	import { lore } from '$lib/lore';
	import type { Flow } from '$lib/domain';

	let {
		projectId,
		value = $bindable(''),
		fallbackLabel,
		id
	}: {
		projectId: string;
		value?: string;
		/** What '' means here, e.g. "Default · full-integration". */
		fallbackLabel: string;
		id?: string;
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
			.then((fs) => (flows = fs))
			.catch(() => (failed = true));
	});

	const owned = $derived(
		(flows ?? []).filter((f) => !f.builtin).sort((a, b) => a.name.localeCompare(b.name))
	);
	const templates = $derived(
		(flows ?? []).filter((f) => f.builtin).sort((a, b) => a.name.localeCompare(b.name))
	);
</script>

{#if failed}
	<div class="fs">
		<input {id} bind:value placeholder={fallbackLabel} class="mono" />
		<span class="help"
			>Flows could not be listed — type a flow name, or leave empty for the default.</span
		>
	</div>
{:else}
	<select {id} bind:value class="mono" disabled={!flows}>
		{#if !flows}
			<option {value}>Loading flows…</option>
		{:else}
			<option value="">{fallbackLabel}</option>
			{#if owned.length}
				<optgroup label="{lore('workshop')} flows">
					{#each owned as f (f.name)}<option value={f.name}
							>{f.name}{f.shadowsBuiltin ? ' (customised template)' : ''}</option
						>{/each}
				</optgroup>
			{/if}
			{#if templates.length}
				<optgroup label="Built-in templates">
					{#each templates as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
				</optgroup>
			{/if}
		{/if}
	</select>
{/if}

<style>
	.fs {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
</style>
