<script lang="ts">
	// Pick the flow a run uses: the project's own flows, then the built-in
	// templates it has not adopted (both run as-is). '' means "the fallback",
	// whose name the first option spells out. If the list cannot be loaded,
	// it degrades to the free-text input it replaces.
	import { api } from '$lib/api';
	import type { Flow } from '$lib/domain';

	let {
		projectId,
		value = $bindable(''),
		fallbackLabel,
		id
	}: {
		projectId: string;
		value?: string;
		/** What '' means here, e.g. "project default (full-integration)". */
		fallbackLabel: string;
		id?: string;
	} = $props();

	let flows = $state<Flow[] | null>(null);
	let failed = $state(false);

	$effect(() => {
		const pid = projectId;
		if (!pid) return;
		api.flows
			.list(pid)
			.then((fs) => (flows = fs))
			.catch(() => (failed = true));
	});

	const owned = $derived((flows ?? []).filter((f) => !f.builtin));
	const templates = $derived((flows ?? []).filter((f) => f.builtin));
</script>

{#if failed}
	<input {id} bind:value placeholder={fallbackLabel} class="mono" />
{:else}
	<select {id} bind:value class="mono" disabled={!flows}>
		<option value="">{fallbackLabel}</option>
		{#if owned.length}
			<optgroup label="This project's flows">
				{#each owned as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
			</optgroup>
		{/if}
		{#if templates.length}
			<optgroup label="Built-in templates">
				{#each templates as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
			</optgroup>
		{/if}
	</select>
{/if}
