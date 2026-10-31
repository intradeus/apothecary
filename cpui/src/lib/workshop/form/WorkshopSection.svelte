<script lang="ts">
	// 2 · Workshop: its name, the flow runs default to, and the commands flows
	// run by name (editable key → script rows).
	import type { Flow } from '$lib/domain';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import KeyValueEditor, { type KVRow } from '$lib/ui/KeyValueEditor.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let {
		ownFlows,
		templateFlows,
		noFlows,
		dupKeys,
		name = $bindable(),
		defaultFlow = $bindable(),
		commandRows = $bindable()
	}: {
		ownFlows: Flow[];
		templateFlows: Flow[];
		noFlows: boolean;
		/** Command names used twice: Save refuses these, so the form computes them. */
		dupKeys: Set<string>;
		name: string;
		defaultFlow: string;
		commandRows: KVRow[];
	} = $props();

	const commandCount = $derived(commandRows.filter((r) => r.key.trim()).length);
	const flowOptions = $derived<SelectOption[]>([
		...ownFlows.map((f) => ({ value: f.name, label: f.name, group: t('form.project.ownFlows') })),
		...templateFlows.map((f) => ({
			value: f.name,
			label: f.name,
			group: t('form.project.templates')
		}))
	]);
</script>

<FormPane
	id="workshop"
	step={2}
	title={t('form.project.workshopHead')}
	lead={t('form.project.workshopSub')}
>
	<div class="grid2">
		<div class="field">
			<label for="p-name">{t('form.project.workshopName')}</label>
			<input id="p-name" bind:value={name} required />
		</div>
		<div class="field">
			<label for="p-flow">{t('form.project.defaultFlow')}</label>
			<Select id="p-flow" mono options={flowOptions} bind:value={defaultFlow} required />
			{#if noFlows}
				<div class="error">{t('form.project.noFlows')}</div>
			{:else}
				<div class="help">{t('form.project.flowHelp')}</div>
			{/if}
		</div>
	</div>

	<FormSub>
		<div class="row">
			<h3>{t('form.project.commands')}</h3>
			<span class="count">{commandCount}</span>
		</div>
		<div class="help">{@html th('form.project.commandsHelp')}</div>
		<KeyValueEditor
			bind:rows={commandRows}
			multiline
			keyWidth={150}
			keyPlaceholder={t('form.project.cmdKey')}
			valuePlaceholder="make test"
			addLabel={t('form.project.addCommand')}
			invalid={dupKeys}
		/>
		{#if dupKeys.size}<div class="error">
				{t('form.project.dupCommands', { names: [...dupKeys].join(', ') })}
			</div>{/if}
	</FormSub>
</FormPane>
