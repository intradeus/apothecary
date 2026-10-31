<script lang="ts">
	import SearchInput from '$lib/ui/SearchInput.svelte';
	// The flow's agents, as a page of their own (beside Graph and YAML): the
	// flow carries their definitions (FLOWLANG §1), and prompts are long —
	// the graph's sidebar is no place to write one. The list on the left, the
	// selected agent's editor on the right; every edit is the document's, so
	// it reaches the YAML like an edit in the graph.
	import AgentEditor from '$lib/flow/AgentEditor.svelte';
	import { runnersOf, type AgentRunner } from '$lib/flow/agents';
	import { addAgent, type FlowDoc } from '$lib/domain/flowdoc';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';

	let {
		doc,
		selected = $bindable<string | undefined>(),
		onchange,
		onopenstage,
		onopenmcp
	}: {
		doc: FlowDoc;
		/** The agent open in the editor, by name. */
		selected?: string;
		onchange: () => void;
		/** A stage running the agent was picked: show it in the graph. */
		onopenstage: (r: AgentRunner) => void;
		/** An MCP server was picked: edit it in the graph's inspector. */
		onopenmcp?: (name: string) => void;
	} = $props();

	let query = $state('');
	const agents = $derived(doc.agents ?? []);
	const shown = $derived(
		agents.filter((a) => {
			const q = query.trim().toLowerCase();
			return (
				!q ||
				a.name.includes(q) ||
				(a.description ?? '').toLowerCase().includes(q) ||
				a.prompt.toLowerCase().includes(q)
			);
		})
	);
	// Opened on the first agent, and back on one when the open one goes.
	const current = $derived(agents.find((a) => a.name === selected) ?? agents[0]);

	function create() {
		const a = addAgent(doc);
		onchange();
		selected = a.name;
	}
	const summary = (a: (typeof agents)[number]) =>
		t('flow.agents.ioSummary', { inputs: a.inputs?.length ?? 0, outputs: a.outputs?.length ?? 0 });
</script>

<div class="agents">
	<nav class="list" aria-label={t('flow.agents.listLabel')}>
		<div class="lh">
			<span class="display lt">{t('flow.agents.title')}</span>
			<span class="spacer"></span>
			<button class="btn-sm" onclick={create}>{I.add} {t('flow.insp.addAgent')}</button>
		</div>
		{#if agents.length > 4}
			<div class="search">
				<SearchInput bind:value={query} width={480} placeholder={t('flow.agents.search')} />
			</div>
		{/if}
		{#each shown as a (a.name)}
			{@const runs = runnersOf(doc, a.name).length}
			<button
				class="item rowbtn"
				class:on={current?.name === a.name}
				aria-current={current?.name === a.name ? 'true' : undefined}
				onclick={() => (selected = a.name)}
			>
				<span class="row nowrap">
					<span class="mono nm truncate grow">{a.name}</span>
					{#if runs}<span class="faint tiny">{t('flow.insp.runByCount', { count: runs })}</span
						>{:else}<span class="hue-warning tiny">{t('flow.agents.unused')}</span>{/if}
				</span>
				{#if a.description}<span class="faint small truncate">{a.description}</span>{/if}
				<span class="tiny muted">
					{[
						summary(a),
						a.read_only && t('flow.insp.readOnly'),
						a.allow_bash && t('flow.insp.allowBash'),
						a.mcp?.length && t('flow.agents.mcpSummary', { names: a.mcp.join(', ') })
					]
						.filter(Boolean)
						.join(' · ')}
				</span>
			</button>
		{:else}
			<div class="help">{agents.length ? t('flow.agents.noMatch') : t('flow.agents.none')}</div>
		{/each}
	</nav>
	<div class="detail">
		{#if current}
			{#key current}
				<AgentEditor
					{doc}
					agent={current}
					{onchange}
					onrenamed={(to) => (selected = to)}
					ondeleted={() => (selected = undefined)}
					{onopenstage}
					{onopenmcp}
				/>
			{/key}
		{:else}
			<div class="empty">
				<p class="faint">{t('flow.agents.emptyHelp')}</p>
				<button class="btn-sm" onclick={create}>{I.add} {t('flow.insp.addAgent')}</button>
			</div>
		{/if}
	</div>
</div>

<style>
	.agents {
		display: grid;
		grid-template-columns: 280px minmax(0, 1fr);
		height: 100%;
		min-height: 0;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 14px 12px;
		border-right: 1px solid var(--line);
		overflow-y: auto;
		min-height: 0;
	}
	.lh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 0 4px 8px;
	}
	.lt {
		font-size: var(--fs-section);
	}
	.search {
		margin-bottom: 6px;
	}
	.item {
		display: grid;
		gap: 2px;
		text-align: left;
		padding: 8px 10px;
		border-radius: var(--r-row);
	}
	.nm {
		font-weight: 600;
	}
	.muted {
		color: var(--text-muted);
	}
	.detail {
		padding: 16px 22px 28px;
		overflow-y: auto;
		min-height: 0;
		min-width: 0;
	}
	.empty {
		display: grid;
		justify-items: start;
		gap: 10px;
		padding-top: 40px;
	}
	@media (max-width: 760px) {
		.agents {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: auto minmax(0, 1fr);
		}
		.list {
			border-right: none;
			border-bottom: 1px solid var(--line);
			max-height: 34vh;
		}
	}
</style>
