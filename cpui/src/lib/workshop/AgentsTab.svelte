<script lang="ts">
	// Workshop agents tab (DESIGN 7.7). There is no agent library on the
	// engine yet: agents are declared inside each flow document (FLOWLANG §1).
	// This lists them read-only, per flow, with what the flow runs them on.
	import { parse } from 'yaml';
	import type { Flow, Project } from '$lib/domain';
	import { to } from '$lib/guild';
	import { lore } from '$lib/lore';
	import { normalizeDoc, type DocAgent, type FlowDoc } from '$lib/flowdoc';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';

	let {
		project,
		flows,
		error,
		onretry
	}: { project: Project; flows: Flow[] | null; error: string; onretry: () => void } = $props();

	interface AgentUse {
		agent: DocAgent;
		stages: { id: string; harness: string; model: string }[];
	}
	interface FlowAgents {
		flow: Flow;
		doc: FlowDoc | null;
		parseError?: string;
		agents: AgentUse[];
	}

	// The workshop's own flows, plus the template it runs on by default if it
	// has not adopted one: those are the agents its runs actually meet.
	const relevant = $derived(
		(flows ?? []).filter((f) => !f.builtin || f.name === project.defaultFlow)
	);

	const parsed = $derived<FlowAgents[]>(
		relevant.map((flow) => {
			try {
				const doc = normalizeDoc(parse(flow.yaml));
				const agents = (doc.agents ?? []).map((agent) => ({
					agent,
					stages: doc.stages
						.filter((s) => s.type === 'agent' && s.agent === agent.name)
						.map((s) => ({
							id: s.id,
							harness: s.harness || doc.defaults?.harness || '—',
							model: s.model || doc.defaults?.model || '—'
						}))
				}));
				return { flow, doc, agents };
			} catch (e) {
				return {
					flow,
					doc: null,
					agents: [],
					parseError: e instanceof Error ? e.message : String(e)
				};
			}
		})
	);
	const total = $derived(parsed.reduce((n, p) => n + p.agents.length, 0));
	let open = $state<Record<string, boolean>>({});
</script>

<div class="note lg">
	<span class="ic">ⓘ</span>
	<span>
		Agents live inside their flow’s document, so each flow carries its own. They are read-only here
		for now — edit them in the flow editor’s YAML view.
	</span>
</div>

