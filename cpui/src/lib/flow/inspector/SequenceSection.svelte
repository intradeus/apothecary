<script lang="ts">
	// A sequence's panel: a chain written once, its stages in order, and the
	// stages that run it.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { STAGE_HUE } from '$lib/design/hues';
	import AddStageChips from '$lib/flow/inspector/AddStageChips.svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import SeqRow from '$lib/flow/inspector/SeqRow.svelte';
	import { addKinds, renamed, runners } from '$lib/flow/inspector/inspector';
	import {
		MAIN,
		appendSequenceStage,
		moveSequenceStage,
		renameSequence,
		type DocSequence,
		type FlowDoc,
		type StageKind
	} from '$lib/domain/flowdoc';

	let {
		doc,
		sequence: q,
		onchange,
		onselect,
		onopen,
		ondelete
	}: {
		doc: FlowDoc;
		sequence: DocSequence;
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen: (sel: Selection) => void;
		ondelete: (q: DocSequence) => void;
	} = $props();

	const users = $derived(runners(doc, (x) => x.type === 'sequence' && x.sequence === q.id));

	function renameSequenceTo(from: string, to: string): boolean {
		to = to.trim();
		if (
			!to ||
			to === from ||
			to === MAIN ||
			!/^[a-z][a-z0-9_-]*$/.test(to) ||
			doc.sequences?.some((q) => q.id === to)
		)
			return false;
		renameSequence(doc, from, to);
		onchange();
		onselect({ kind: 'sequence', id: to }, 'rename');
		return true;
	}
	// A stage is added at the end of the sequence, wired in after its last
	// stage.
	function addToSequence(type: StageKind) {
		const s = appendSequenceStage(doc, type, q.id);
		if (!s) return;
		onchange();
		onopen({ kind: 'stage', id: s.id, sequence: q.id });
	}
</script>

<PanelHead id={q.id}>
	{#snippet lead()}
		<span class="pill sm flat sq mono tint-{STAGE_HUE.sequence}"
			>{t('flow.insp.kind.sequence')}</span
		>
	{/snippet}
	<span class="faint tiny">{t('flow.insp.phaseStages', { count: q.stages.length })}</span>
	<button
		class="btn-danger btn-sm"
		disabled={users.length > 0}
		onclick={() => ondelete(q)}
		title={users.length ? t('flow.insp.sequenceInUse') : t('flow.insp.deleteSequence')}
		>{t('flow.insp.delete')}</button
	>
</PanelHead>
<div class="help">{t('flow.insp.sequencePanelHelp')}</div>
<div class="field">
	<label for="sq-id">{t('flow.insp.id')}</label>
	<input
		id="sq-id"
		class="mono"
		value={q.id}
		onchange={(e) => renamed(e.currentTarget, q.id, renameSequenceTo(q.id, e.currentTarget.value))}
	/>
	<div class="help">{t('flow.insp.sequenceRenameHelp')}</div>
</div>
<div class="field">
	<label for="sq-desc">{t('flow.insp.description')}</label>
	<input
		id="sq-desc"
		value={q.description ?? ''}
		oninput={(e) => {
			q.description = e.currentTarget.value || undefined;
			onchange();
		}}
	/>
</div>
<Group caption={t('flow.insp.sequenceStages')}>
	{#each q.stages as x, i (x.id)}
		<div class="seq-line">
			<SeqRow
				stage={x}
				label={x.id}
				onclick={() => onopen({ kind: 'stage', id: x.id, sequence: q.id })}
			/>
			<button
				class="btn-icon"
				disabled={i === 0}
				title={t('flow.insp.moveUp')}
				aria-label={t('flow.insp.moveUp')}
				onclick={() => {
					moveSequenceStage(doc, q.id, x.id, -1);
					onchange();
				}}>↑</button
			>
			<button
				class="btn-icon"
				disabled={i === q.stages.length - 1}
				title={t('flow.insp.moveDown')}
				aria-label={t('flow.insp.moveDown')}
				onclick={() => {
					moveSequenceStage(doc, q.id, x.id, 1);
					onchange();
				}}>↓</button
			>
		</div>
	{/each}
	<div class="help">{t('flow.insp.sequenceOrderHelp')}</div>
</Group>
<Caption text={t('flow.insp.addToSequence')} />
<AddStageChips kinds={addKinds(doc, { sequence: q.id })} onadd={addToSequence} />
<Group caption={t('flow.insp.runBy')}>
	{#if users.length}
		<div class="chips">
			{#each users as u (u.label)}<button class="btn-sm mono" onclick={() => onopen(u.sel)}
					>{u.label}</button
				>{/each}
		</div>
	{:else}
		<div class="help">{t('flow.insp.sequenceUnused')}</div>
	{/if}
</Group>

<style>
	.seq-line {
		display: flex;
		align-items: center;
		gap: 2px;
	}
</style>
