<script lang="ts">
	// A sequence stage: the sequence it runs — or the main chain again — and
	// that sequence's stages, as they will run.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import SeqRow from '$lib/flow/inspector/SeqRow.svelte';
	import {
		MAIN,
		hasMain,
		type DocSequence,
		type DocStage,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		scopeSeq,
		onchange,
		onopen
	}: {
		doc: FlowDoc;
		stage: DocStage;
		/** The sequence the stage is written in, if any. */
		scopeSeq?: DocSequence;
		onchange: () => void;
		onopen: (sel: Selection) => void;
	} = $props();

	// What a sequence stage may run: the flow's sequences, and the main chain
	// for the chains that are not it — never the sequence it is written in.
	const options = $derived([
		...(hasMain(doc) && s.phase !== MAIN ? [MAIN] : []),
		...(doc.sequences ?? []).map((q) => q.id).filter((id) => id !== scopeSeq?.id)
	]);
	const sequenceSel = $derived(doc.sequences?.find((q) => q.id === s.sequence));
</script>

<div class="field">
	<label for="st-sequence">{t('flow.insp.sequence')}</label>
	<Select
		id="st-sequence"
		value={s.sequence ?? ''}
		options={[
			...options.map((id) => ({
				value: id,
				label: id === MAIN ? t('flow.insp.runsMain') : id
			})),
			...(s.sequence && !options.includes(s.sequence)
				? [{ value: s.sequence, label: s.sequence }]
				: [])
		]}
		onchange={(v) => {
			s.sequence = v;
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.sequenceHelp')}</div>
	{#if sequenceSel}
		{@const id = sequenceSel.id}
		<div class="chips">
			<button class="btn-sm" onclick={() => onopen({ kind: 'sequence', id })}
				>{t('flow.insp.editSequence')}</button
			>
		</div>
	{/if}
</div>
{#if sequenceSel}
	{#if sequenceSel.description}<div class="help">{sequenceSel.description}</div>{/if}
	<Group caption={t('flow.insp.sequenceStages')}>
		{#each sequenceSel.stages as q (q.id)}
			<SeqRow stage={q} label="{s.id}.{q.id}" />
		{/each}
	</Group>
	<div class="help">{t('flow.insp.sequenceEditHelp')}</div>
{/if}
