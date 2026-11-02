<script lang="ts">
	// Flow editor (DESIGN 7.8): header, then the canvas beside a 450px
	// inspector. All document logic lives in flowdoc / FlowEditor / FlowGraph /
	// FlowInspector; this page wires them up.
	import { onMount, untrack } from 'svelte';
	import { beforeNavigate } from '$app/navigation';
	import { api } from '$lib/api';
	import { I } from '$lib/design/icons';
	import AgentsPage from '$lib/flow/AgentsPage.svelte';
	import FlowGraph from '$lib/flow/FlowGraph.svelte';
	import FlowHeader from '$lib/flow/FlowHeader.svelte';
	import FlowInspector from '$lib/flow/FlowInspector.svelte';
	import FlowVersions from '$lib/flow/FlowVersions.svelte';
	import NameFlowDialog from '$lib/flow/NameFlowDialog.svelte';
	import SequencePane from '$lib/flow/SequencePane.svelte';
	import { FlowEditor } from '$lib/flow/flowEditor.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { slide } from '$lib/ui/motion';

	let { projectId, name }: { projectId: string; name: string } = $props();

	const ed = new FlowEditor(
		() => projectId,
		() => name
	);
	const { panels } = ed;

	// The parser's verdict for the YAML tab's lint: its errors only.
	const checkYaml = async (text: string) => (await api.flows.validate(text)).errors ?? [];

	let inspector = $state<ReturnType<typeof FlowInspector> | null>(null);
	let side = $state<HTMLElement | null>(null);
	// A new selection opens its panel from the top, wherever the last one
	// was scrolled to (the flow panel's agent and sequence chips sit low) —
	// but an edge opens its stage on the route it picked, which the
	// inspector scrolls to.
	$effect(() => {
		if (panels.selection?.kind === 'edge') return;
		untrack(() => side?.scrollTo({ top: 0 }));
	});

	onMount(ed.load);

	$effect(() => {
		void panels.paneKey;
		untrack(() => panels.resetSequenceLayout());
	});
	// Opening or closing the pane resizes the flow's canvas: it fits again.
	let wasOpen = false;
	$effect(() => {
		const isOpen = panels.levels.length > 0;
		if (isOpen === wasOpen) return;
		wasOpen = isOpen;
		untrack(() => ed.mainRefit++);
	});

	beforeNavigate(ed.beforeLeave);

	function keydown(e: KeyboardEvent) {
		const mod = e.metaKey || e.ctrlKey;
		if (mod && e.key.toLowerCase() === 's') {
			e.preventDefault();
			void ed.save();
			return;
		}
		const el = e.target as HTMLElement | null;
		if (
			e.key === 'Escape' &&
			panels.levels.length &&
			!el?.closest('input, textarea, select, [role="combobox"]')
		) {
			panels.select(null);
			return;
		}
		if (e.key !== 'Delete' && e.key !== 'Backspace') return;
		if (el?.closest('input, textarea, select, [contenteditable], [role="combobox"]')) return;
		if (ed.tab === 'graph' && panels.selection) {
			e.preventDefault();
			inspector?.deleteSelection();
		}
	}
</script>

<svelte:window onkeydown={keydown} />

<FlowHeader {ed} />

