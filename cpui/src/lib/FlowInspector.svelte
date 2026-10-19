<script lang="ts">
	import type { Selection } from '$lib/FlowGraph.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import Stepper from '$lib/ui/Stepper.svelte';
	import { esc } from '$lib/ui/html';
	import { EDGE_HUE, STAGE_HUE, STAGE_WORD } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { ms } from '$lib/ui/motion';
	import { fade } from 'svelte/transition';
	import BindingRows from '$lib/flow/BindingRows.svelte';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import OptionalNumber from '$lib/ui/OptionalNumber.svelte';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		ABORT,
		DEBUG,
		DONE,
		EDGE_KINDS,
		SENTINEL_CHANGED,
		SENTINEL_DECISION,
		SENTINEL_FAILURE,
		chainLinks,
		edgeKindsFor,
		edgeOf,
		feedSentinelFor,
		ioEntries,
		isFeedSentinel,
		pruneEdgeTarget,
		sentinelsFor,
		setEdge,
		freshStage,
		movePhase,
		triggerLabel,
		verdictOutput,
		type DocEdge,
		type DocStage,
		type EdgeKind,
		type DocAgent,
		type FlowDoc,
		type StageKind
	} from '$lib/flowdoc';

	// Flows are portable, project-independent data: this editor takes NOTHING
	// from any project. Agent / harness / model / run_ref are free text and are
	// validated against the project only at run creation (preflight). `agents`
	// is an OPTIONAL hint list (built-ins ∪ a project's authored agents) used
	// only to render binding fields from declared I/O — never a constraint.
	let {
		doc,
		selection,
		onchange,
		onselect,
		agents = [],
		actions = [],
		signals = []
	}: {
		doc: FlowDoc;
		selection: Selection;
		onchange: () => void;
		onselect: (sel: Selection) => void;
		agents?: DocAgent[];
		actions?: ActionSpec[];
		signals?: SignalSpec[];
	} = $props();

	const stage = $derived(
		selection?.kind === 'stage' ? doc.stages.find((s) => s.id === selection.id) : undefined
	);

	// The declared I/O of the selected stage's agent, when known. feedback is
	// excluded from bindable inputs — it is filled by edge feed:, not a stage.
	const agentDecl = $derived(
		stage?.type === 'agent' ? agents.find((a) => a.name === stage.agent) : undefined
	);
	const declInputs = $derived(agentDecl?.inputs ?? []);
	const declOutputs = $derived(agentDecl?.outputs ?? []);
	const declVerdict = $derived(verdictOutput(agentDecl));

	// The selected stage's action signature, when it names a registered one.
	const actionSig = $derived(
		stage?.type === 'action' ? actions.find((a) => a.slug === stage.action) : undefined
	);
	const waitSig = $derived(
		stage?.type === 'wait' ? signals.find((g) => g.slug === stage.for) : undefined
	);

	// Bindings are one form everywhere (FLOWLANG §4), so there is one editor:
	// read a name's value, write it back preserving order.
	function bindingValue(s: DocStage, field: 'input' | 'output', name: string): string {
		return s[field]?.[name] ?? '';
	}
	function setBinding(s: DocStage, field: 'input' | 'output', name: string, value: string) {
		const cur = { ...(s[field] ?? {}) };
		if (value) cur[name] = value;
		else delete cur[name];
		s[field] = Object.keys(cur).length ? cur : undefined;
		onchange();
	}
	// Reorder a contract's bindings into declaration order, so the YAML reads
	// like the contract and a round-trip never churns the flow hash.
	function orderBindings(s: DocStage, field: 'input' | 'output', names: string[]) {
		const cur = s[field];
		if (!cur) return;
		const rec: Record<string, string> = {};
		for (const n of names) if (cur[n]) rec[n] = cur[n];
		for (const [k, v] of Object.entries(cur)) if (!(k in rec)) rec[k] = v;
		s[field] = rec;
	}
	const freeBindings = $derived.by(() => {
		if (!stage) return [] as [string, string][];
		const declared = new Set(
			stage.type === 'agent'
				? declInputs.map((d) => d.name)
				: stage.type === 'action'
					? (actionSig?.inputs ?? []).map((d) => d.name)
					: []
		);
		return ioEntries(stage.input).filter(([k]) => !declared.has(k));
	});
	const isDeclared = (k: string) =>
		stage?.type === 'agent'
			? declInputs.some((d) => d.name === k)
			: (actionSig?.inputs ?? []).some((d) => d.name === k);
	// Once a stage shows its unrecognized bindings, the section stays while
	// the stage is selected — clearing the last row mid-edit must not unmount
	// the editor under the cursor.
	let freeShown = $state<Record<string, boolean>>({});
	$effect(() => {
		if (stage && freeBindings.length && !freeShown[stage.id]) freeShown[stage.id] = true;
	});

	// The inspector cross-fades between selections instead of jumping: the
	// outgoing and incoming views share one grid cell for the moment.
	const viewKey = $derived(
		selection?.kind === 'stage'
			? `s:${selection.id}`
			: selection?.kind === 'edge'
				? `e:${selection.stage}:${selection.edge}`
				: 'flow'
	);
	const xf = () => ({ duration: ms('--dur-fade', 160) });
	const ADD: StageKind[] = ['agent', 'command', 'human_gate', 'action', 'wait'];

	const edgeSel = $derived(
		selection?.kind === 'edge'
			? {
					stage: doc.stages.find((s) => s.id === selection.stage),
					kind: selection.edge as EdgeKind
				}
			: undefined
	);
	const stageIds = $derived(doc.stages.map((s) => s.id));

	function addStage(type: StageKind) {
		const s = freshStage(doc, type);
		doc.stages.push(s);
		onchange();
		onselect({ kind: 'stage', id: s.id });
	}

	function renameStage(oldId: string, newId: string) {
		newId = newId.trim();
		if (!newId || newId === oldId || newId === DONE || newId === ABORT || newId === DEBUG) return;
		if (doc.stages.some((s) => s.id === newId)) return;
		for (const s of doc.stages) {
			if (s.id === oldId) s.id = newId;
			if (s.on_success === oldId) s.on_success = newId;
			for (const kind of EDGE_KINDS) {
				for (const link of chainLinks(edgeOf(s, kind))) {
					if (link.to === oldId) link.to = newId;
				}
			}
		}
		onchange();
		onselect({ kind: 'stage', id: newId });
	}

	async function deleteStage(id: string) {
		const refs = doc.stages.filter(
			(s) =>
				s.id !== id &&
				(s.on_success === id ||
					EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === id)))
		).length;
		const ok = await confirmDialog({
			title: `Delete stage ${id}?`,
			body: `The stage <b>${esc(id)}</b> is removed from this draft.${refs ? ` ${refs} other stage${refs === 1 ? '' : 's'} pointing at it ${refs === 1 ? 'is' : 'are'} rewired: success goes to <b>done</b>, failure links to it are dropped.` : ''} Nothing changes for runs until you save.`,
			action: 'Delete stage'
		});
		if (!ok) return;
		doc.stages = doc.stages.filter((s) => s.id !== id);
		for (const s of doc.stages) {
			if (s.on_success === id) s.on_success = DONE;
			for (const kind of EDGE_KINDS) {
				setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
			}
		}
		onchange();
		onselect(null);
	}

	function addEdge(s: NonNullable<typeof stage>, kind: EdgeKind) {
		setEdge(s, kind, { to: doc.stages[0]?.id ?? '', max: 3 });
		onchange();
		onselect({ kind: 'edge', stage: s.id, edge: kind });
	}

	const targetsGate = (id: string) => doc.stages.find((x) => x.id === id)?.type === 'human_gate';
	// Unlimited (-1) needs a human on the link: gate target (each firing
	// parks on a decision) or gate source (the link only fires on reject).
	const unlimitedOK = (sourceType: StageKind, to: string) =>
		sourceType === 'human_gate' || targetsGate(to);

	// retarget switches one chain link's target, normalizing the link's other
	// fields: abort and debug links carry no max/feed/then; stage links need a
	// max, and -1 survives only while a gate remains on the link.
	function retarget(sourceType: StageKind, link: DocEdge, to: string) {
		link.to = to;
		if (to === ABORT || to === DEBUG) {
			link.max = undefined;
			link.then = undefined;
		} else if (link.max == null || (link.max === -1 && !unlimitedOK(sourceType, to))) {
			link.max = 3;
		}
		onchange();
	}

	/** Delete what is selected (⌫ / Del on the canvas): a stage asks first;
	 *  an edge goes at once (re-adding it is one click). */
	export function deleteSelection() {
		if (selection?.kind === 'stage') void deleteStage(selection.id);
		else if (selection?.kind === 'edge') {
			const s = doc.stages.find((x) => x.id === selection.stage);
			if (!s) return;
			setEdge(s, selection.edge as EdgeKind, undefined);
			onchange();
			onselect({ kind: 'stage', id: s.id });
		}
	}

	// appendLink adds a fallback ("then") after the chain's last link.
	function appendLink(e: DocEdge) {
		let last = e;
		while (last.then) last = last.then;
		if (last.to === ABORT || last.to === DEBUG || last.max === -1) return; // nothing follows abort/debug/unlimited
		last.then = { to: doc.stages[0]?.id ?? '', max: 3 };
		onchange();
	}

	// dropLink removes the chain's last link (the whole edge if it is alone).
	function dropLink(s: NonNullable<typeof stage>, kind: EdgeKind, e: DocEdge) {
		const links = chainLinks(e);
		if (links.length === 1) {
			setEdge(s, kind, undefined);
			onselect({ kind: 'stage', id: s.id });
		} else {
			links[links.length - 2].then = undefined;
		}
		onchange();
	}
