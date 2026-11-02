<script lang="ts">
	// An agent stage: the flow's agent it runs, what it runs on, and how the
	// agent's declared inputs and outputs are wired here.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import type { HarnessSpec } from '$lib/domain';
	import Toggle from '$lib/ui/Toggle.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { enumParamsWithin, isParamRef } from '$lib/flow/params';
	import { t, th } from '$lib/i18n/index.svelte';
	import PortRows from '$lib/flow/PortRows.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { schemaNamesOf, writtenPathsOf, writtenTypesOf } from '$lib/flow/inspector/inspector';
	import {
		AGENT_NAME,
		SOURCE_VERDICT,
		addAgent,
		edgeOf,
		flowServices,
		paramDefault,
		phaseOf,
		sourcesFor,
		verdictOutput,
		type DocAgent,
		type DocStage,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		agents,
		harnessSpecs = [],
		onchange,
		onopen
	}: {
		doc: FlowDoc;
		stage: DocStage;
		agents: DocAgent[];
		harnessSpecs?: HarnessSpec[];
		onchange: () => void;
		onopen: (sel: Selection) => void;
	} = $props();

	// The declared I/O of the selected stage's agent, when known. feedback is
	// excluded from bindable inputs — it is filled by edge feed:, not a stage.
	const agentDecl = $derived(agents.find((a) => a.name === s.agent));
	const declInputs = $derived(agentDecl?.inputs ?? []);
	const declOutputs = $derived(agentDecl?.outputs ?? []);
	const declVerdict = $derived(verdictOutput(agentDecl));

	const services = $derived(flowServices(doc));
	const writtenPaths = $derived(writtenPathsOf(doc));
	const writtenTypes = $derived(writtenTypesOf(doc));
	const schemaNames = $derived(schemaNamesOf(doc));

	// ---- harness options: only the block of the harness it runs on applies --
	// A literal harness shows its own block; a $parameter (or none yet) shows
	// every kind's. A block already written always shows, so it can be cleared.
	const optionKinds = $derived.by(() => {
		const h = s.harness ?? '';
		const shown =
			h && !isParamRef(h) ? harnessSpecs.filter((k) => k.kind === h) : [...harnessSpecs];
		for (const kind of Object.keys(s.harness_options ?? {}))
			if (!shown.some((k) => k.kind === kind)) shown.push({ kind, options: [] });
		return shown;
	});
	function optionValue(kind: string, name: string): string {
		return s.harness_options?.[kind]?.[name] ?? '';
	}
	function setOption(kind: string, name: string, value: string) {
		const all = { ...(s.harness_options ?? {}) };
		const block = { ...(all[kind] ?? {}) };
		if (value) block[name] = value;
		else delete block[name];
		if (Object.keys(block).length) all[kind] = block;
		else delete all[kind];
		s.harness_options = Object.keys(all).length ? all : undefined;
		onchange();
	}

	// ---- agents: the flow carries their definitions (FLOWLANG §1) ----------
	// They are edited in the page's Agents tab: opening one goes there.
	function newAgent(name?: string) {
		const a = addAgent(doc, name);
		onchange();
		onopen({ kind: 'agent', name: a.name });
	}
</script>

