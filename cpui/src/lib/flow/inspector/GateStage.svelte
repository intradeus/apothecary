<script lang="ts">
	// A human gate: the question put to a person, and what they are shown to
	// answer it.
	import { t, th } from '$lib/i18n/index.svelte';
	import { CURATABLE_TYPES } from '$lib/core/ping';
	import PortRows from '$lib/flow/PortRows.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { schemaNamesOf, writtenPathsOf, writtenTypesOf } from '$lib/flow/inspector/inspector';
	import {
		GATE_TYPES,
		flowServices,
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
	<label for="st-question">{t('flow.insp.question')}</label>
	<textarea
		id="st-question"
		bind:value={s.question}
		oninput={onchange}
		rows="2"
		placeholder={t('flow.insp.questionPlaceholder')}></textarea>
	<div class="help">{t('flow.insp.questionHelp')}</div>
</div>
<Group caption={t('flow.insp.shows')}>
	<div class="help">{@html th('flow.insp.showsHelp')}</div>
</Group>
<PortRows
	stage={s}
	field="input"
	sources={sourcesFor('human_gate', services)}
	paths={writtenPaths}
	{writtenTypes}
	schemas={schemaNames}
	types={GATE_TYPES}
	links={1}
	{onchange}
/>
{#if Object.values(s.input ?? {}).some((p) => CURATABLE_TYPES.includes(p.type ?? ''))}
	<div class="help">{t('flow.insp.curateHelp')}</div>
{/if}
