<script lang="ts">
	import { lore } from '$lib/lore';
	import type { Selection } from '$lib/FlowGraph.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { esc } from '$lib/system/html';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		ABORT,
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
	function renameBinding(s: DocStage, field: 'input' | 'output', from: string, to: string) {
		const cur = s[field];
		if (!cur || from === to) return;
		const rec: Record<string, string> = {};
		for (const [k, v] of Object.entries(cur)) rec[k === from ? to : k] = v;
		s[field] = rec;
		onchange();
	}
	function addBinding(s: DocStage, field: 'input' | 'output') {
		const cur = { ...(s[field] ?? {}) };
		let n = 1;
		while (cur[`file_${n}`] != null) n++;
		cur[`file_${n}`] = '';
		s[field] = cur;
		onchange();
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
		if (!newId || newId === oldId || newId === DONE || newId === ABORT) return;
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
	// fields: abort links carry no max/feed/then; stage links need a max, and
	// -1 survives only while a gate remains on the link.
	function retarget(sourceType: StageKind, link: DocEdge, to: string) {
		link.to = to;
		if (to === ABORT) {
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
		if (last.to === ABORT || last.max === -1) return; // nothing runs after abort/unlimited
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

<div class="insp">
	{#if stage}
		{@const s = stage}
		<div class="ih">
			<StageChip type={s.type} />
			<span class="mono iid ell">{s.id}</span>
			<span class="spacer"></span>
			<button class="btn-danger btn-sm" onclick={() => deleteStage(s.id)} title="Delete stage (⌫)"
				>Delete…</button
			>
		</div>
		<label for="st-id">Id <span class="muted">— renaming updates every reference</span></label>
		<input
			id="st-id"
			class="mono"
			value={s.id}
			onchange={(e) => renameStage(s.id, e.currentTarget.value)}
		/>
		{#if doc.phases?.length}
			<label for="st-phase"
				>Phase <span class="muted">— its routes stay inside it; the phase's last stage ends it</span
				></label
			>
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
		{/if}

		{#if s.type === 'agent'}
			<label for="st-agent"
				>Agent <span class="muted">— a name declared in this flow’s agents block</span></label
			>
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
			<label for="st-harness"
				>Harness <span class="muted"
					>— override the flow default; empty = {doc.defaults.harness || 'flow default'}</span
				></label
			>
			<input
				id="st-harness"
				bind:value={s.harness}
				oninput={onchange}
				placeholder={doc.defaults.harness || 'opencode'}
			/>
			<label for="st-model"
				>Model <span class="muted"
					>— override the flow default; empty = {doc.defaults.model || 'flow default'}</span
				></label
			>
			<input
				id="st-model"
				bind:value={s.model}
				oninput={onchange}
				placeholder={doc.defaults.model}
			/>
			<div class="row" style="gap:12px">
				<div style="flex:1">
					<label for="st-turns">Max turns</label>
					<input
						id="st-turns"
						type="number"
						value={s.max_turns ?? ''}
						oninput={(e) => {
							s.max_turns =
								e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
							onchange();
						}}
					/>
				</div>
				<div style="flex:1">
					<label for="st-timeout">Timeout</label>
					<input id="st-timeout" bind:value={s.timeout} oninput={onchange} placeholder="30m" />
				</div>
			</div>
			{#if declInputs.length > 0}
				<div class="fieldhdr" style="margin-bottom:2px">
					Inputs <span class="muted">— bind each declared input</span>
				</div>
				{#each declInputs as d (d.name)}
					{@const val = bindingValue(s, 'input', d.name)}
					<div class="io-bind">
						<div class="small">
							<span class="mono">{d.name}</span>
							{#if d.optional}<span class="muted">optional</span>{:else}<span class="req"
									>required</span
								>{/if}
							{#if d.description}<span class="muted">— {d.description}</span>{/if}
						</div>
						<div class="row" style="gap:6px; align-items:center">
							<input
								style="flex:1"
								list="agent-sentinels"
								placeholder="artifacts/plan.md"
								value={val}
								oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
							/>
						</div>
						{#if isFeedSentinel(val)}
							<p class="muted small" style="margin:2px 0 0">
								Filled by whichever edge brings the run back here — empty on the first attempt, so
								this input must be optional on the agent.
							</p>
						{/if}
					</div>
				{/each}
				<datalist id="agent-sentinels">
					{#each sentinelsFor('agent') as v (v)}<option value={v}></option>{/each}
				</datalist>
			{:else if s.agent}
				<p class="muted small" style="margin:6px 0 0">
					{agentDecl
						? `Agent “${s.agent}” declares no inputs — its prompt gets the task and nothing else.`
						: `Unknown agent “${s.agent}”: bind its inputs after it exists in the project's library.`}
				</p>
			{/if}

			{#if declOutputs.length > 0}
				<div class="fieldhdr" style="margin-bottom:2px">
					Outputs <span class="muted"
						>— declared by the agent; edit a path only to override it here</span
					>
				</div>
				{#each declOutputs as d (d.name)}
					<div class="io-bind">
						<div class="small">
							<span class="mono">{d.name}</span>
							{#if d.type === 'verdict'}<span class="req">verdict</span>{/if}
							{#if d.description}<span class="muted"> — {d.description}</span>{/if}
						</div>
						<input
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
				<p class="muted small" style="margin:4px 0 0">
					Renders a verdict, so this stage's <span class="mono">on_reject</span> fires on REJECT —
					and the target of that edge receives its comments as
					<span class="mono">{SENTINEL_DECISION}</span>.
				</p>
			{:else if s.agent && agentDecl && edgeOf(s, 'on_reject')}
				<p class="warn small" style="margin:4px 0 0">
					<span class="mono">on_reject</span> will never fire: agent “{s.agent}” declares no verdict
					output. Add one on the agent (Outputs → type <span class="mono">verdict</span>).
				</p>
			{/if}
		{:else if s.type === 'command'}
			<label for="st-runref"
				>run_ref <span class="muted">— a command name in the project running this flow</span></label
			>
			<input id="st-runref" bind:value={s.run_ref} oninput={onchange} placeholder="verify" />
			<label for="st-run">…or literal script</label>
			<textarea
				id="st-run"
				class="mono"
				bind:value={s.run}
				oninput={onchange}
				rows="3"
				placeholder="make test"></textarea>
			<div class="tgl">
				<Toggle
					checked={!!s.each_repo}
					label="Run in each repository"
					title="Multi-repo runs: run once per checkout instead of once in the primary (what git commit / push want)"
					onchange={(v) => {
						s.each_repo = v || undefined;
						onchange();
					}}
				/>
			</div>
			<div class="row spread" style="margin-top:8px">
				<div class="fieldhdr">
					Reads <span class="muted small">— checked before the script runs</span>
				</div>
				<button type="button" class="btn-sm" onclick={() => addBinding(s, 'input')}>+ Add</button>
			</div>
			{#each ioEntries(s.input) as [name, value] (name)}
				<div class="row io-row">
					<input
						class="mono"
						style="flex:0 0 120px"
						value={name}
						onchange={(e) => renameBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<input
						style="flex:1"
						{value}
						placeholder="artifacts/commit.md"
						oninput={(e) => setBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<button
						type="button"
						class="btn-icon"
						aria-label="Remove {name}"
						onclick={() => setBinding(s, 'input', name, '')}>×</button
					>
				</div>
			{/each}
			<div class="row spread" style="margin-top:8px">
				<div class="fieldhdr">
					Writes <span class="muted small">— must exist when the script exits 0</span>
				</div>
				<button type="button" class="btn-sm" onclick={() => addBinding(s, 'output')}>+ Add</button>
			</div>
			{#each ioEntries(s.output) as [name, value] (name)}
				<div class="row io-row">
					<input
						class="mono"
						style="flex:0 0 120px"
						value={name}
						onchange={(e) => renameBinding(s, 'output', name, e.currentTarget.value.trim())}
					/>
					<input
						style="flex:1"
						{value}
						placeholder="artifacts/report.txt"
						oninput={(e) => setBinding(s, 'output', name, e.currentTarget.value.trim())}
					/>
					<button
						type="button"
						class="btn-icon"
						aria-label="Remove {name}"
						onclick={() => setBinding(s, 'output', name, '')}>×</button
					>
				</div>
			{/each}
			<p class="muted small" style="margin:4px 0 0">
				The name is a label — it documents what each file is for and titles its dot in the graph.
			</p>
		{:else if s.type === 'human_gate'}
			<label for="st-question"
				>Question <span class="muted"
					>— what the human is being asked; approve continues, reject follows on_reject</span
				></label
			>
			<textarea
				id="st-question"
				bind:value={s.question}
				oninput={onchange}
				rows="2"
				placeholder="Approve this implementation plan?"></textarea>
			<div class="fieldhdr" style="margin:8px 0 2px">
				Shows <span class="muted small">— one artifact to review, or a link</span>
			</div>
			{#each ioEntries(s.input) as [name, value] (name)}
				<div class="row io-row">
					<input
						class="mono"
						style="flex:0 0 120px"
						value={name}
						onchange={(e) => renameBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<input
						style="flex:1"
						list="gate-sentinels"
						{value}
						placeholder="artifacts/plan.md"
						oninput={(e) => setBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<button
						type="button"
						class="btn-icon"
						aria-label="Remove {name}"
						onclick={() => setBinding(s, 'input', name, '')}>×</button
					>
				</div>
			{/each}
			{#if ioEntries(s.input).length === 0}
				<button type="button" class="btn-sm" onclick={() => addBinding(s, 'input')}
					>+ Add what it shows</button
				>
			{/if}
			<datalist id="gate-sentinels">
				{#each sentinelsFor('human_gate') as v (v)}<option value={v}></option>{/each}
			</datalist>
			<p class="muted small" style="margin:2px 0 0">
				<span class="mono">$WORKSPACE</span> hands over the terminal/IDE links;
				<span class="mono">$MR_URL</span> the merge request opened by a
				<span class="mono">create_mr</span> action.
			</p>
			<div class="fieldhdr" style="margin:8px 0 2px">
				Answered automatically by <span class="muted small"
					>— signals that supply the same answer a human would</span
				>
			</div>
			<div class="row" style="gap:8px">
				<div style="flex:1">
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
							<option value={sig.slug} disabled={sig.slug === s.auto_reject}>{sig.title}</option>
						{/each}
					</select>
				</div>
				<div style="flex:1">
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
							<option value={sig.slug} disabled={sig.slug === s.auto_approve}>{sig.title}</option>
						{/each}
					</select>
				</div>
			</div>
			{#if s.auto_approve || s.auto_reject}
				<p class="muted small" style="margin:2px 0 0">
					The gate keeps accepting a manual answer the whole time; whichever lands first wins.
				</p>
			{/if}
		{:else if s.type === 'action'}
			<label for="st-action"
				>Action <span class="muted">— a built-in apothecary integration</span></label
			>
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
			{#if actionSig}
				{#if actionSig.description}<p class="muted small" style="margin:2px 0 0">
						{actionSig.description}
					</p>{/if}
				{#if actionSig.requires}
					<p class="muted small" style="margin:2px 0 0">
						Needs a <span class="mono">{actionSig.requires}</span> integration on the project running
						this flow.
					</p>
				{/if}
				{#each actionSig.inputs ?? [] as d (d.name)}
					<div class="io-bind">
						<div class="small">
							<span class="mono">{d.name}</span>
							{#if d.required}<span class="req">required</span>{:else}<span class="muted"
									>optional</span
								>{/if}
							{#if d.description}<span class="muted">— {d.description}</span>{/if}
						</div>
						{#if d.kind === 'enum'}
							<select
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
								value={bindingValue(s, 'input', d.name)}
								placeholder={d.defaultPath ?? ''}
								oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
							/>
						{/if}
					</div>
				{/each}
				{#each actionSig.outputs ?? [] as d (d.name)}
					<p class="muted small" style="margin:4px 0 0">
						Produces <span class="mono">{d.name}</span>{#if d.sentinel}, available to later stages
							as
							<span class="mono">{d.sentinel}</span>{/if}{#if d.path}
							at <span class="mono">{d.path}</span>{/if}{#if d.description}
							— {d.description}{/if}.
					</p>
				{/each}
			{:else if s.action}
				<p class="warn small" style="margin:4px 0 0">Unknown action “{s.action}”.</p>
			{/if}
		{:else if s.type === 'wait'}
			<label for="st-for"
				>Wait for <span class="muted">— an external signal; the run parks until it fires</span
				></label
			>
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
			{#if waitSig}
				{#if waitSig.description}<p class="muted small" style="margin:2px 0 0">
						{waitSig.description}
					</p>{/if}
				{#if waitSig.requires}
					<p class="muted small" style="margin:2px 0 0">
						Needs a <span class="mono">{waitSig.requires}</span> integration on the project running this
						flow.
					</p>
				{/if}
			{:else if s.for}
				<p class="warn small" style="margin:4px 0 0">Unknown signal “{s.for}”.</p>
			{/if}
			<label for="st-wait-timeout"
				>Timeout <span class="muted">— empty waits indefinitely</span></label
			>
			<input id="st-wait-timeout" bind:value={s.timeout} oninput={onchange} placeholder="24h" />
		{/if}

		{#if freeBindings.length > 0 && (s.type === 'agent' || s.type === 'action')}
			<div class="fieldhdr" style="margin:8px 0 2px">Unrecognized bindings</div>
			{#each freeBindings as [name, value] (name)}
				<div class="row io-row">
					<input
						class="mono"
						style="flex:0 0 120px"
						value={name}
						onchange={(e) => renameBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<input
						style="flex:1"
						{value}
						oninput={(e) => setBinding(s, 'input', name, e.currentTarget.value.trim())}
					/>
					<button
						type="button"
						class="btn-icon"
						aria-label="Remove {name}"
						onclick={() => setBinding(s, 'input', name, '')}>×</button
					>
				</div>
			{/each}
			<p class="warn small" style="margin:2px 0 0">
				Not declared by this stage's {s.type === 'agent' ? 'agent' : 'action'} — run creation will refuse
				it.
			</p>
		{/if}

		<label for="st-desc"
			>Description <span class="muted">— optional note on what this stage is for</span></label
		>
		<input
			id="st-desc"
			bind:value={s.description}
			oninput={onchange}
			placeholder="implement the approved plan"
		/>

		<label for="st-succ">On success</label>
		<select id="st-succ" bind:value={s.on_success} {onchange}>
			<option value={DONE}>done (complete the run)</option>
			{#each stageIds.filter((id) => id !== s.id) as id (id)}<option value={id}>{id}</option>{/each}
		</select>

		<div class="row" style="margin-top:10px; gap:8px; flex-wrap:wrap">
			{#each edgeKindsFor(s.type) as kind (kind)}
				{#if !edgeOf(s, kind)}
					<button class="btn-sm ekb {kind}" onclick={() => addEdge(s, kind)}>+ {kind}</button>
				{:else}
					<button
						class="btn-sm btn-sel"
						onclick={() => onselect({ kind: 'edge', stage: s.id, edge: kind })}>{kind} →</button
					>
				{/if}
			{/each}
		</div>
		{#if s.type === 'agent' || s.type === 'command'}
			<p class="muted small" style="margin:6px 0 0">
				Timeouts route via on_timeout when declared, else fall back to on_fail.
			</p>
		{/if}
	{:else if edgeSel?.stage}
		{@const s = edgeSel.stage}
		{@const kind = edgeSel.kind}
		{@const e = edgeOf(s, kind)}
		{#if e}
			{@const links = chainLinks(e)}
			<div class="ih">
				<span class="ek {kind}">{kind}</span>
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
			<p class="muted small" style="margin:0 0 8px">
				This edge carries <span class="mono">{feedSentinelFor(kind)}</span>. It lands wherever the
				target stage binds that value as an input — bind it there, or the retry runs blind.
			</p>
			{#each links as link, depth (depth)}
				<div class="link" class:fallback={depth > 0}>
					{#if depth > 0}<div class="small muted" style="margin-bottom:2px">
							then (once the link above is exhausted)
						</div>{/if}
					<label for="ed-to-{depth}">To</label>
					<select
						id="ed-to-{depth}"
						value={link.to}
						onchange={(ev) => retarget(s.type, link, ev.currentTarget.value)}
					>
						{#each stageIds as id (id)}<option value={id}>{id}</option>{/each}
						<option value={ABORT}>abort — stop the run deliberately</option>
					</select>
					{#if link.to !== ABORT}
						{@const canUnlimit = unlimitedOK(s.type, link.to)}
						{#if canUnlimit}
							<label class="row small" style="gap:6px; margin-top:6px">
								<input
									type="checkbox"
									style="width:auto"
									checked={link.max === -1}
									onchange={(ev) => {
										link.max = ev.currentTarget.checked ? -1 : 3;
										if (link.max === -1) link.then = undefined;
										onchange();
									}}
								/>
								∞ no limit — every firing costs a human decision{targetsGate(link.to)
									? '; rejecting the gate ends the run'
									: ''}
							</label>
						{/if}
						<label for="ed-max-{depth}"
							>Max <span class="muted"
								>— times this link may fire{depth === 0 && !link.then
									? ' before the run is abandoned'
									: ''}{canUnlimit ? ' · -1 = ∞ (unlimited)' : ''}</span
							></label
						>
						<input
							id="ed-max-{depth}"
							type="number"
							min={canUnlimit ? -1 : 1}
							value={link.max ?? 1}
							oninput={(ev) => {
								const v = Math.floor(Number(ev.currentTarget.value));
								link.max = canUnlimit && v === -1 ? -1 : Math.max(1, v || 1);
								if (link.max === -1) link.then = undefined;
								onchange();
							}}
						/>
					{/if}
				</div>
			{/each}
			<div class="row" style="margin-top:10px; gap:8px">
				{#if links[links.length - 1].to !== ABORT && links[links.length - 1].max !== -1}
					<button class="btn-sm" onclick={() => appendLink(e)}>+ then (fallback)</button>
				{/if}
				<button class="btn-sm btn-ghost" onclick={() => dropLink(s, kind, e)}
					>− remove last link</button
				>
			</div>
			<p class="muted small" style="margin:8px 0 0">
				Links fire top-down: each keeps its own counter; when one is exhausted the next failure
				falls through. An exhausted chain abandons the run.
			</p>
		{/if}
	{:else}
		<div class="ih">
			<span class="serif ttl">{lore('flow')}</span>
			<span class="spacer"></span>
			<span class="fnt tiny">nothing selected</span>
		</div>
		<label for="fl-desc">Description</label>
		<input
			id="fl-desc"
			value={doc.description ?? ''}
			oninput={(e) => {
				doc.description = e.currentTarget.value || undefined;
				onchange();
			}}
		/>
		<label for="fl-harness"
			>Default harness <span class="muted"
				>— the harness kind stages run on unless they override it</span
			></label
		>
		<input
			id="fl-harness"
			bind:value={doc.defaults.harness}
			oninput={onchange}
			placeholder="opencode"
		/>
		<label for="fl-model">Default model</label>
		<input
			id="fl-model"
			bind:value={doc.defaults.model}
			oninput={onchange}
			placeholder="openrouter/anthropic/claude-sonnet-4.6"
		/>
		<div class="row" style="gap:12px">
			<div style="flex:1">
				<label for="fl-timeout">Default timeout</label>
				<input
					id="fl-timeout"
					bind:value={doc.defaults.timeout}
					oninput={onchange}
					placeholder="30m"
				/>
			</div>
			<div style="flex:1">
				<label for="fl-turns">Default max turns</label>
				<input
					id="fl-turns"
					type="number"
					value={doc.defaults.max_turns ?? ''}
					oninput={(e) => {
						doc.defaults.max_turns =
							e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
						onchange();
					}}
				/>
			</div>
		</div>
		<div class="row" style="gap:12px">
			<div style="flex:1">
				<label for="fl-cost">Budget: max cost (USD)</label>
				<input
					id="fl-cost"
					type="number"
					step="0.5"
					value={doc.budget?.max_cost_usd ?? ''}
					oninput={(e) => {
						doc.budget.max_cost_usd =
							e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
						onchange();
					}}
				/>
			</div>
			<div style="flex:1">
				<label for="fl-tturns">Budget: max total turns</label>
				<input
					id="fl-tturns"
					type="number"
					value={doc.budget?.max_total_turns ?? ''}
					oninput={(e) => {
						doc.budget.max_total_turns =
							e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
						onchange();
					}}
				/>
			</div>
		</div>
		<div class="sec">Add a stage</div>
		<div class="adds">
			<button class="btn-sm add az" onclick={() => addStage('agent')}>+ agent</button>
			<button class="btn-sm add grn" onclick={() => addStage('command')}>+ command</button>
			<button class="btn-sm add amb" onclick={() => addStage('human_gate')}>+ gate</button>
			<button class="btn-sm add vio" onclick={() => addStage('action')}>+ action</button>
			<button class="btn-sm add tea" onclick={() => addStage('wait')}>+ wait</button>
		</div>
		<p class="muted small" style="margin-top:12px">
			Click a node or edge to edit it. Drag from a node's coloured handles to rewire control flow:
			green = success, rose = on_fail, teal = on_timeout, amber = on_reject. Edges may target the
			abort terminal to stop the run deliberately. The faint dashed lines are dataflow — they are
			derived from the artifact paths stages read and write, so you change them by editing an input
			or output, not by dragging.
		</p>
	{/if}
</div>

<style>
	/* The inspector sits on control glass, given by the page (DESIGN 7.8). */
	.insp {
		display: flex;
		flex-direction: column;
		padding: 12px 14px 16px;
		font-size: 12.5px;
	}
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		padding-bottom: 10px;
		margin-bottom: 2px;
		border-bottom: 1px solid var(--ln);
		min-width: 0;
	}
	.iid {
		font-size: 13px;
		font-weight: 600;
	}
	.ttl {
		font-size: 19px;
	}
	.insp :global(label) {
		margin: 12px 0 4px;
	}
	.insp :global(label .muted) {
		display: block;
		font-weight: 400;
		font-size: 11px;
		margin-top: 1px;
	}
	.insp :global(input),
	.insp :global(select),
	.insp :global(textarea) {
		font-size: 12.5px;
		padding: 6px 9px;
	}
	.insp :global(input.mono),
	.insp :global(textarea.mono) {
		font-family: var(--mono) !important;
		font-size: 12px !important;
	}
	.sec {
		margin: 16px 0 6px;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--fnt);
	}
	.adds {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.add.az {
		color: var(--az);
	}
	.add.grn {
		color: var(--grn);
	}
	.add.amb {
		color: var(--amb);
	}
	.add.vio {
		color: var(--vio);
	}
	.add.tea {
		color: var(--tea);
	}
	.ek {
		font-family: var(--mono);
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		border: 1px solid;
	}
	.ek.on_fail,
	.ekb.on_fail {
		color: var(--ros);
		border-color: var(--rosLn);
	}
	.ek.on_timeout,
	.ekb.on_timeout {
		color: var(--tea);
		border-color: var(--teaLn);
	}
	.ek.on_reject,
	.ekb.on_reject {
		color: var(--amb);
		border-color: var(--ambLn);
	}
	.ek.on_fail {
		background: var(--rosBg);
	}
	.ek.on_timeout {
		background: var(--teaBg);
	}
	.ek.on_reject {
		background: var(--ambBg);
	}
	.tgl {
		margin-top: 10px;
	}
	.link.fallback {
		border-left: 2px dashed var(--ln2);
		padding-left: 10px;
		margin-top: 12px;
	}
	.io-bind {
		margin-top: 6px;
		padding: 7px 9px;
		background: var(--p2);
		border: 1px solid var(--ln);
		border-radius: 10px;
		display: flex;
		flex-direction: column;
		gap: 5px;
	}
	.io-row {
		margin-top: 6px;
		flex-wrap: nowrap;
		gap: 6px;
	}
	.req {
		color: var(--amb);
		font-size: 11px;
	}
	.warn {
		color: var(--amb);
	}
	.fieldhdr {
		font-size: 12px;
		font-weight: 500;
		color: var(--tx);
		margin: 14px 0 4px;
	}
</style>
