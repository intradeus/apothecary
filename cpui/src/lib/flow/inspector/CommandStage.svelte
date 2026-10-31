<script lang="ts">
	// A command stage: a command of the project or a literal script, and the
	// files it reads and writes, under names of its own.
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import PortRows from '$lib/flow/PortRows.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { schemaNamesOf, writtenPathsOf, writtenTypesOf } from '$lib/flow/inspector/inspector';
	import { paramDefault, type DocStage, type FlowDoc } from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		onchange
	}: {
		doc: FlowDoc;
		stage: DocStage;
		onchange: () => void;
	} = $props();

	const writtenPaths = $derived(writtenPathsOf(doc));
	const writtenTypes = $derived(writtenTypesOf(doc));
	const schemaNames = $derived(schemaNamesOf(doc));
</script>

<div class="field">
	<label for="st-runref">run_ref</label>
	<input
		id="st-runref"
		class="mono"
		list="flow-params"
		bind:value={s.run_ref}
		oninput={onchange}
		placeholder={t('flow.insp.runRefPlaceholder')}
	/>
	<div class="help">{t('flow.insp.commandHelp')}</div>
</div>
<div class="field">
	<label for="st-run">{t('flow.insp.literalScript')}</label>
	<textarea
		id="st-run"
		class="mono"
		bind:value={s.run}
		oninput={onchange}
		rows="3"
		placeholder="make test"></textarea>
</div>
<div class="field">
	<label for="st-cmd-timeout">{t('flow.insp.timeout')}</label>
	<input
		id="st-cmd-timeout"
		class="mono"
		list="flow-duration-params"
		bind:value={s.timeout}
		oninput={onchange}
		placeholder={paramDefault(doc, 'timeout') ?? '30m'}
	/>
	<div class="help">{t('flow.insp.commandTimeoutHelp')}</div>
</div>
<Toggle
	checked={!!s.each_repo}
	label={t('flow.insp.eachRepo')}
	title={t('flow.insp.eachRepoTitle')}
	onchange={(v) => {
		s.each_repo = v || undefined;
		onchange();
	}}
/>
<Group caption={t('flow.insp.reads')}>
	<div class="help">{t('flow.insp.readsHelp')}</div>
</Group>
<PortRows
	stage={s}
	field="input"
	paths={writtenPaths}
	{writtenTypes}
	schemas={schemaNames}
	valuePlaceholder="artifacts/thresholds.json"
	{onchange}
/>
<Group caption={t('flow.insp.writes')}>
	<div class="help">{t('flow.insp.writesHelp')}</div>
</Group>
<PortRows
	stage={s}
	field="output"
	schemas={schemaNames}
	valuePlaceholder="artifacts/report.json"
	{onchange}
/>
<div class="help">{t('flow.insp.nameLabelHelp')}</div>