{#if error && !flows}
	<ErrorCard cause="Couldn’t load flows" detail={error} {onretry} />
{:else if !flows}
	<div class="c pane"><div class="pane-body"><Skeleton lines={5} /></div></div>
{:else if !relevant.length}
	<EmptyState
		title="No agents yet"
		text="Agents arrive with this {lore('workshop').toLowerCase()}’s first flow."
	>
		<a class="btn btn-primary" href={to.workshop(project.id, 'flows')}>Flows</a>
	</EmptyState>
{:else}
	{#if total === 0}
		<EmptyState
			compact
			title="No agents declared"
			text="These flows run commands, gates and actions only."
		/>
	{/if}
	{#each parsed as p (p.flow.name)}
		<section class="c pane">
			<div class="pane-head">
				<h2>{p.flow.name}</h2>
				{#if p.flow.builtin}<span class="pill t-mut">built-in</span>{/if}
				{#if p.flow.name === project.defaultFlow}<span class="pill t-brand">default</span>{/if}
				<span class="mono fnt">@{p.flow.hash.slice(0, 8)}</span>
				<span class="count">{p.agents.length}</span>
				<span class="spacer"></span>
				<a class="btn btn-sm" href={to.flow(project.id, p.flow.name)}>Edit in flow editor →</a>
			</div>
			{#if p.parseError}
				<div class="pane-body">
					<ErrorCard cause="This flow’s YAML didn’t parse here" detail={p.parseError} />
				</div>
			{:else if !p.agents.length}
				<div class="pane-body fnt small">No agents in this flow.</div>
			{:else}
				<div class="agents">
					{#each p.agents as a (a.agent.name)}
						{@const key = `${p.flow.name}/${a.agent.name}`}
						<article class="ag card-in">
							<div class="ah">
								<StageChip type="agent" />
								<span class="mono an">{a.agent.name}</span>
								<span class="spacer"></span>
								{#if a.agent.read_only}<span class="pill t-tea" title="May not edit files"
										>read-only</span
									>{:else}<span class="pill t-amb" title="May edit files">writes</span>{/if}
								{#if a.agent.allow_bash}<span class="pill t-grn" title="May run shell commands"
										>bash</span
									>{:else}<span class="pill t-mut" title="No shell">no bash</span>{/if}
							</div>
							{#if a.agent.description}<div class="desc">{a.agent.description}</div>{/if}
							<div class="kv">
								<span class="k">runs on</span>
								<span class="v">
									{#each a.stages as s (s.id)}
										<span class="use"
											><span class="mono">{s.id}</span>
											<span class="fnt">·</span>
											<span class="mono mut">{s.harness}</span>
											<span class="fnt">·</span>
											<span class="mono mut ell">{s.model}</span></span
										>
									{:else}
										<span class="fnt">declared, not used by any stage</span>
									{/each}
								</span>
								<span class="k">inputs</span>
								<span class="v io">
									{#each a.agent.inputs ?? [] as i (i.name)}
										<span class="port" title={i.description}
											><span class="pd in" class:opt={i.optional}></span><span class="mono"
												>{i.name}</span
											>{#if i.optional}<span class="fnt"> optional</span>{/if}</span
										>
									{:else}<span class="fnt">the ticket only</span>{/each}
								</span>
								<span class="k">outputs</span>
								<span class="v io">
									{#each a.agent.outputs ?? [] as o (o.name)}
										<span class="port" title={o.description}
											><span class="pd out" class:verdict={o.type === 'verdict'}></span><span
												class="mono">{o.name}</span
											>
											<span class="mono fnt">{o.path}</span>
											{#if o.type === 'verdict'}<span class="pill t-vio vd">verdict</span
												>{/if}</span
										>
									{:else}<span class="fnt">none</span>{/each}
								</span>
							</div>
							{#if a.agent.prompt}
								<details bind:open={open[key]}>
									<summary class="ps"><span class="caretglyph">▸</span> Prompt</summary>
									<pre class="log prompt">{a.agent.prompt}</pre>
								</details>
							{/if}
						</article>
					{/each}
				</div>
			{/if}
		</section>
	{/each}
{/if}

<style>
	.note {
		border-radius: 14px;
		padding: 9px 14px;
		display: flex;
		align-items: baseline;
		gap: 10px;
		font-size: 12.5px;
	}
	.note .ic {
		color: var(--az);
	}
	.pane-head h2 {
		font-family: var(--mono);
		font-style: normal;
		font-size: 14px;
		font-weight: 600;
	}
	.agents {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
		gap: 10px;
		padding: 12px;
	}
	.ag {
		padding: 10px 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.ah {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.an {
		font-size: 13px;
		font-weight: 600;
	}
	.desc {
		font-size: 12px;
		color: var(--mut);
	}
	.kv {
		display: grid;
		grid-template-columns: 64px minmax(0, 1fr);
		gap: 5px 10px;
		font-size: 11.5px;
	}
	.k {
		color: var(--fnt);
	}
	.v {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.v.io {
		flex-direction: row;
		flex-wrap: wrap;
		gap: 4px 12px;
	}
	.use {
		display: flex;
		gap: 5px;
		min-width: 0;
	}
	.use .mono {
		font-size: 11px;
	}
	.port {
		display: inline-flex;
		align-items: center;
		gap: 5px;
	}
	.port .mono {
		font-size: 11px;
	}
	.pd {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--grn);
	}
	.pd.in.opt {
		background: transparent;
		border: 1.5px solid var(--grn);
	}
	.pd.out {
		background: var(--az);
	}
	.pd.out.verdict {
		background: var(--vio);
	}
	.vd {
		font-size: 9.5px;
		padding: 0 6px;
	}
	.ps {
		font-size: 11.5px;
		color: var(--mut);
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.caretglyph {
		font-size: 9px;
	}
	.prompt {
		margin-top: 6px;
		max-height: 260px;
		font-size: 11px;
	}
	@media (max-width: 720px) {
		.agents {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
