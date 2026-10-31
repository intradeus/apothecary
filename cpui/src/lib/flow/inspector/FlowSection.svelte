<script lang="ts">
	// The flow's own panel, open when nothing is selected: what it says about
	// itself, and everything it carries, each a way in and a way to add one.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import ParamsEditor from '$lib/flow/ParamsEditor.svelte';
	import AddStageChips from '$lib/flow/inspector/AddStageChips.svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import { addKinds, setOperatesOn } from '$lib/flow/inspector/inspector';
	import {
		MAIN,
		OPERATES_ON,
		addAgent,
		addMCPServer,
		addMainChain,
		addOutcome,
		addSchema,
		addSequence,
		addWatcher,
		appendStage,
		type FlowDoc,
		type StageKind
	} from '$lib/domain/flowdoc';

	let {
		doc,
		onchange,
		onopen
	}: {
		doc: FlowDoc;
		onchange: () => void;
		onopen: (sel: Selection) => void;
	} = $props();

	const mainPhase = $derived(doc.phases?.find((p) => p.kind === 'main'));

	function addChain(kind: 'watcher' | 'outcome') {
		const ph = kind === 'watcher' ? addWatcher(doc) : addOutcome(doc);
		onchange();
		onopen({ kind: 'phase', id: ph.id });
	}

	function newMainChain() {
		addMainChain(doc);
		onchange();
		onopen({ kind: 'phase', id: MAIN });
	}

	function newAgent() {
		const a = addAgent(doc);
		onchange();
		onopen({ kind: 'agent', name: a.name });
	}
	function newSchema() {
		const q = addSchema(doc);
		onchange();
		onopen({ kind: 'schema', name: q.name });
	}
	function newMCP() {
		const m = addMCPServer(doc);
		onchange();
		onopen({ kind: 'mcp', name: m.name });
	}
	function newSequence() {
		const q = addSequence(doc);
		onchange();
		onopen({ kind: 'sequence', id: q.id });
	}

	// A stage is added at the end of a chain, wired in after its last stage;
	// this panel adds to the main chain (or the first chain there is).
	function addStage(type: StageKind) {
		const phase = mainPhase?.id ?? doc.phases?.[0]?.id;
		if (!phase) return;
		const s = appendStage(doc, type, phase);
		onchange();
		onopen({ kind: 'stage', id: s.id });
	}
</script>

<PanelHead>
	{#snippet lead()}
		<span class="display ttl">{t('flow.insp.formula')}</span>
	{/snippet}
	<span class="faint tiny">{t('flow.insp.nothingSelected')}</span>
</PanelHead>
<div class="field">
	<label for="fl-desc">{t('flow.insp.description')}</label>
	<input
		id="fl-desc"
		value={doc.description ?? ''}
		oninput={(e) => {
			doc.description = e.currentTarget.value || undefined;
			onchange();
		}}
	/>
</div>
<div class="field">
	<label for="fl-operates">{t('flow.insp.operatesOn')}</label>
	<Select
		id="fl-operates"
		value={doc.operates_on ?? 'ticket'}
		options={OPERATES_ON.map((r) => ({ value: r, label: t(`flow.insp.operatesOn.${r}`) }))}
		onchange={(v) => {
			setOperatesOn(doc, v);
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.operatesOnHelp')}</div>
</div>
<div class="field">
	<label for="fl-ask">{t('flow.insp.ask')}</label>
	<input
		id="fl-ask"
		list="flow-params"
		value={doc.ask?.harness ?? ''}
		placeholder={t('flow.insp.askPlaceholder')}
		oninput={(e) => {
			const v = e.currentTarget.value.trim();
			doc.ask = v ? { harness: v } : undefined;
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.askHelp')}</div>
</div>
<Caption text={t('flow.insp.params')} />
<div class="help">{t('flow.insp.paramsHelp')}</div>
<ParamsEditor {doc} {onchange} />
<Caption text={t('flow.insp.schemas')} />
<div class="help">{t('flow.insp.schemasHelp')}</div>
<div class="chips">
	{#each doc.schemas ?? [] as q (q.name)}<button
			class="btn-sm mono"
			onclick={() => onopen({ kind: 'schema', name: q.name })}>{q.name}</button
		>{/each}
	<button class="btn-sm" onclick={newSchema}>{I.add} {t('flow.insp.addSchema')}</button>
</div>
<Caption text={t('flow.insp.mcpServers')} />
<div class="help">{t('flow.insp.mcpServersHelp')}</div>
<div class="chips">
	{#each doc.mcp_servers ?? [] as m (m.name)}<button
			class="btn-sm mono"
			onclick={() => onopen({ kind: 'mcp', name: m.name })}>{m.name}</button
		>{/each}
	<button class="btn-sm" onclick={newMCP}>{I.add} {t('flow.insp.addMCP')}</button>
</div>
<Caption text={t('flow.insp.agents')} />
<div class="help">{t('flow.insp.agentsHelp')}</div>
<div class="chips">
	{#each doc.agents ?? [] as a (a.name)}<button
			class="btn-sm mono"
			onclick={() => onopen({ kind: 'agent', name: a.name })}>{a.name}</button
		>{/each}
	<button class="btn-sm" onclick={newAgent}>{I.add} {t('flow.insp.addAgent')}</button>
</div>
<Caption text={t('flow.insp.sequences')} />
<div class="help">{t('flow.insp.sequencesHelp')}</div>
<div class="chips">
	{#each doc.sequences ?? [] as q (q.id)}<button
			class="btn-sm mono"
			onclick={() => onopen({ kind: 'sequence', id: q.id })}>{q.id}</button
		>{/each}
	<button class="btn-sm" onclick={newSequence}>{I.add} {t('flow.insp.addSequence')}</button>
</div>
<Caption text={t('flow.insp.addChain')} />
<div class="chips">
	{#if !mainPhase}
		<button class="btn-sm" onclick={newMainChain}
			><span class="live-dot sm flat hue-brand"></span>{t('flow.insp.addMain')}</button
		>
	{/if}
	<button class="btn-sm" onclick={() => addChain('watcher')}
		><span class="live-dot sm flat hue-accent"></span>{t('flow.insp.addWatcher')}</button
	>
	<button class="btn-sm" onclick={() => addChain('outcome')}
		><span class="live-dot sm flat hue-success"></span>{t('flow.insp.addOutcome')}</button
	>
</div>
<div class="help">
	{t(mainPhase ? 'flow.insp.addChainHelp' : 'flow.insp.addChainHelpNoMain')}
</div>
{#if doc.phases?.length}
	{@const into = mainPhase ?? doc.phases[0]}
	<Caption
		text={into.kind === 'main'
			? t('flow.insp.addStageMain')
			: t('flow.insp.addStageTo', { id: into.id })}
	/>
	<AddStageChips kinds={addKinds(doc, { phase: into.id })} onadd={addStage} />
{/if}
<div class="help">{t('flow.insp.canvasHelp')}</div>

<style>
	.ttl {
		font-size: var(--fs-section);
	}
</style>