<div class="field">
	<label for="st-agent">{t('flow.insp.agent')}</label>
	<input
		id="st-agent"
		list="agent-names"
		bind:value={s.agent}
		oninput={onchange}
		placeholder={agents[0]?.name ?? ''}
	/>
	<datalist id="agent-names">
		{#each agents as a (a.name)}<option value={a.name}></option>{/each}
	</datalist>
	<div class="help">{t('flow.insp.agentHelp')}</div>
	{#if agentDecl}
		{@const name = agentDecl.name}
		<div class="chips">
			<button class="btn-sm" onclick={() => onopen({ kind: 'agent', name })}
				>{t('flow.insp.editAgent', { name })}</button
			>
		</div>
	{:else}
		<div class="chips">
			<button class="btn-sm" onclick={() => newAgent(s.agent)}
				>{s.agent && AGENT_NAME.test(s.agent)
					? t('flow.insp.declareAgent', { name: s.agent })
					: t('flow.insp.newAgent')}</button
			>
		</div>
	{/if}
</div>
<!-- Every agent stage says what it runs on: a kind or a model, or
     the parameter the workshop binds. No silent fallback. -->
<div class="field">
	<label for="st-harness"
		>{t('flow.insp.harness')}
		{#if !s.harness}<span class="hue-warning">{t('flow.insp.required')}</span>{/if}</label
	>
	<input
		id="st-harness"
		list="flow-params"
		bind:value={s.harness}
		oninput={onchange}
		placeholder="$harness"
	/>
	<div class="help">{t('flow.insp.stageHarnessHelp')}</div>
</div>
<div class="field">
	<label for="st-model"
		>{t('flow.insp.model')}
		{#if !s.model}<span class="hue-warning">{t('flow.insp.required')}</span>{/if}</label
	>
	<input
		id="st-model"
		list="flow-params"
		bind:value={s.model}
		oninput={onchange}
		placeholder="$model"
	/>
	<div class="help">{t('flow.insp.stageModelHelp')}</div>
</div>
<div class="field">
	<label for="st-turns">{t('flow.insp.maxTurns')}</label>
	<input
		id="st-turns"
		class="mono"
		list="flow-int-params"
		value={s.max_turns ?? ''}
		placeholder={paramDefault(doc, 'max_turns') ?? '50'}
		oninput={(e) => {
			const v = e.currentTarget.value.trim();
			s.max_turns = v === '' ? undefined : /^\d+$/.test(v) ? Number(v) : v;
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.numberOrParam')}</div>
</div>
<Toggle
	checked={s.reuse_session !== false}
	label={t('flow.insp.reuseSession')}
	title={t('flow.insp.reuseSessionTitle')}
	onchange={(v) => {
		s.reuse_session = v ? undefined : false;
		onchange();
	}}
/>
<div class="field">
	<label for="st-timeout">{t('flow.insp.timeout')}</label>
	<input
		id="st-timeout"
		class="mono"
		list="flow-duration-params"
		bind:value={s.timeout}
		oninput={onchange}
		placeholder={paramDefault(doc, 'timeout') ?? '30m'}
	/>
	<div class="help">{t('flow.insp.durationOrParam')}</div>
</div>
{#if optionKinds.length > 0}
	<Group caption={t('flow.insp.harnessOptions')}>
		<div class="help">{t('flow.insp.harnessOptionsHelp')}</div>
	</Group>
	{#each optionKinds as h (h.kind)}
		<div class="mono faint">{h.kind}</div>
		{#each h.options as o (o.name)}
			{@const cur = optionValue(h.kind, o.name)}
			<div class="io-bind card-in field">
				<label for="ho-{h.kind}-{o.name}"><span class="mono">{o.name}</span></label>
				{#if o.kind === 'enum'}
					{@const fits = enumParamsWithin(doc, o.values ?? [])}
					<Select
						id="ho-{h.kind}-{o.name}"
						value={cur}
						options={[
							{ value: '', label: '—' },
							...(o.values ?? []).map((v) => ({ value: v, label: v })),
							...fits.map((p) => ({ value: '$' + p, label: '$' + p })),
							...(isParamRef(cur) && !fits.includes(cur.slice(1))
								? [{ value: cur, label: cur }]
								: [])
						]}
						onchange={(v) => setOption(h.kind, o.name, v)}
					/>
				{:else}
					<input
						id="ho-{h.kind}-{o.name}"
						list="flow-params"
						value={cur}
						oninput={(e) => setOption(h.kind, o.name, e.currentTarget.value.trim())}
						placeholder="provider/model or $param"
					/>
				{/if}
				{#if o.description}<div class="help">{o.description}</div>{/if}
			</div>
		{/each}
		{#each Object.keys(s.harness_options?.[h.kind] ?? {}).filter((n) => !h.options.some((o) => o.name === n)) as n (n)}
			<div class="io-bind card-in field">
				<label for="ho-{h.kind}-{n}"><span class="mono">{n}</span></label>
				<input
					id="ho-{h.kind}-{n}"
					value={optionValue(h.kind, n)}
					oninput={(e) => setOption(h.kind, n, e.currentTarget.value.trim())}
				/>
			</div>
		{/each}
	{/each}
{/if}
{#if declInputs.length > 0}
	<Group caption={t('flow.insp.inputs')}>
		<div class="help">{t('flow.insp.inputsHelp')}</div>
	</Group>
	{#key s.agent}
		<PortRows
			stage={s}
			field="input"
			declared={declInputs}
			sources={sourcesFor('agent', services, phaseOf(doc, s)?.on.signal === 'ci_failed')}
			paths={writtenPaths}
			{writtenTypes}
			schemas={schemaNames}
			{onchange}
		/>
	{/key}
{:else if s.agent}
	<div class="help" class:hue-warning={!agentDecl}>
		{agentDecl
			? t('flow.insp.noInputs', { name: s.agent })
			: t('flow.insp.unknownAgent', { name: s.agent })}
	</div>
{/if}
{#if declOutputs.length > 0}
	<Group caption={t('flow.insp.outputs')}>
		<div class="help">{t('flow.insp.outputsHelp')}</div>
	</Group>
	{#key s.agent}
		<PortRows stage={s} field="output" declared={declOutputs} schemas={schemaNames} {onchange} />
	{/key}
{/if}
{#if declVerdict}
	<div class="help">
		{@html th('flow.insp.verdictHelp', { sentinel: SOURCE_VERDICT })}
	</div>
{:else if s.agent && agentDecl && edgeOf(s, 'on_reject')}
	<div class="help hue-warning">{@html th('flow.insp.neverFires', { agent: s.agent })}</div>
{/if}
