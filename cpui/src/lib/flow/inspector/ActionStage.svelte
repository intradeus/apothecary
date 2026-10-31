<script lang="ts">
	// An action stage: the registered action it runs, that action's settings
	// (`with:`) and its ports, from the action's signature.
	import type { ActionSpec } from '$lib/domain';
	import Select from '$lib/ui/Select.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/core/format';
	import PortRows from '$lib/flow/PortRows.svelte';
	import { enumParamsWithin, isParamRef, stringListParams } from '$lib/flow/params';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { schemaNamesOf, writtenPathsOf, writtenTypesOf } from '$lib/flow/inspector/inspector';
	import type { DocPort, DocStage, FlowDoc } from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		actions,
		onchange
	}: {
		doc: FlowDoc;
		stage: DocStage;
		actions: ActionSpec[];
		onchange: () => void;
	} = $props();

	// The selected stage's action signature, when it names a registered one.
	const actionSig = $derived(actions.find((a) => a.slug === s.action));

	const writtenPaths = $derived(writtenPathsOf(doc));
	const writtenTypes = $derived(writtenTypesOf(doc));
	const schemaNames = $derived(schemaNamesOf(doc));

	// An action's settings (`with:`): read one, write it back preserving order.
	function settingValue(name: string): string {
		const v = s.with?.[name];
		return typeof v === 'string' ? v : '';
	}
	// A list setting holds items written here, or a list $parameter.
	function setSettingList(name: string, value: string | string[]) {
		const cur = { ...(s.with ?? {}) };
		if (Array.isArray(value) ? value.length : value) cur[name] = value;
		else delete cur[name];
		s.with = Object.keys(cur).length ? cur : undefined;
		onchange();
	}
	function setSetting(name: string, value: string) {
		const cur = { ...(s.with ?? {}) };
		if (value) cur[name] = value;
		else delete cur[name];
		s.with = Object.keys(cur).length ? cur : undefined;
		onchange();
	}
</script>

<div class="field">
	<label for="st-action">{t('flow.insp.action')}</label>
	<Select
		id="st-action"
		value={s.action ?? ''}
		options={[
			{ value: '', label: '—' },
			...actions.map((a) => ({ value: a.slug, label: a.title }))
		]}
		onchange={(v) => {
			s.action = v || undefined;
			// Prefill the new action's ports with their conventional paths.
			const sig = actions.find((a) => a.slug === s.action);
			const ins: Record<string, DocPort> = {};
			for (const d of sig?.inputs ?? [])
				if (d.defaultPath) ins[d.name] = { from: d.defaultPath, type: d.type };
			const outs: Record<string, DocPort> = {};
			for (const d of sig?.outputs ?? [])
				outs[d.name] = { to: d.defaultPath ?? `artifacts/${d.name}`, type: d.type };
			s.input = Object.keys(ins).length ? ins : undefined;
			s.output = Object.keys(outs).length ? outs : undefined;
			s.with = undefined;
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.actionHelp')}</div>
</div>
{#if actionSig}
	{#if actionSig.description}<div class="help">{actionSig.description}</div>{/if}
	{#if actionSig.requires === 'tracker'}
		<div class="help">{t('flow.insp.tracker')}</div>
	{:else if actionSig.requires}
		<div class="help">
			{@html th('flow.insp.needsIntegration', { kind: portLabel(actionSig.requires) })}
		</div>
	{/if}
	{#if actionSig.needs === 'merge_request'}
		<div class="help">{t('flow.insp.needsMR')}</div>
	{/if}
	{#if actionSig.exactlyOne?.length}
		<div class="help">
			{t('flow.insp.exactlyOne', { names: actionSig.exactlyOne.join(' / ') })}
		</div>
	{/if}
	{#each actionSig.settings as d (d.name)}
		{@const cur = settingValue(d.name)}
		{@const fits = d.kind === 'enum' ? enumParamsWithin(doc, d.values ?? []) : []}
		<div class="io-bind card-in field">
			<label for="act-{d.name}"
				><span class="mono">{d.name}</span>
				{#if d.required}<span class="hue-warning">{t('flow.insp.required')}</span>{:else}<span
						class="faint">{t('flow.insp.optional')}</span
					>{/if}</label
			>
			{#if d.description}<div class="help">{d.description}</div>{/if}
			{#if d.kind === 'enum'}
				<Select
					id="act-{d.name}"
					value={cur || d.defaultValue || ''}
					options={[
						...(!d.required && !d.defaultValue ? [{ value: '', label: '—' }] : []),
						...(d.values ?? []).map((v) => ({ value: v, label: v })),
						...fits.map((p) => ({ value: '$' + p, label: '$' + p })),
						...(isParamRef(cur) && !fits.includes(cur.slice(1)) ? [{ value: cur, label: cur }] : [])
					]}
					onchange={(v) => setSetting(d.name, v)}
				/>
				{#if fits.length}<div class="help">{t('flow.insp.enumParamHelp')}</div>{/if}
			{:else if d.kind === 'list'}
				{@const items = s.with?.[d.name]}
				<Select
					id="act-{d.name}"
					value={typeof items === 'string' ? items : ''}
					options={[
						{ value: '', label: t('flow.insp.listWritten') },
						...stringListParams(doc).map((p) => ({ value: '$' + p, label: '$' + p })),
						...(typeof items === 'string' &&
						items &&
						!stringListParams(doc).includes(items.slice(1))
							? [{ value: items, label: items }]
							: [])
					]}
					onchange={(v) => setSettingList(d.name, v || (Array.isArray(items) ? items : []))}
				/>
				{#if typeof items !== 'string'}
					<input
						class="mono"
						aria-label={d.name}
						value={(items ?? []).join(', ')}
						placeholder={t('flow.insp.paramListPlaceholder')}
						onchange={(e) =>
							setSettingList(
								d.name,
								e.currentTarget.value
									.split(',')
									.map((v) => v.trim())
									.filter(Boolean)
							)}
					/>
				{/if}
				<div class="help">{t('flow.insp.listSettingHelp')}</div>
			{:else}
				<input
					id="act-{d.name}"
					list="flow-params"
					value={cur}
					placeholder={t('flow.insp.textPlaceholder')}
					oninput={(e) => setSetting(d.name, e.currentTarget.value.trim())}
				/>
				<div class="help">{t('flow.insp.textInputHelp')}</div>
			{/if}
		</div>
	{/each}
	{#key s.action}
		{#if actionSig.inputs.length}
			<Group caption={t('flow.insp.reads')} />
			<PortRows
				stage={s}
				field="input"
				declared={actionSig.inputs}
				paths={writtenPaths}
				{writtenTypes}
				schemas={schemaNames}
				{onchange}
			/>
		{/if}
		{#if actionSig.outputs.length}
			<Group caption={t('flow.insp.writes')} />
			<PortRows
				stage={s}
				field="output"
				declared={actionSig.outputs}
				schemas={schemaNames}
				{onchange}
			/>
		{/if}
	{/key}
{:else if s.action}
	<div class="help hue-warning">{t('flow.insp.unknownAction', { name: s.action })}</div>
{/if}

<style>
	.io-bind {
		padding: 7px 9px;
	}
	.io-bind label {
		display: flex;
		align-items: center;
		gap: 6px;
	}
</style>
