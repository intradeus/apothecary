<script lang="ts">
	// A stage's panel, which an edge opens too (on its route): what every
	// stage has — its name, where it is written, its condition, its routes —
	// around what its kind asks for.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import type { ActionSpec, HarnessSpec, SignalSpec } from '$lib/domain';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import AgentStage from '$lib/flow/inspector/AgentStage.svelte';
	import CommandStage from '$lib/flow/inspector/CommandStage.svelte';
	import GateStage from '$lib/flow/inspector/GateStage.svelte';
	import ManualStage from '$lib/flow/inspector/ManualStage.svelte';
	import ActionStage from '$lib/flow/inspector/ActionStage.svelte';
	import ServiceStage from '$lib/flow/inspector/ServiceStage.svelte';
	import SequenceStage from '$lib/flow/inspector/SequenceStage.svelte';
	import WaitStage from '$lib/flow/inspector/WaitStage.svelte';
	import RoutesSection from '$lib/flow/inspector/RoutesSection.svelte';
	import { renamed, runIfOptions } from '$lib/flow/inspector/inspector';
	import {
		ABORT,
		DEBUG,
		DONE,
		EDGE_KINDS,
		END,
		FAIL,
		START,
		chainLinks,
		edgeOf,
		movePhase,
		triggerLabel,
		type DocAgent,
		type DocSequence,
		type DocStage,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		scopeSeq,
		focusRoute,
		agents,
		actions,
		signals,
		harnessSpecs = [],
		onchange,
		onselect,
		onopen,
		ondelete
	}: {
		doc: FlowDoc;
		stage: DocStage;
		/** The sequence the stage is written in, if any: its id, its routes and
		 *  its neighbours are that sequence's own. */
		scopeSeq?: DocSequence;
		/** The route the selected edge is. */
		focusRoute?: string;
		agents: DocAgent[];
		actions: ActionSpec[];
		signals: SignalSpec[];
		harnessSpecs?: HarnessSpec[];
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen: (sel: Selection) => void;
		ondelete: (id: string) => void;
	} = $props();

	const scopeStages = $derived(scopeSeq ? scopeSeq.stages : doc.stages);
	// What a selection inside the open sequence carries along.
	const inSeq = $derived(scopeSeq ? { sequence: scopeSeq.id } : {});
	const only = $derived(scopeSeq?.stages.length === 1);

	function renameStage(oldId: string, newId: string): boolean {
		newId = newId.trim();
		if (
			!newId ||
			newId === oldId ||
			[END, DONE, ABORT, DEBUG, START, FAIL].includes(newId) ||
			!/^[a-z][a-z0-9_-]*$/.test(newId)
		)
			return false;
		// Ids are unique in the chains, and in each sequence on its own.
		if (scopeStages.some((s) => s.id === newId)) return false;
		for (const s of scopeStages) {
			if (s.id === oldId) s.id = newId;
			if (s.on_success === oldId) s.on_success = newId;
			for (const kind of EDGE_KINDS) {
				for (const link of chainLinks(edgeOf(s, kind))) {
					if (link.to === oldId) link.to = newId;
				}
			}
		}
		onchange();
		onselect({ kind: 'stage', id: newId, ...inSeq }, 'rename');
		return true;
	}
</script>

<PanelHead id={s.id}>
	{#snippet lead()}
		<StageChip type={s.type} />
		{#if scopeSeq}<span class="mono faint">{scopeSeq.id}.</span>{/if}
	{/snippet}
	<button
		class="btn-danger btn-sm"
		disabled={only}
		onclick={() => ondelete(s.id)}
		title={only ? t('flow.insp.onlyStage') : t('flow.insp.deleteTitle')}
		>{t('flow.insp.delete')}</button
	>
</PanelHead>
<div class="field">
	<label for="st-id">{t('flow.insp.id')}</label>
	<input
		id="st-id"
		class="mono"
		value={s.id}
		onchange={(e) => renamed(e.currentTarget, s.id, renameStage(s.id, e.currentTarget.value))}
	/>
	<div class="help">{t('flow.insp.renameHelp')}</div>
</div>
{#if scopeSeq}
	{@const q = scopeSeq}
	<div class="field">
		<span class="lbl-in">{t('flow.insp.inSequence')}</span>
		<div class="row">
			<span class="mono">{q.id}</span>
			<span class="faint small truncate grow">{q.description ?? ''}</span>
			<button class="btn-sm" onclick={() => onopen({ kind: 'sequence', id: q.id })}
				>{t('flow.insp.editSequence')}</button
			>
		</div>
		<div class="help">{t('flow.insp.inSequenceHelp')}</div>
	</div>
{:else if doc.phases?.length}
	<div class="field">
		<label for="st-phase">{t('flow.insp.chain')}</label>
		<Select
			id="st-phase"
			value={s.phase}
			options={doc.phases.map((ph) => ({
				value: ph.id,
				label:
					ph.kind === 'main'
						? t('flow.insp.chainMain')
						: t(`flow.insp.chainOption.${ph.kind}`, {
								id: ph.id,
								trigger: triggerLabel(ph.on)
							})
			}))}
			onchange={(v) => {
				movePhase(doc, s.id, v);
				onchange();
			}}
		/>
		<div class="help">{t('flow.insp.phaseHelp')}</div>
	</div>
{/if}

{#if s.type !== 'sequence'}
	<div class="field">
		<label for="st-run-if">{t('flow.insp.runIf')}</label>
		<Select
			id="st-run-if"
			value={s.run_if ?? ''}
			options={runIfOptions(doc, s.run_if)}
			onchange={(v) => {
				s.run_if = v || undefined;
				onchange();
			}}
		/>
		<div class="help">{t('flow.insp.runIfHelp')}</div>
	</div>
{/if}
{#if s.type === 'agent'}
	<AgentStage {doc} stage={s} {agents} {harnessSpecs} {onchange} {onopen} />
{:else if s.type === 'command'}
	<CommandStage {doc} stage={s} {onchange} />
{:else if s.type === 'human_gate'}
	<GateStage {doc} stage={s} {onchange} />
{:else if s.type === 'manual'}
	<ManualStage {doc} stage={s} {onchange} />
{:else if s.type === 'action'}
	<ActionStage {doc} stage={s} {actions} {onchange} />
{:else if s.type === 'service'}
	<ServiceStage {doc} stage={s} {onchange} {onopen} />
{:else if s.type === 'sequence'}
	<SequenceStage {doc} stage={s} {scopeSeq} {onchange} {onopen} />
{:else if s.type === 'wait'}
	<WaitStage stage={s} {signals} {onchange} />
{/if}

<div class="field">
	<label for="st-desc">{t('flow.insp.description')}</label>
	<input
		id="st-desc"
		bind:value={s.description}
		oninput={onchange}
		placeholder={t('flow.insp.descPlaceholder')}
	/>
	<div class="help">{t('flow.insp.descHelp')}</div>
</div>

<RoutesSection {doc} stage={s} {scopeSeq} {focusRoute} {agents} {onchange} />

<style>
	.lbl-in {
		font-size: var(--fs-label, inherit);
	}
</style>
