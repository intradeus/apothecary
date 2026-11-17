<script lang="ts">
	// The Finished tab's two filters, beside the tab inside a `.filters` row:
	// how the run ended, and whether it is archived. The list owns the value
	// (the runs page mirrors it in the URL).
	import { stateLabel } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import type { ArchivedFilter, FinishedFilter, OutcomeFilter } from './filter';

	let { value, onchange }: { value: FinishedFilter; onchange: (f: FinishedFilter) => void } =
		$props();

	const outcomes = $derived<SelectOption<OutcomeFilter>[]>([
		{ value: 'any', label: t('run.filter.anyOutcome') },
		{ value: 'completed', label: stateLabel.completed },
		{ value: 'abandoned', label: stateLabel.abandoned },
		{ value: 'failed', label: stateLabel.failed }
	]);
	const archived = $derived<SelectOption<ArchivedFilter>[]>([
		{ value: 'any', label: t('run.filter.anyArchived') },
		{ value: 'no', label: t('run.filter.notArchived') },
		{ value: 'yes', label: t('run.filter.onlyArchived') }
	]);
</script>

<Select
	inline
	label={t('run.filter.outcome')}
	options={outcomes}
	value={value.outcome}
	onchange={(outcome) => onchange({ ...value, outcome })}
/>
<Select
	inline
	label={t('run.filter.archived')}
	options={archived}
	value={value.archived}
	onchange={(a) => onchange({ ...value, archived: a })}
/>