{#if ed.flow?.builtin}
	<div class="strip glass">
		<Callout glyph={I.workshop}>
			{@html th('page.flow.builtinCallout')}
		</Callout>
	</div>
{/if}
{#if ed.notice}
	<div class="strip glass">
		<Callout
			hue={ed.notice.tone === 'ok' ? 'success' : 'attention'}
			glyph={ed.notice.tone === 'ok' ? I.ok : I.info}
			ondismiss={() => (ed.notice = null)}>{ed.notice.text}</Callout
		>
	</div>
{/if}
{#if ed.error}
	<div class="strip glass">
		<ErrorCard
			cause={t('page.flow.wrong')}
			detail={ed.error}
			onretry={ed.flow ? undefined : ed.load}
		/>
	</div>
{/if}

<div class="fe">
	{#if !ed.flow && !ed.error}
		<div class="canvas glass"><Skeleton pane lines={6} /></div>
	{:else if ed.flow}
		<div class="canvas glass" class:yaml={ed.tab === 'yaml'} class:agents={ed.tab === 'agents'}>
			{#if ed.tab === 'agents' && ed.doc}
				<AgentsPage
					doc={ed.doc}
					bind:selected={ed.agentOpen}
					onchange={ed.markChanged}
					onopenstage={(r) => {
						ed.switchTab('graph');
						panels.select({ kind: 'stage', id: r.id, sequence: r.sequence });
					}}
					onopenmcp={(name) => {
						ed.switchTab('graph');
						panels.select({ kind: 'mcp', name });
					}}
				/>
			{:else if ed.tab === 'graph' && ed.doc}
				<div class="split">
					<div class="half">
						<FlowGraph
							doc={ed.doc}
							layout={ed.layout}
							rev={ed.rev}
							onselect={panels.select}
							onmove={ed.onmove}
							onlink={ed.onlink}
							onarrange={ed.autoArrange}
							onmeasured={ed.onmeasured}
							agents={ed.doc?.agents ?? []}
							actions={ed.actions}
							refit={ed.mainRefit}
						/>
						<div class="hint faint">
							{@html th('page.flow.hint')}
						</div>
					</div>
					{#if panels.open && panels.sequenceDoc}
						<section
							class="half seq"
							aria-label={t('page.flow.sequencePane', { sequence: panels.open.sequence.id })}
							in:slide={{ from: 'right', distance: 32 }}
						>
							<SequencePane
								{panels}
								open={panels.open}
								doc={panels.sequenceDoc}
								actions={ed.actions}
							/>
						</section>
					{/if}
				</div>
			{:else}
				<!-- Loaded with the tab: the graph never pays for the editor. -->
				{#await import('$lib/flow/YamlEditor.svelte') then { default: YamlEditor }}
					<YamlEditor
						bind:value={ed.yaml}
						schema={ed.flowSchema}
						check={checkYaml}
						label={t('page.flow.yamlLabel')}
					/>
				{/await}
			{/if}
		</div>

		{#if ed.showVersions}
			<aside class="side glass" in:slide={{ from: 'right', distance: 24 }}>
				<FlowVersions
					flow={ed.flow}
					versions={ed.versions}
					onload={ed.loadVersion}
					onclose={() => (ed.showVersions = false)}
				/>
			</aside>
		{:else if ed.tab === 'graph' && ed.doc}
			<aside class="side glass" bind:this={side} in:slide={{ from: 'right', distance: 24 }}>
				<FlowInspector
					bind:this={inspector}
					doc={ed.doc}
					selection={panels.selection}
					agents={ed.doc?.agents ?? []}
					actions={ed.actions}
					signals={ed.signals}
					harnessSpecs={ed.harnessSpecs}
					onchange={ed.markChanged}
					onselect={panels.replace}
					onopen={panels.openPanel}
					back={panels.trail.length ? panels.trail[panels.trail.length - 1] : undefined}
					onback={panels.goBack}
				/>
			</aside>
		{/if}
	{/if}
</div>

{#if ed.cloning && ed.flow}
	<NameFlowDialog
		title={t('page.flow.cloneTitle', { name: ed.flow.name })}
		initial="{ed.flow.name}-copy"
		action={t('page.flow.cloneAction')}
		taken={[ed.flow.name]}
		onsubmit={ed.clone}
		onclose={() => (ed.cloning = false)}
	>
		<p>
			{ed.dirty ? t('page.flow.cloneHelpDirty') : t('page.flow.cloneHelp')}
		</p>
	</NameFlowDialog>
{/if}

<style>
	.strip {
		margin: var(--shell-gap) var(--shell-gap) 0;
		border-radius: var(--r-btn);
	}
	.fe {
		flex: 1;
		min-height: 0;
		display: flex;
		gap: var(--desk-gap);
		padding: var(--shell-gap);
	}
	.canvas {
		position: relative;
		flex: 1;
		min-width: 0;
		border-radius: var(--r-pane);
		overflow: hidden;
	}
	/* The flow, and — while a sequence stage is selected — the sequence it
	 * runs, side by side. */
	.split {
		display: flex;
		height: 100%;
	}
	.half {
		position: relative;
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.half :global(.graph-wrap) {
		flex: 1;
		min-height: 0;
	}
	.half.seq {
		flex: 0 0 55%;
		border-left: 1px solid var(--glass-line);
	}
	.canvas.yaml,
	.canvas.agents {
		background: var(--glass-bg);
		backdrop-filter: var(--glass-filter);
		-webkit-backdrop-filter: var(--glass-filter);
	}
	.hint {
		position: absolute;
		right: 14px;
		top: 10px;
		font-size: var(--fs-meta);
		pointer-events: none;
	}
	/* Wider than a form's side column: the inspector edits whole agents —
	 * prompts included — and sequences. */
	.side {
		flex: none;
		width: 450px;
		border-radius: var(--r-pane);
		overflow: auto;
	}
	@media (max-width: 1000px) {
		.fe {
			flex-direction: column;
		}
		.canvas {
			min-height: 420px;
		}
		.side {
			width: 100%;
		}
		.split {
			flex-direction: column;
		}
		.half.seq {
			flex-basis: 50%;
			border-left: none;
			border-top: 1px solid var(--glass-line);
		}
	}
</style>
