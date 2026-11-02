<script lang="ts">
	// A manual stage: what the person is to do in the run's workspace, and
	// what flowed in for them — rendered into the brief they get.
	import { t } from '$lib/i18n/index.svelte';
	import PortRows from '$lib/flow/PortRows.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { schemaNamesOf, writtenPathsOf, writtenTypesOf } from '$lib/flow/inspector/inspector';
	import {
		GATE_TYPES,
		flowServices,
		phaseOf,
		sourcesFor,
		type DocStage,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		onchange
	}: {
		doc: FlowDoc;
		stage: DocStage;
		onchange: () => void;
	} = $props();

	const services = $derived(flowServices(doc));
	const writtenPaths = $derived(writtenPathsOf(doc));
	const writtenTypes = $derived(writtenTypesOf(doc));
	const schemaNames = $derived(schemaNamesOf(doc));
</script>

<div class="field">
	<label for="st-question">{t('flow.insp.manualQuestion')}</label>
	<textarea
		id="st-question"
		bind:value={s.question}
		oninput={onchange}
		rows="2"
		placeholder={t('flow.insp.manualQuestionPlaceholder')}></textarea>
	<div class="help">{t('flow.insp.manualQuestionHelp')}</div>
</div>
<Group caption={t('flow.insp.manualShows')}>
	<div class="help">{t('flow.insp.manualShowsHelp')}</div>
</Group>
<PortRows
	stage={s}
	field="input"
	sources={sourcesFor('manual', services, phaseOf(doc, s)?.on.signal === 'ci_failed')}
	paths={writtenPaths}
	{writtenTypes}
	schemas={schemaNames}
	types={GATE_TYPES}
	{onchange}
/>
