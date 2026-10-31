<script lang="ts">
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { ms } from '$lib/ui/motion';
	import { fade } from 'svelte/transition';
	import { durationParams, intParams, stringParams } from '$lib/flow/params';
	import StageSection from '$lib/flow/inspector/StageSection.svelte';
	import PhaseSection from '$lib/flow/inspector/PhaseSection.svelte';
	import SequenceSection from '$lib/flow/inspector/SequenceSection.svelte';
	import SchemaSection from '$lib/flow/inspector/SchemaSection.svelte';
	import McpSection from '$lib/flow/inspector/McpSection.svelte';
	import FlowSection from '$lib/flow/inspector/FlowSection.svelte';
	import { MCPDraft, SchemaDraft } from '$lib/flow/inspector/drafts.svelte';
	import {
		backLabelOf,
		confirmDeleteMCP,
		confirmDeletePhase,
		confirmDeleteSchema,
		confirmDeleteSequence,
		confirmDeleteStage,
		dropStage,
		viewKeyOf
	} from '$lib/flow/inspector/inspector';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		removeMCPServer,
		removePhase,
		removeSchema,
		removeSequence,
		setEdge,
		type DocAgent,
		type DocPhase,
		type DocSequence,
		type EdgeKind,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	// Flows are portable, project-independent data: this editor takes NOTHING
	// from any project. Agent / harness / model / run_ref are free text and are
	// validated against the project only at run creation (preflight). `agents`
	// is an OPTIONAL hint list (built-ins ∪ a project's authored agents) used
	// only to render binding fields from declared I/O — never a constraint.
	//
	// Moving between panels comes in two kinds: `onselect` puts another
	// selection in the current one's place (a rename, a delete); `onopen`
	// goes from this panel into another — a stage's agent, a sequence's
	// stage — and the page remembers where from: `back` is that selection
	// (null: the flow panel), offered at the top of every panel.
	//
	// This file picks the panel for the selection and owns what outlives one:
	// deleting (⌫ / Del on the canvas reaches it here too) and the drafts.
	let {
		doc,
		selection,
		onchange,
		onselect,
		onopen = (sel) => onselect(sel),
		back,
		onback,
		agents = [],
		actions = [],
		signals = []
	}: {
		doc: FlowDoc;
		selection: Selection;
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen?: (sel: Selection) => void;
		back?: Selection;
		onback?: () => void;
		agents?: DocAgent[];
		actions?: ActionSpec[];
		signals?: SignalSpec[];
	} = $props();

	// Where a delete leaves the inspector: back where it came from, or the
	// panel given.
	const leave = (fallback: Selection) =>
		back !== undefined && onback ? onback() : onselect(fallback);

	// A stage or an edge inside a sequence is edited in its sequence: its id,
	// its routes and its neighbours are the sequence's own.
	const scopeSeq = $derived(
		(selection?.kind === 'stage' || selection?.kind === 'edge') && selection.sequence
			? doc.sequences?.find((q) => q.id === selection.sequence)
			: undefined
	);
	const scopeStages = $derived(scopeSeq ? scopeSeq.stages : doc.stages);
	// What a selection inside the open sequence carries along.
	const inSeq = $derived(scopeSeq ? { sequence: scopeSeq.id } : {});

	// An edge is edited in its stage's routes: selecting one shows the stage
	// with that route picked out.
	const stage = $derived(
		selection?.kind === 'stage'
			? scopeStages.find((s) => s.id === selection.id)
			: selection?.kind === 'edge'
				? scopeStages.find((s) => s.id === selection.stage)
				: undefined
	);
	const focusRoute = $derived(selection?.kind === 'edge' ? selection.edge : undefined);
	const phaseSel = $derived(
		selection?.kind === 'phase' ? doc.phases?.find((p) => p.id === selection.id) : undefined
	);
	const seqSel = $derived(
		selection?.kind === 'sequence' ? doc.sequences?.find((q) => q.id === selection.id) : undefined
	);
	const schemaSel = $derived(
		selection?.kind === 'schema' ? doc.schemas?.find((q) => q.name === selection.name) : undefined
	);
	const mcpSel = $derived(
		selection?.kind === 'mcp' ? doc.mcp_servers?.find((m) => m.name === selection.name) : undefined
	);

	const schemaDraft = new SchemaDraft(
		() => schemaSel,
		() => onchange()
	);
	const mcpDraft = new MCPDraft(
		() => mcpSel,
		() => onchange()
	);

	async function deleteStage(id: string) {
		const seqId = scopeSeq?.id;
		if (!(await confirmDeleteStage(scopeStages, id))) return;
		// A save may have replaced the document while the dialog was open.
		const seq = doc.sequences?.find((q) => q.id === seqId);
		if (seqId && !seq) return;
		dropStage(doc, seq, id);
		onchange();
		leave(seq ? { kind: 'sequence', id: seq.id } : null);
	}
	async function deletePhase(ph: DocPhase) {
		if (!(await confirmDeletePhase(doc, ph))) return;
		removePhase(doc, ph.id);
		onchange();
		leave(null);
	}
	async function deleteSequence(q: DocSequence) {
		if (!(await confirmDeleteSequence(doc, q))) return;
		removeSequence(doc, q.id);
		onchange();
		leave(null);
	}
	async function deleteSchema(name: string) {
		if (!(await confirmDeleteSchema(doc, name))) return;
		removeSchema(doc, name);
		onchange();
		leave(null);
	}
	async function deleteMCP(name: string) {
		if (!(await confirmDeleteMCP(doc, name))) return;
		removeMCPServer(doc, name);
		onchange();
		leave(null);
	}

	/** Delete what is selected (⌫ / Del on the canvas): a stage, a chain, a
	 *  sequence, a schema or an MCP server asks first; an edge goes at once
	 *  (re-adding it is one click). */
	export function deleteSelection() {
		if (selection?.kind === 'stage') {
			if (scopeSeq?.stages.length !== 1) void deleteStage(selection.id);
		} else if (selection?.kind === 'edge') {
			const s = scopeStages.find((x) => x.id === selection.stage);
			if (!s) return;
			setEdge(s, selection.edge as EdgeKind, undefined);
			onchange();
			onselect({ kind: 'stage', id: s.id, ...inSeq });
		} else if (phaseSel && phaseSel.kind !== 'main') void deletePhase(phaseSel);
		else if (seqSel) void deleteSequence(seqSel);
		else if (mcpSel) void deleteMCP(mcpSel.name);
		else if (schemaSel) void deleteSchema(schemaSel.name);
	}

	const viewKey = $derived(viewKeyOf(selection));
	const xf = () => ({ duration: ms('--dur-fade', 160) });
	const backLabel = $derived(backLabelOf(doc, back));
