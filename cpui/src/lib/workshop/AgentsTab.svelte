<script lang="ts">
	// Workshop agents tab (DESIGN 7.7). There is no agent library on the
	// engine yet: agents are declared inside each flow document (FLOWLANG §1).
	// This lists them read-only, per flow, with what the flow runs them on.
	import { parse } from 'yaml';
	import type { Flow, Project } from '$lib/domain';
	import { to } from '$lib/guild';
	import { normalizeDoc, type DocAgent, type FlowDoc } from '$lib/flowdoc';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import { I } from '$lib/ui/icons';

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
</script>

<Callout hue="az">
	Agents live inside their flow’s document, so each flow carries its own. They are read-only here
	for now — edit them in the flow editor’s YAML view.
</Callout>

{#if error && !flows}
	<ErrorCard cause="Couldn’t load flows" detail={error} {onretry} />
{:else if !flows}
	<Skeleton pane lines={5} />
{:else if !relevant.length}
	<EmptyState title="No agents yet" text="Agents arrive with this workshop’s first flow.">
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
				<h2 class="fname">{p.flow.name}</h2>
				{#if p.flow.builtin}<span class="pill sm t-mut">built-in</span>{/if}
				{#if p.flow.name === project.defaultFlow}<span class="pill sm t-brand">default</span>{/if}
				<span class="mono fnt">@{p.flow.hash.slice(0, 8)}</span>
				<span class="count">{p.agents.length}</span>
				<span class="spacer"></span>
				<a class="btn btn-sm" href={to.flow(project.id, p.flow.name)}
					>Edit in flow editor {I.forward}</a
				>
			</div>
			{#if p.parseError}
				<div class="pane-body">
					<ErrorCard cause="This flow’s YAML didn’t parse here" detail={p.parseError} />
				</div>
			{:else if !p.agents.length}
				<div class="empty-line">No agents in this flow.</div>
			{:else}
				<div class="pane-body card-grid agents">
					{#each p.agents as a (a.agent.name)}
						<article class="ag card-in">
							<div class="row nowrap ah">
								<StageChip type="agent" />
								<span class="mono an ell">{a.agent.name}</span>
								<span class="spacer"></span>
								{#if a.agent.read_only}<span class="pill sm t-tea" title="May not edit files"
										>read-only</span
									>{:else}<span class="pill sm t-amb" title="May edit files">writes</span>{/if}
								{#if a.agent.allow_bash}<span class="pill sm t-grn" title="May run shell commands"
										>bash</span
									>{:else}<span class="pill sm t-mut" title="No shell">no bash</span>{/if}
							</div>
							{#if a.agent.description}<p class="lede">{a.agent.description}</p>{/if}
							<dl class="kv">
								<dt>runs on</dt>
								<dd class="v">
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
								</dd>
								<dt>inputs</dt>
								<dd class="v io">
									{#each a.agent.inputs ?? [] as i (i.name)}
										<span class="port" title={i.description}
											><span class="ldot sm flat hue-grn" class:hollow={i.optional}></span><span
												class="mono">{i.name}</span
											>{#if i.optional}<span class="fnt"> optional</span>{/if}</span
										>
									{:else}<span class="fnt">the ticket only</span>{/each}
								</dd>
								<dt>outputs</dt>
								<dd class="v io">
									{#each a.agent.outputs ?? [] as o (o.name)}
										<span class="port" title={o.description}
											><span class="ldot sm flat {o.type === 'verdict' ? 'hue-vio' : 'hue-az'}"
											></span><span class="mono">{o.name}</span>
											<span class="mono fnt">{o.path}</span>
											{#if o.type === 'verdict'}<span class="pill sm t-vio">verdict</span
												>{/if}</span
										>
									{:else}<span class="fnt">none</span>{/each}
								</dd>
							</dl>
							{#if a.agent.prompt}
								<details>
									<summary class="disclosure small mut">Prompt</summary>
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
	.fname {
		font-family: var(--mono);
		font-style: normal;
		font-size: var(--fs-aside);
		font-weight: 600;
	}
	.agents {
		--min: 420px;
	}
	.ag {
		padding: 10px 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.ah {
		gap: 6px;
	}
	.an {
		font-size: var(--fs-body);
		font-weight: 600;
	}
	dl.kv {
		--k: 64px;
		font-size: var(--fs-help);
	}
	.v {
		display: flex;
		flex-direction: column;
		gap: 2px;
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
	.port {
		display: inline-flex;
		align-items: center;
		gap: 5px;
	}
	.use .mono,
	.port .mono {
		font-size: var(--fs-meta);
	}
	.prompt {
		margin-top: 6px;
		max-height: 260px;
		font-size: var(--fs-meta);
	}
</style>
