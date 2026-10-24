<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Workshop agents tab (DESIGN 7.7). There is no agent library on the
	// engine yet: agents are declared inside each flow document (FLOWLANG §1).
	// This lists them read-only, per flow, with what the flow runs them on.
	import { parse } from 'yaml';
	import type { Flow, Project } from '$lib/domain';
	import { to } from '$lib/guild';
	import { expandDoc, normalizeDoc, type DocAgent, type FlowDoc, paramDefault } from '$lib/flowdoc';
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
				// Stages inside blocks run agents too: count them expanded.
				const doc = expandDoc(normalizeDoc(parse(flow.yaml)));
				const agents = (doc.agents ?? []).map((agent) => ({
					agent,
					stages: doc.stages
						.filter((s) => s.type === 'agent' && s.agent === agent.name)
						.map((s) => ({
							id: s.id,
							harness: s.harness || paramDefault(doc, 'harness') || '—',
							model: s.model || paramDefault(doc, 'model') || '—'
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
	{t('workshop.agents.callout')}
</Callout>

{#if error && !flows}
	<ErrorCard cause={t('workshop.agents.loadError')} detail={error} {onretry} />
{:else if !flows}
	<Skeleton pane lines={5} />
{:else if !relevant.length}
	<EmptyState title={t('workshop.agents.emptyTitle')} text={t('workshop.agents.emptyText')}>
		<a class="btn btn-primary" href={to.workshop(project.id, 'flows')}
			>{t('workshop.agents.flows')}</a
		>
	</EmptyState>
{:else}
	{#if total === 0}
		<EmptyState
			compact
			title={t('workshop.agents.noneDeclaredTitle')}
			text={t('workshop.agents.noneDeclaredText')}
		/>
	{/if}
	{#each parsed as p (p.flow.name)}
		<section class="c pane">
			<div class="pane-head">
				<h2 class="fname">{p.flow.name}</h2>
				{#if p.flow.builtin}<span class="pill sm t-mut">{t('workshop.agents.builtin')}</span>{/if}
				{#if p.flow.name === project.defaultFlow}<span class="pill sm t-brand"
						>{t('workshop.agents.default')}</span
					>{/if}
				<span class="mono fnt">@{p.flow.hash.slice(0, 8)}</span>
				<span class="count">{p.agents.length}</span>
				<span class="spacer"></span>
				<a class="btn btn-sm" href={to.flow(project.id, p.flow.name)}
					>{t('workshop.agents.editInEditor')} {I.forward}</a
				>
			</div>
			{#if p.parseError}
				<div class="pane-body">
					<ErrorCard cause={t('workshop.agents.parseError')} detail={p.parseError} />
				</div>
			{:else if !p.agents.length}
				<div class="empty-line">{t('workshop.agents.noneInFlow')}</div>
			{:else}
				<div class="pane-body card-grid agents">
					{#each p.agents as a (a.agent.name)}
						<article class="ag card-in">
							<div class="row nowrap ah">
								<StageChip type="agent" />
								<span class="mono an ell">{a.agent.name}</span>
								<span class="spacer"></span>
								{#if a.agent.read_only}<span
										class="pill sm t-tea"
										title={t('workshop.agents.readOnlyTitle')}>{t('workshop.agents.readOnly')}</span
									>{:else}<span class="pill sm t-amb" title={t('workshop.agents.writesTitle')}
										>{t('workshop.agents.writes')}</span
									>{/if}
								{#if a.agent.allow_bash}<span
										class="pill sm t-grn"
										title={t('workshop.agents.bashTitle')}>{t('workshop.agents.bash')}</span
									>{:else}<span class="pill sm t-mut" title={t('workshop.agents.noBashTitle')}
										>{t('workshop.agents.noBash')}</span
									>{/if}
							</div>
							{#if a.agent.description}<p class="lede">{a.agent.description}</p>{/if}
							<dl class="kv">
								<dt>{t('workshop.agents.runsOn')}</dt>
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
										<span class="fnt">{t('workshop.agents.unused')}</span>
									{/each}
								</dd>
								<dt>{t('workshop.agents.inputs')}</dt>
								<dd class="v io">
									{#each a.agent.inputs ?? [] as i (i.name)}
										<span class="port" title={i.description}
											><span class="ldot sm flat hue-grn" class:hollow={i.optional}></span><span
												class="mono">{i.name}</span
											>{#if i.optional}<span class="fnt">{t('workshop.agents.optional')}</span
												>{/if}</span
										>
									{:else}<span class="fnt">{t('workshop.agents.ticketOnly')}</span>{/each}
								</dd>
								<dt>{t('workshop.agents.outputs')}</dt>
								<dd class="v io">
									{#each a.agent.outputs ?? [] as o (o.name)}
										<span class="port" title={o.description}
											><span class="ldot sm flat {o.type === 'verdict' ? 'hue-vio' : 'hue-az'}"
											></span><span class="mono">{o.name}</span>
											<span class="mono fnt">{o.path}</span>
											{#if o.type === 'verdict'}<span class="pill sm t-vio"
													>{t('workshop.agents.verdict')}</span
												>{/if}</span
										>
									{:else}<span class="fnt">{t('workshop.agents.none')}</span>{/each}
								</dd>
							</dl>
							{#if a.agent.prompt}
								<details>
									<summary class="disclosure small mut">{t('workshop.agents.prompt')}</summary>
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
	/* Stage and harness keep their words whole; the model gives way. */
	.use > * {
		flex: none;
		white-space: nowrap;
	}
	.use > .ell {
		flex: 0 1 auto;
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