</script>

<div class="insp form-dense">
	{#key viewKey}
		<div class="view-x" in:fade={xf()} out:fade={xf()}>
			{#if back !== undefined && onback}
				<button
					class="btn-sm btn-ghost back"
					title={t('flow.insp.back', { what: backLabel })}
					onclick={onback}>{I.back} {backLabel}</button
				>
			{/if}
			{#if stage}
				<StageSection
					{doc}
					{stage}
					{scopeSeq}
					{focusRoute}
					{agents}
					{actions}
					{signals}
					{onchange}
					{onselect}
					{onopen}
					ondelete={deleteStage}
				/>
			{:else if phaseSel}
				<PhaseSection
					{doc}
					phase={phaseSel}
					{signals}
					{onchange}
					{onselect}
					{onopen}
					ondelete={deletePhase}
				/>
			{:else if seqSel}
				<SequenceSection
					{doc}
					sequence={seqSel}
					{onchange}
					{onselect}
					{onopen}
					ondelete={deleteSequence}
				/>
			{:else if schemaSel}
				<SchemaSection
					{doc}
					schema={schemaSel}
					draft={schemaDraft}
					{onchange}
					{onselect}
					{onopen}
					ondelete={deleteSchema}
				/>
			{:else if mcpSel}
				<McpSection
					{doc}
					server={mcpSel}
					draft={mcpDraft}
					{onchange}
					{onselect}
					{onopen}
					ondelete={deleteMCP}
				/>
			{:else}
				<FlowSection {doc} {onchange} {onopen} />
			{/if}
		</div>
	{/key}
	<!-- Every panel's parameter fields suggest from these. -->
	<datalist id="flow-params">
		{#each stringParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
	</datalist>
	<datalist id="flow-int-params">
		{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
	</datalist>
	<datalist id="flow-duration-params">
		{#each durationParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
	</datalist>
</div>

<style>
	/* The inspector sits on window glass, given by the page (DESIGN 7.8). */
	.insp {
		display: grid;
		padding: 12px 14px 16px;
		font-size: var(--fs-ui);
	}
	/* The way back to the panel this one was opened from. */
	.back {
		align-self: flex-start;
		margin: -4px 0 -2px -6px;
		color: var(--text-muted);
	}
	/* Outgoing and incoming views share the cell while they cross-fade. */
	.view-x {
		grid-area: 1 / 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
</style>