</script>

<div class="insp form-dense">
	{#key viewKey}
		<div class="view-x" in:fade={xf()} out:fade={xf()}>
			{#if stage}
				{@const s = stage}
				<div class="ih">
					<StageChip type={s.type} />
					<span class="mono iid ell">{s.id}</span>
					<span class="spacer"></span>
					<button
						class="btn-danger btn-sm"
						onclick={() => deleteStage(s.id)}
						title="Delete stage (⌫)">Delete…</button
					>
				</div>
				<div class="field">
					<label for="st-id">Id</label>
					<input
						id="st-id"
						class="mono"
						value={s.id}
						onchange={(e) => renameStage(s.id, e.currentTarget.value)}
					/>
					<div class="help">Renaming updates every reference.</div>
				</div>
				{#if doc.phases?.length}
					<div class="field">
						<label for="st-phase">Phase</label>
						<select
							id="st-phase"
							value={s.phase}
							onchange={(e) => {
								movePhase(doc, s.id, e.currentTarget.value);
								onchange();
							}}
						>
							{#each doc.phases as ph (ph.id)}
								<option value={ph.id}>{ph.id} — on {triggerLabel(ph.on)}</option>
							{/each}
						</select>
						<div class="help">Its routes stay inside it; the phase's last stage ends it.</div>
					</div>
				{/if}

				{#if s.type === 'agent'}
					<div class="field">
						<label for="st-agent">Agent</label>
						<input
							id="st-agent"
							list="agent-names"
							bind:value={s.agent}
							oninput={onchange}
							placeholder="builder"
						/>
						<datalist id="agent-names">
							{#each agents as a (a.name)}<option value={a.name}></option>{/each}
						</datalist>
						<div class="help">A name declared in this flow’s agents block.</div>
					</div>
					<div class="field">
						<label for="st-harness">Harness</label>
						<input
							id="st-harness"
							bind:value={s.harness}
							oninput={onchange}
							placeholder={doc.defaults.harness || 'opencode'}
						/>
						<div class="help">
							Overrides the flow default; empty = {doc.defaults.harness || 'flow default'}.
						</div>
					</div>
					<div class="field">
						<label for="st-model">Model</label>
						<input
							id="st-model"
							bind:value={s.model}
							oninput={onchange}
							placeholder={doc.defaults.model}
						/>
						<div class="help">
							Overrides the flow default; empty = {doc.defaults.model || 'flow default'}.
						</div>
					</div>
					<div class="field">
						<label for="st-turns">Max turns</label>
						<OptionalNumber
							id="st-turns"
							label="Max turns"
							bind:value={s.max_turns}
							empty="flow default{doc.defaults.max_turns ? ` · ${doc.defaults.max_turns}` : ''}"
							fallback={doc.defaults.max_turns ?? 50}
							{onchange}
						/>
					</div>
					<div class="field">
						<label for="st-timeout">Timeout</label>
						<OptionalDuration
							id="st-timeout"
							label="Timeout"
							bind:value={s.timeout}
							empty="flow default{doc.defaults.timeout ? ` · ${doc.defaults.timeout}` : ''}"
							fallback={doc.defaults.timeout || '30m'}
							{onchange}
						/>
					</div>
					{#if declInputs.length > 0}
						<div class="grp">
							<div class="lbl">Inputs</div>
							<div class="help">Bind each declared input.</div>
						</div>
						{#each declInputs as d (d.name)}
							{@const val = bindingValue(s, 'input', d.name)}
							<div class="io-bind card-in field">
								<label for="in-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.optional}<span class="fnt">optional</span>{:else}<span class="hue-amb"
											>required</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								<input
									id="in-{d.name}"
									list="agent-sentinels"
									placeholder="artifacts/plan.md"
									value={val}
									oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
								/>
								{#if isFeedSentinel(val)}
									<div class="help">
										Filled by whichever edge brings the run back here — empty on the first attempt,
										so this input must be optional on the agent.
									</div>
								{/if}
							</div>
						{/each}
						<datalist id="agent-sentinels">
							{#each sentinelsFor('agent') as v (v)}<option value={v}></option>{/each}
						</datalist>
					{:else if s.agent}
						<div class="help">
							{agentDecl
								? `Agent “${s.agent}” declares no inputs — its prompt gets the task and nothing else.`
								: `Unknown agent “${s.agent}”: bind its inputs after it exists in the project's library.`}
						</div>
					{/if}

					{#if declOutputs.length > 0}
						<div class="grp">
							<div class="lbl">Outputs</div>
							<div class="help">Declared by the agent; edit a path only to override it here.</div>
						</div>
						{#each declOutputs as d (d.name)}
							<div class="io-bind card-in field">
								<label for="out-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.type === 'verdict'}<span class="pill sm t-vio">verdict</span>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								<input
									id="out-{d.name}"
									value={bindingValue(s, 'output', d.name) || (d.path ?? '')}
									placeholder={d.path}
									oninput={(e) => {
										const v = e.currentTarget.value.trim();
										setBinding(s, 'output', d.name, v === d.path ? '' : v);
									}}
								/>
							</div>
						{/each}
					{/if}
					{#if declVerdict}
						<div class="help">
							Renders a verdict, so this stage's <span class="mono">on_reject</span> fires on REJECT
							— and the target of that edge receives its comments as
							<span class="mono">{SENTINEL_DECISION}</span>.
						</div>
					{:else if s.agent && agentDecl && edgeOf(s, 'on_reject')}
						<div class="help hue-amb">
							<span class="mono">on_reject</span> will never fire: agent “{s.agent}” declares no
							verdict output. Add one on the agent (Outputs → type
							<span class="mono">verdict</span>).
						</div>
					{/if}
				{:else if s.type === 'command'}
					<div class="field">
						<label for="st-runref">run_ref</label>
						<input
							id="st-runref"
							class="mono"
							bind:value={s.run_ref}
							oninput={onchange}
							placeholder="verify"
						/>
						<div class="help">A command name in the project running this flow.</div>
					</div>
					<div class="field">
						<label for="st-run">…or literal script</label>
						<textarea
							id="st-run"
							class="mono"
							bind:value={s.run}
							oninput={onchange}
							rows="3"
							placeholder="make test"></textarea>
					</div>
					<Toggle
						checked={!!s.each_repo}
						label="Run in each repository"
						title="Multi-repo runs: run once per checkout instead of once in the primary (what git commit / push want)"
						onchange={(v) => {
							s.each_repo = v || undefined;
							onchange();
						}}
					/>
					<div class="grp">
						<div class="lbl">Reads</div>
						<div class="help">Checked before the script runs.</div>
					</div>
					<BindingRows stage={s} field="input" valuePlaceholder="artifacts/commit.md" {onchange} />
					<div class="grp">
						<div class="lbl">Writes</div>
						<div class="help">Must exist when the script exits 0.</div>
					</div>
					<BindingRows
						stage={s}
						field="output"
						valuePlaceholder="artifacts/report.txt"
						{onchange}
					/>
					<div class="help">
						The name is a label — it documents what each file is for and titles its dot in the
						graph.
					</div>
				{:else if s.type === 'human_gate'}
					<div class="field">
						<label for="st-question">Question</label>
						<textarea
							id="st-question"
							bind:value={s.question}
							oninput={onchange}
							rows="2"
							placeholder="Approve this implementation plan?"></textarea>
						<div class="help">
							What the human is being asked; approve continues, reject follows on_reject.
						</div>
					</div>
					<div class="grp">
						<div class="lbl">Shows</div>
						<div class="help">
							One artifact to review, or a link: <span class="mono">$WORKSPACE</span> hands over the
							terminal/IDE links; <span class="mono">$MR_URL</span> the merge request opened by a
							<span class="mono">create_mr</span> action.
						</div>
					</div>
					<BindingRows
						stage={s}
						field="input"
						valuePlaceholder="artifacts/plan.md"
						addLabel="Add what it shows"
						{onchange}
					/>
					<div class="grp">
						<div class="lbl">Answered automatically by</div>
						<div class="help">Signals that supply the same answer a human would.</div>
					</div>
					<div class="grid2">
						<div class="field">
							<label for="st-auto-ok">Approve when</label>
							<select
								id="st-auto-ok"
								value={s.auto_approve ?? ''}
								onchange={(e) => {
									s.auto_approve = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each signals as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_reject}>{sig.title}</option
									>
								{/each}
							</select>
						</div>
						<div class="field">
							<label for="st-auto-no">Reject when</label>
							<select
								id="st-auto-no"
								value={s.auto_reject ?? ''}
								onchange={(e) => {
									s.auto_reject = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each signals as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_approve}
										>{sig.title}</option
									>
								{/each}
							</select>
						</div>
					</div>
					{#if s.auto_approve || s.auto_reject}
						<div class="help">
							The gate keeps accepting a manual answer the whole time; whichever lands first wins.
						</div>
					{/if}
				{:else if s.type === 'action'}
					<div class="field">
						<label for="st-action">Action</label>
						<select
							id="st-action"
							value={s.action ?? ''}
							onchange={(e) => {
								s.action = e.currentTarget.value || undefined;
								// Prefill the new action's file inputs with their conventional paths.
								const sig = actions.find((a) => a.slug === s.action);
								const rec: Record<string, string> = {};
								for (const d of sig?.inputs ?? []) if (d.defaultPath) rec[d.name] = d.defaultPath;
								s.input = Object.keys(rec).length ? rec : undefined;
								onchange();
							}}
						>
							<option value="">—</option>
							{#each actions as a (a.slug)}<option value={a.slug}>{a.title}</option>{/each}
						</select>
						<div class="help">A built-in apothecary integration.</div>
					</div>
					{#if actionSig}
						{#if actionSig.description}<div class="help">{actionSig.description}</div>{/if}
						{#if actionSig.requires}
							<div class="help">
								Needs a <span class="mono">{actionSig.requires}</span> integration on the project running
								this flow.
							</div>
						{/if}
						{#each actionSig.inputs ?? [] as d (d.name)}
							<div class="io-bind card-in field">
								<label for="act-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.required}<span class="hue-amb">required</span>{:else}<span class="fnt"
											>optional</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								{#if d.kind === 'enum'}
									<select
										id="act-{d.name}"
										value={bindingValue(s, 'input', d.name) || d.defaultValue || ''}
										onchange={(e) => {
											setBinding(s, 'input', d.name, e.currentTarget.value);
											onchange();
										}}
									>
										{#each d.values ?? [] as v (v)}<option value={v}>{v}</option>{/each}
									</select>
								{:else}
									<input
										id="act-{d.name}"
										value={bindingValue(s, 'input', d.name)}
										placeholder={d.defaultPath ?? ''}
										oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
									/>
								{/if}
							</div>
						{/each}
						{#each actionSig.outputs ?? [] as d (d.name)}
							<div class="help">
								Produces <span class="mono">{d.name}</span>{#if d.sentinel}, available to later
									stages as
									<span class="mono">{d.sentinel}</span>{/if}{#if d.path}
									at <span class="mono">{d.path}</span>{/if}{#if d.description}
									— {d.description}{/if}.
							</div>
						{/each}
					{:else if s.action}
						<div class="help hue-amb">Unknown action “{s.action}”.</div>
					{/if}
				{:else if s.type === 'wait'}
					<div class="field">
						<label for="st-for">Wait for</label>
						<select
							id="st-for"
							value={s.for ?? ''}
							onchange={(e) => {
								s.for = e.currentTarget.value || undefined;
								onchange();
							}}
						>
							<option value="">—</option>
							{#each signals as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
						</select>
						<div class="help">An external signal; the run parks until it fires.</div>
					</div>
					{#if waitSig}
						{#if waitSig.description}<div class="help">{waitSig.description}</div>{/if}
						{#if waitSig.requires}
							<div class="help">
								Needs a <span class="mono">{waitSig.requires}</span> integration on the project running
								this flow.
							</div>
						{/if}
					{:else if s.for}
						<div class="help hue-amb">Unknown signal “{s.for}”.</div>
					{/if}
					<div class="field">
						<label for="st-wait-timeout">Timeout</label>
						<OptionalDuration
							id="st-wait-timeout"
							label="Timeout"
							bind:value={s.timeout}
							empty="waits indefinitely"
							fallback="24h"
							{onchange}
						/>
					</div>
				{/if}

				{#if (freeBindings.length > 0 || freeShown[s.id]) && (s.type === 'agent' || s.type === 'action')}
					<div class="grp">
						<div class="lbl">Unrecognized bindings</div>
						<div class="help hue-amb">
							Not declared by this stage's {s.type === 'agent' ? 'agent' : 'action'} — run creation will
							refuse it.
						</div>
					</div>
					{#key s.type === 'agent' ? s.agent : s.action}
						<BindingRows stage={s} field="input" keep={isDeclared} {onchange} />
					{/key}
				{/if}

				<div class="field">
					<label for="st-desc">Description</label>
					<input
						id="st-desc"
						bind:value={s.description}
						oninput={onchange}
						placeholder="implement the approved plan"
					/>
					<div class="help">Optional note on what this stage is for.</div>
				</div>

				<div class="field">
					<label for="st-succ">On success</label>
					<select id="st-succ" bind:value={s.on_success} {onchange}>
						<option value={DONE}>done (complete the run)</option>
						{#each stageIds.filter((id) => id !== s.id) as id (id)}<option value={id}>{id}</option
							>{/each}
					</select>
				</div>

				<div class="chips">
					{#each edgeKindsFor(s.type) as kind (kind)}
						{#if !edgeOf(s, kind)}
							<button class="btn-sm hue-{EDGE_HUE[kind]}" onclick={() => addEdge(s, kind)}
								>{I.add} {kind}</button
							>
						{:else}
							<button
								class="btn-sm btn-sel"
								onclick={() => onselect({ kind: 'edge', stage: s.id, edge: kind })}
								>{kind} {I.forward}</button
							>
						{/if}
					{/each}
				</div>
				{#if s.type === 'agent' || s.type === 'command'}
					<div class="help">
						Timeouts route via on_timeout when declared, else fall back to on_fail.
					</div>
				{/if}
			{:else if edgeSel?.stage}
				{@const s = edgeSel.stage}
				{@const kind = edgeSel.kind}
				{@const e = edgeOf(s, kind)}
				{#if e}
					{@const links = chainLinks(e)}
					<div class="ih">
						<span class="pill sm mono t-{EDGE_HUE[kind]}">{kind}</span>
						<span class="mut small">from</span>
						<span class="mono iid ell">{s.id}</span>
						<span class="spacer"></span>
						<button
							class="btn-danger btn-sm"
							title="Remove edge (⌫)"
							onclick={() => {
								setEdge(s, kind, undefined);
								onchange();
								onselect({ kind: 'stage', id: s.id });
							}}>Remove edge</button
						>
					</div>
					<div class="help">
						This edge carries <span class="mono">{feedSentinelFor(kind)}</span>. It lands wherever
						the target stage binds that value as an input — bind it there, or the retry runs blind.
					</div>
					{#each links as link, depth (depth)}
						<div class="link" class:fallback={depth > 0}>
							{#if depth > 0}<div class="help">then (once the link above is exhausted)</div>{/if}
							<div class="field">
								<label for="ed-to-{depth}">To</label>
								<select
									id="ed-to-{depth}"
									value={link.to}
									onchange={(ev) => retarget(s.type, link, ev.currentTarget.value)}
								>
									{#each stageIds as id (id)}<option value={id}>{id}</option>{/each}
									<option value={ABORT}>abort — stop the run deliberately</option>
									<option value={DEBUG}
										>debug — park for a human to fix the workspace, then re-run this stage</option
									>
								</select>
							</div>
							{#if link.to !== ABORT && link.to !== DEBUG}
								{@const canUnlimit = unlimitedOK(s.type, link.to)}
								{#if canUnlimit}
									<Toggle
										checked={link.max === -1}
										label="No limit (∞)"
										title="Every firing costs a human decision{targetsGate(link.to)
											? '; rejecting the gate ends the run'
											: ''}"
										onchange={(v) => {
											link.max = v ? -1 : 3;
											if (link.max === -1) link.then = undefined;
											onchange();
										}}
									/>
								{/if}
								<div class="field">
									<label for="ed-max-{depth}">Max</label>
									{#if link.max === -1}
										<div class="mono unl">∞ unlimited</div>
									{:else}
										<Stepper
											id="ed-max-{depth}"
											label="Max"
											min={1}
											bind:value={
												() => link.max ?? 1,
												(v) => {
													link.max = v;
													onchange();
												}
											}
										/>
									{/if}
									<div class="help">
										Times this link may fire{depth === 0 && !link.then
											? ' before the run is abandoned'
											: ''}{canUnlimit ? ' · or no limit, a human decides each time' : ''}.
									</div>
								</div>
							{/if}
						</div>
					{/each}
					<div class="row">
						{#if links[links.length - 1].to !== ABORT && links[links.length - 1].to !== DEBUG && links[links.length - 1].max !== -1}
							<button class="btn-sm" onclick={() => appendLink(e)}>{I.add} then (fallback)</button>
						{/if}
						<button class="btn-sm btn-ghost" onclick={() => dropLink(s, kind, e)}
							>− remove last link</button
						>
					</div>
					<div class="help">
						Links fire top-down: each keeps its own counter; when one is exhausted the next failure
						falls through. An exhausted chain abandons the run.
					</div>
				{/if}
			{:else}
				<div class="ih">
					<span class="serif ttl">The formula</span>
					<span class="spacer"></span>
					<span class="fnt tiny">nothing selected</span>
				</div>
				<div class="field">
					<label for="fl-desc">Description</label>
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
					<label for="fl-harness">Default harness</label>
					<input
						id="fl-harness"
						bind:value={doc.defaults.harness}
						oninput={onchange}
						placeholder="opencode"
					/>
					<div class="help">The harness kind stages run on unless they override it.</div>
				</div>
				<div class="field">
					<label for="fl-model">Default model</label>
					<input
						id="fl-model"
						bind:value={doc.defaults.model}
						oninput={onchange}
						placeholder="openrouter/anthropic/claude-sonnet-4.6"
					/>
				</div>
				<div class="field">
					<label for="fl-timeout">Default timeout</label>
					<OptionalDuration
						id="fl-timeout"
						label="Default timeout"
						bind:value={doc.defaults.timeout}
						empty="no default"
						fallback="30m"
						{onchange}
					/>
				</div>
				<div class="field">
					<label for="fl-turns">Default max turns</label>
					<OptionalNumber
						id="fl-turns"
						label="Default max turns"
						bind:value={doc.defaults.max_turns}
						empty="no default"
						fallback={50}
						{onchange}
					/>
				</div>
				<div class="lbl">Budget</div>
				<div class="field">
					<label for="fl-cost">Max cost (USD)</label>
					<input
						id="fl-cost"
						type="number"
						min="0"
						step="0.5"
						placeholder="no cap"
						value={doc.budget?.max_cost_usd ?? ''}
						oninput={(e) => {
							doc.budget.max_cost_usd =
								e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
							onchange();
						}}
					/>
				</div>
				<div class="field">
					<label for="fl-tturns">Max total turns</label>
					<OptionalNumber
						id="fl-tturns"
						label="Max total turns"
						bind:value={doc.budget.max_total_turns}
						empty="no cap"
						fallback={300}
						{onchange}
					/>
				</div>
				<div class="lbl">Add a stage</div>
				<div class="chips">
					{#each ADD as t (t)}
						<button class="btn-sm" onclick={() => addStage(t)}
							><span class="ldot sm flat hue-{STAGE_HUE[t]}"></span>{STAGE_WORD[t] ?? t}</button
						>
					{/each}
				</div>
				<div class="help">
					Click a node or edge to edit it. Drag from a node's coloured handles to rewire control
					flow: green = success, rose = on_fail, teal = on_timeout, amber = on_reject. Edges may
					target the abort terminal to stop the run deliberately, or the debug leaf to park it for a
					human and then run the stage again. The faint dashed lines are dataflow — they are derived
					from the artifact paths stages read and write, so you change them by editing an input or
					output, not by dragging.
				</div>
			{/if}
		</div>
	{/key}
</div>

<style>
	/* The inspector sits on control glass, given by the page (DESIGN 7.8). */
	.insp {
		display: grid;
		padding: 12px 14px 16px;
		font-size: var(--fs-ui);
	}
	/* Outgoing and incoming views share the cell while they cross-fade. */
	.view-x {
		grid-area: 1 / 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--ln);
		min-width: 0;
	}
	.iid {
		font-size: var(--fs-body);
		font-weight: 600;
	}
	.ttl {
		font-size: var(--fs-section);
	}
	.grp {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin-top: 4px;
	}
	.lbl {
		margin-top: 4px;
	}
	.io-bind {
		padding: 7px 9px;
	}
	.io-bind label {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.link {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.link.fallback {
		border-left: 2px dashed var(--ln2);
		padding-left: 10px;
	}
	.unl {
		padding: 5px 0;
		color: var(--mut);
	}
</style>
