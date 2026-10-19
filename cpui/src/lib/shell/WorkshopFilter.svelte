<script lang="ts">
	// The "which workshop" filter of a guild-wide list (runs, tickets): a
	// select over the fleet's workshops, '' meaning all of them. The page owns
	// the value (it mirrors ?project=).
	import { fleet } from '$lib/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let { value, onchange }: { value: string; onchange: (projectId: string) => void } = $props();
</script>

<select
	class="wf"
	aria-label={t('shell.filter.aria')}
	{value}
	onchange={(e) => onchange((e.target as HTMLSelectElement).value)}
>
	<option value="">{t('shell.filter.all')}</option>
	{#each fleet.projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
</select>

<style>
	.wf {
		width: auto;
		min-width: 160px;
		max-width: 220px;
	}
	@media (max-width: 760px) {
		.wf {
			width: 100%;
			max-width: none;
		}
	}
</style>
