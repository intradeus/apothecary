<script lang="ts">
	// The "which workshop" filter of a desk-wide list (runs, tickets): a
	// select over the fleet's workshops, '' meaning all of them. The page owns
	// the value (it mirrors ?project=).
	import { fleet } from '$lib/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';

	let { value, onchange }: { value: string; onchange: (projectId: string) => void } = $props();

	const options = $derived<SelectOption[]>([
		{ value: '', label: t('shell.filter.all') },
		...fleet.projects.map((p) => ({ value: p.id, label: p.name }))
	]);
</script>

<span class="wf">
	<Select label={t('shell.filter.aria')} {options} {value} onchange={(v) => onchange(v)} />
</span>

<style>
	.wf {
		display: inline-block;
		min-width: 160px;
		max-width: 220px;
	}
	@media (max-width: 760px) {
		.wf {
			display: block;
			width: 100%;
			max-width: none;
		}
	}
</style>
