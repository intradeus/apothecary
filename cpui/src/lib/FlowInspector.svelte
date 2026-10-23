<script lang="ts">
	import type { Selection } from '$lib/FlowGraph.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import Stepper from '$lib/ui/Stepper.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { EDGE_HUE, STAGE_HUE, STAGE_WORD } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { ms } from '$lib/ui/motion';
	import { fade } from 'svelte/transition';
	import BindingRows from '$lib/flow/BindingRows.svelte';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import OptionalNumber from '$lib/ui/OptionalNumber.svelte';
	import ParamsEditor from '$lib/flow/ParamsEditor.svelte';
	import { boolParams, intParams, isParamRef, policyParams, stringParams } from '$lib/flow/params';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		ABORT,
		DEBUG,
		DONE,
		END,
		isReservedTarget,
		START,
		MANUAL,
		RUN_START,
		START_POLICIES,
		fires,
		paramDefault,
		chainSummary,
		routableFrom,
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
	// A trigger starts a phase; it can neither end a wait nor answer a gate
	// (the parser refuses it there), so the two pickers never offer the other's.
	const waitable = $derived(signals.filter((g) => !g.trigger && !g.entry));
	// What opens a phase on its own: a forge trigger (manual is a person's
	// act, offered apart) — or an outcome, which opens once and ends the run.
	const triggers = $derived(signals.filter((g) => g.trigger && g.slug !== MANUAL));
	const outcomes = $derived(signals.filter((g) => g.outcome));
	const WHEN_TERMS = ['tracker', 'not tracker', 'subject', 'not subject'];

	const phaseSel = $derived(
		selection?.kind === 'phase' ? doc.phases?.find((p) => p.id === selection.id) : undefined
	);
	const phaseSig = $derived(
		phaseSel ? signals.find((g) => g.slug === phaseSel.on.signal) : undefined
	);
	const phaseStageCount = $derived(
		phaseSel ? doc.stages.filter((x) => x.phase === phaseSel.id).length : 0
	);

	// renamePhase renames a phase and every stage that belongs to it.
	function renamePhase(oldId: string, newId: string) {
		newId = newId.trim();
		const ph = doc.phases?.find((p) => p.id === oldId);
		if (!ph || !newId || newId === oldId || !/^[a-z][a-z0-9_-]*$/.test(newId)) return;
		if (doc.phases?.some((p) => p.id === newId)) return;
		ph.id = newId;
		for (const s of doc.stages) if (s.phase === oldId) s.phase = newId;
		onchange();
		onselect({ kind: 'phase', id: newId });
	}

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
				: selection?.kind === 'phase'
					? `p:${selection.id}`
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
		if (
			!newId ||
			newId === oldId ||
			[END, DONE, ABORT, DEBUG, START].includes(newId) ||
			!/^[a-z][a-z0-9_-]*$/.test(newId)
		)
			return;
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
			title: t('flow.insp.deleteStageTitle', { id }),
			body: [
				th('flow.insp.deleteBody', { id }),
				refs ? th('flow.insp.deleteRewired', { count: refs }) : '',
				t('flow.insp.deleteNote')
			]
				.filter(Boolean)
				.join(' '),
			action: t('flow.insp.deleteAction')
		});
		if (!ok) return;
		doc.stages = doc.stages.filter((s) => s.id !== id);
		for (const s of doc.stages) {
			if (s.on_success === id) s.on_success = END;
			for (const kind of EDGE_KINDS) {
				setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
			}
		}
		onchange();
		onselect(null);
	}

	// A new edge starts somewhere legal: a stage of the same phase (a route
	// may not leave it). A failure or timeout retries the stage itself; a
	// gate's rejection goes back to whatever leads into the gate.
	function addEdge(s: NonNullable<typeof stage>, kind: EdgeKind) {
		const mine = routableFrom(doc, s);
		const feeder = mine.find((x) => x.on_success === s.id && x.id !== s.id);
		const to = s.type === 'human_gate' ? (feeder ?? mine.find((x) => x.id !== s.id))?.id : s.id;
		setEdge(s, kind, to ? { to, max: 3 } : { to: DEBUG });
		onchange();
		onselect({ kind: 'edge', stage: s.id, edge: kind });
	}

	const targetsGate = (id: string) => doc.stages.find((x) => x.id === id)?.type === 'human_gate';
	// Unlimited (-1) needs a human on the link: gate target (each firing
	// parks on a decision) or gate source (the link only fires on reject).
	const unlimitedOK = (sourceType: StageKind, to: string) =>
		sourceType === 'human_gate' || targetsGate(to);

	// retarget switches one chain link's target, normalizing the link's other
	// fields: links to a reserved target (end, done, abort, debug) carry no
	// max/then; stage links need a max, and -1 survives only while a gate
	// remains on the link.
	function retarget(sourceType: StageKind, link: DocEdge, to: string) {
		link.to = to;
		if (isReservedTarget(to)) {
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

	const maxHelp = (depth: number, then: unknown, canUnlimit: boolean) => {
		const abandon = depth === 0 && !then;
		return t(
			abandon
				? canUnlimit
					? 'flow.insp.maxHelpAbandonUnl'
					: 'flow.insp.maxHelpAbandon'
				: canUnlimit
					? 'flow.insp.maxHelpUnl'
					: 'flow.insp.maxHelp'
		);
	};

	// What an action output produces: its name, then whichever of sentinel,
	// path and description it declares.
	const producesHtml = (d: {
		name: string;
		sentinel?: string;
		path?: string;
		description?: string;
	}) =>
		th('flow.insp.produces', { name: d.name }) +
		(d.sentinel ? th('flow.insp.producesSentinel', { sentinel: d.sentinel }) : '') +
		(d.path ? th('flow.insp.producesPath', { path: d.path }) : '') +
		(d.description ? th('flow.insp.producesDesc', { description: d.description }) : '') +
		'.';

	// appendLink adds a fallback ("then") after the chain's last link.
	function appendLink(e: DocEdge) {
		let last = e;
		while (last.then) last = last.then;
		if (isReservedTarget(last.to) || last.max === -1) return; // nothing follows a reserved target or an unlimited link
		// The usual fallback once retries are spent: a human with the workspace.
		last.then = { to: DEBUG };
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
						title={t('flow.insp.deleteTitle')}>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="field">
					<label for="st-id">{t('flow.insp.id')}</label>
					<input
						id="st-id"
						class="mono"
						value={s.id}
						onchange={(e) => renameStage(s.id, e.currentTarget.value)}
					/>
					<div class="help">{t('flow.insp.renameHelp')}</div>
				</div>
				{#if doc.phases?.length}
					<div class="field">
						<label for="st-phase">{t('flow.insp.phase')}</label>
						<select
							id="st-phase"
							value={s.phase}
							onchange={(e) => {
								movePhase(doc, s.id, e.currentTarget.value);
								onchange();
							}}
						>
							{#each doc.phases as ph (ph.id)}
								<option value={ph.id}
									>{t('flow.insp.phaseOption', { id: ph.id, trigger: triggerLabel(ph.on) })}</option
								>
							{/each}
						</select>
						<div class="help">{t('flow.insp.phaseHelp')}</div>
					</div>
				{/if}

				<div class="field">
					<label for="st-when">{t('flow.insp.when')}</label>
					<select
						id="st-when"
						value={s.when ?? ''}
						onchange={(e) => {
							s.when = e.currentTarget.value || undefined;
							onchange();
						}}
					>
						<option value="">{t('flow.insp.when.none')}</option>
						<option value="tracker">{t('flow.insp.when.tracker')}</option>
						<option value="not tracker">{t('flow.insp.when.notTracker')}</option>
						<option value="subject">{t('flow.insp.when.subject')}</option>
						<option value="not subject">{t('flow.insp.when.notSubject')}</option>
						{#each boolParams(doc) as p (p)}
							<option value={'$' + p}>${p}</option>
							<option value={'not $' + p}>not ${p}</option>
						{/each}
						{#if s.when && !WHEN_TERMS.includes(s.when) && !boolParams(doc).some((p) => s.when === '$' + p || s.when === 'not $' + p)}
							<option value={s.when}>{s.when}</option>
						{/if}
					</select>
					<div class="help">{t('flow.insp.whenHelp')}</div>
				</div>
				<datalist id="flow-params">
					{#each stringParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>

				{#if s.type === 'agent'}
					<div class="field">
						<label for="st-agent">{t('flow.insp.agent')}</label>
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
						<div class="help">{t('flow.insp.agentHelp')}</div>
					</div>
					<div class="field">
						<label for="st-harness">{t('flow.insp.harness')}</label>
						<input
							id="st-harness"
							list="flow-params"
							bind:value={s.harness}
							oninput={onchange}
							placeholder={paramDefault(doc, 'harness') || 'opencode'}
						/>
						<div class="help">
							{t('flow.insp.overrideDefault', {
								value: paramDefault(doc, 'harness') || t('flow.insp.flowDefault')
							})}
						</div>
					</div>
					<div class="field">
						<label for="st-model">{t('flow.insp.model')}</label>
						<input
							id="st-model"
							list="flow-params"
							bind:value={s.model}
							oninput={onchange}
							placeholder={paramDefault(doc, 'model')}
						/>
						<div class="help">
							{t('flow.insp.overrideDefault', {
								value: paramDefault(doc, 'model') || t('flow.insp.flowDefault')
							})}
						</div>
					</div>
					<div class="field">
						<label for="st-turns">{t('flow.insp.maxTurns')}</label>
						<input
							id="st-turns"
							class="mono"
							list="flow-int-params"
							value={s.max_turns ?? ''}
							placeholder={paramDefault(doc, 'max_turns') ?? '50'}
							oninput={(e) => {
								const v = e.currentTarget.value.trim();
								s.max_turns = v === '' ? undefined : /^\d+$/.test(v) ? Number(v) : v;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.numberOrParam')}</div>
					</div>
					<div class="field">
						<label for="st-timeout">{t('flow.insp.timeout')}</label>
						<input
							id="st-timeout"
							class="mono"
							list="flow-params"
							bind:value={s.timeout}
							oninput={onchange}
							placeholder={paramDefault(doc, 'timeout') ?? '30m'}
						/>
						<div class="help">{t('flow.insp.durationOrParam')}</div>
					</div>
					{#if declInputs.length > 0}
						<div class="grp">
							<div class="lbl">{t('flow.insp.inputs')}</div>
							<div class="help">{t('flow.insp.inputsHelp')}</div>
						</div>
						{#each declInputs as d (d.name)}
							{@const val = bindingValue(s, 'input', d.name)}
							<div class="io-bind card-in field">
								<label for="in-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.optional}<span class="fnt">{t('flow.insp.optional')}</span>{:else}<span
											class="hue-amb">{t('flow.insp.required')}</span
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
									<div class="help">{t('flow.insp.feedHelp')}</div>
								{/if}
							</div>
						{/each}
						<datalist id="agent-sentinels">
							{#each sentinelsFor('agent') as v (v)}<option value={v}></option>{/each}
						</datalist>
					{:else if s.agent}
						<div class="help">
							{agentDecl
								? t('flow.insp.noInputs', { name: s.agent })
								: t('flow.insp.unknownAgent', { name: s.agent })}
						</div>
					{/if}

					{#if declOutputs.length > 0}
						<div class="grp">
							<div class="lbl">{t('flow.insp.outputs')}</div>
							<div class="help">{t('flow.insp.outputsHelp')}</div>
						</div>
						{#each declOutputs as d (d.name)}
							<div class="io-bind card-in field">
								<label for="out-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.type === 'verdict'}<span class="pill sm t-vio"
											>{t('flow.insp.verdict')}</span
										>{/if}</label
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
							{@html th('flow.insp.verdictHelp', { sentinel: SENTINEL_DECISION })}
						</div>
					{:else if s.agent && agentDecl && edgeOf(s, 'on_reject')}
						<div class="help hue-amb">{@html th('flow.insp.neverFires', { agent: s.agent })}</div>
					{/if}
				{:else if s.type === 'command'}
					<div class="field">
						<label for="st-runref">run_ref</label>
						<input
							id="st-runref"
							class="mono"
							list="flow-params"
							bind:value={s.run_ref}
							oninput={onchange}
							placeholder="verify"
						/>
						<div class="help">{t('flow.insp.commandHelp')}</div>
					</div>
					<div class="field">
						<label for="st-run">{t('flow.insp.literalScript')}</label>
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
						label={t('flow.insp.eachRepo')}
						title={t('flow.insp.eachRepoTitle')}
						onchange={(v) => {
							s.each_repo = v || undefined;
							onchange();
						}}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.reads')}</div>
						<div class="help">{t('flow.insp.readsHelp')}</div>
					</div>
					<BindingRows stage={s} field="input" valuePlaceholder="artifacts/commit.md" {onchange} />
					<div class="grp">
						<div class="lbl">{t('flow.insp.writes')}</div>
						<div class="help">{t('flow.insp.writesHelp')}</div>
					</div>
					<BindingRows
						stage={s}
						field="output"
						valuePlaceholder="artifacts/report.txt"
						{onchange}
					/>
					<div class="help">{t('flow.insp.nameLabelHelp')}</div>
				{:else if s.type === 'human_gate'}
					<div class="field">
						<label for="st-question">{t('flow.insp.question')}</label>
						<textarea
							id="st-question"
							bind:value={s.question}
							oninput={onchange}
							rows="2"
							placeholder={t('flow.insp.questionPlaceholder')}></textarea>
						<div class="help">{t('flow.insp.questionHelp')}</div>
					</div>
					<div class="grp">
						<div class="lbl">{t('flow.insp.shows')}</div>
						<div class="help">{@html th('flow.insp.showsHelp')}</div>
					</div>
					<BindingRows
						stage={s}
						field="input"
						valuePlaceholder="artifacts/plan.md"
						addLabel={t('flow.insp.addShows')}
						{onchange}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.autoBy')}</div>
						<div class="help">{t('flow.insp.autoByHelp')}</div>
					</div>
					<div class="grid2">
						<div class="field">
							<label for="st-auto-ok">{t('flow.insp.approveWhen')}</label>
							<select
								id="st-auto-ok"
								value={s.auto_approve ?? ''}
								onchange={(e) => {
									s.auto_approve = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each waitable as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_reject}>{sig.title}</option
									>
								{/each}
							</select>
						</div>
						<div class="field">
							<label for="st-auto-no">{t('flow.insp.rejectWhen')}</label>
							<select
								id="st-auto-no"
								value={s.auto_reject ?? ''}
								onchange={(e) => {
									s.auto_reject = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each waitable as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_approve}
										>{sig.title}</option
									>
								{/each}
							</select>
						</div>
					</div>
					{#if s.auto_approve || s.auto_reject}
						<div class="help">{t('flow.insp.manualStillOk')}</div>
					{/if}
				{:else if s.type === 'action'}
					<div class="field">
						<label for="st-action">{t('flow.insp.action')}</label>
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
						<div class="help">{t('flow.insp.actionHelp')}</div>
					</div>
					{#if actionSig}
						{#if actionSig.description}<div class="help">{actionSig.description}</div>{/if}
						{#if actionSig.requires === 'tracker'}
							<div class="help">{t('flow.insp.tracker')}</div>
						{:else if actionSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: actionSig.requires })}
							</div>
						{/if}
						{#if actionSig.needs === 'merge_request'}
							<div class="help">{t('flow.insp.needsMR')}</div>
						{/if}
						{#if actionSig.exactlyOne?.length}
							<div class="help">
								{t('flow.insp.exactlyOne', { names: actionSig.exactlyOne.join(' / ') })}
							</div>
						{/if}
						{#each actionSig.inputs ?? [] as d (d.name)}
							<div class="io-bind card-in field">
								<label for="act-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.required}<span class="hue-amb">{t('flow.insp.required')}</span>{:else}<span
											class="fnt">{t('flow.insp.optional')}</span
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
										{#if !d.required && !d.defaultValue}<option value="">—</option>{/if}
										{#each d.values ?? [] as v (v)}<option value={v}>{v}</option>{/each}
									</select>
								{:else if d.kind === 'text'}
									<input
										id="act-{d.name}"
										value={bindingValue(s, 'input', d.name)}
										placeholder={t('flow.insp.textPlaceholder')}
										oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
									/>
									<div class="help">{t('flow.insp.textInputHelp')}</div>
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
							<div class="help">{@html producesHtml(d)}</div>
						{/each}
					{:else if s.action}
						<div class="help hue-amb">{t('flow.insp.unknownAction', { name: s.action })}</div>
					{/if}
				{:else if s.type === 'wait'}
					<div class="field">
						<label for="st-for">{t('flow.insp.waitFor')}</label>
						<select
							id="st-for"
							value={s.for ?? ''}
							onchange={(e) => {
								s.for = e.currentTarget.value || undefined;
								onchange();
							}}
						>
							<option value="">—</option>
							{#each waitable as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
						</select>
						<div class="help">{t('flow.insp.waitHelp')}</div>
					</div>
					{#if waitSig}
						{#if waitSig.description}<div class="help">{waitSig.description}</div>{/if}
						{#if waitSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: waitSig.requires })}
							</div>
						{/if}
					{:else if s.for}
						<div class="help hue-amb">{t('flow.insp.unknownSignal', { name: s.for })}</div>
					{/if}
					<div class="field">
						<label for="st-wait-timeout">{t('flow.insp.timeout')}</label>
						<OptionalDuration
							id="st-wait-timeout"
							label={t('flow.insp.timeout')}
							bind:value={s.timeout}
							empty={t('flow.insp.waitsForever')}
							fallback="24h"
							{onchange}
						/>
					</div>
				{/if}

				{#if (freeBindings.length > 0 || freeShown[s.id]) && (s.type === 'agent' || s.type === 'action')}
					<div class="grp">
						<div class="lbl">{t('flow.insp.unrecognized')}</div>
						<div class="help hue-amb">
							{s.type === 'agent'
								? t('flow.insp.unrecognizedAgent')
								: t('flow.insp.unrecognizedAction')}
						</div>
					</div>
					{#key s.type === 'agent' ? s.agent : s.action}
						<BindingRows stage={s} field="input" keep={isDeclared} {onchange} />
					{/key}
				{/if}

				<div class="field">
					<label for="st-desc">{t('flow.insp.description')}</label>
					<input
						id="st-desc"
						bind:value={s.description}
						oninput={onchange}
						placeholder={t('flow.insp.descPlaceholder')}
					/>
					<div class="help">{t('flow.insp.descHelp')}</div>
				</div>

				<div class="field">
					<label for="st-succ">{t('flow.insp.onSuccess')}</label>
					<select id="st-succ" bind:value={s.on_success} {onchange}>
						<option value={END}>{t('flow.insp.endOption')}</option>
						<option value={DONE}>{t('flow.insp.doneOption')}</option>
						{#each routableFrom(doc, s).filter((x) => x.id !== s.id) as x (x.id)}<option
								value={x.id}>{x.id}</option
							>{/each}
					</select>
					<div class="help">{t('flow.insp.endsHelp')}</div>
				</div>

				<div class="chips">
					{#each edgeKindsFor(s.type) as kind (kind)}
						{#if !edgeOf(s, kind)}
							<button class="btn-sm hue-{EDGE_HUE[kind]}" onclick={() => addEdge(s, kind)}
								>{I.add} {kind}</button
							>
						{:else}
							<button
								class="btn-sm btn-sel hue-{EDGE_HUE[kind]}"
								title={t('flow.insp.editEdge')}
								onclick={() => onselect({ kind: 'edge', stage: s.id, edge: kind })}
								>{kind}
								{I.forward} <span class="mono">{chainSummary(edgeOf(s, kind))}</span></button
							>
						{/if}
					{/each}
				</div>
				{#if s.type === 'agent' || s.type === 'command'}
					<div class="help">{t('flow.insp.timeoutsHelp')}</div>
				{/if}
			{:else if edgeSel?.stage}
				{@const s = edgeSel.stage}
				{@const kind = edgeSel.kind}
				{@const e = edgeOf(s, kind)}
				{#if e}
					{@const links = chainLinks(e)}
					<div class="ih">
						<span class="pill sm mono t-{EDGE_HUE[kind]}">{kind}</span>
						<span class="mut small">{t('flow.insp.from')}</span>
						<span class="mono iid ell">{s.id}</span>
						<span class="spacer"></span>
						<button
							class="btn-danger btn-sm"
							title={t('flow.insp.removeEdgeTitle')}
							onclick={() => {
								setEdge(s, kind, undefined);
								onchange();
								onselect({ kind: 'stage', id: s.id });
							}}>{t('flow.insp.removeEdge')}</button
						>
					</div>
					<div class="help">
						{@html th('flow.insp.edgeCarries', { sentinel: feedSentinelFor(kind) })}
					</div>
					{#each links as link, depth (depth)}
						<div class="link" class:fallback={depth > 0}>
							{#if depth > 0}<div class="help">{t('flow.insp.thenHelp')}</div>{/if}
							<div class="field">
								<label for="ed-to-{depth}">{t('flow.insp.to')}</label>
								<select
									id="ed-to-{depth}"
									value={link.to}
									onchange={(ev) => retarget(s.type, link, ev.currentTarget.value)}
								>
									{#each routableFrom(doc, s) as x (x.id)}<option value={x.id}>{x.id}</option
										>{/each}
									<option value={END}>{t('flow.insp.endOption')}</option>
									<option value={DONE}>{t('flow.insp.doneOption')}</option>
									<option value={ABORT}>{t('flow.insp.abortOption')}</option>
									<option value={DEBUG}>{t('flow.insp.debugOption')}</option>
								</select>
							</div>
							{#if !isReservedTarget(link.to)}
								{@const canUnlimit = unlimitedOK(s.type, link.to)}
								{#if canUnlimit}
									<Toggle
										checked={link.max === -1}
										label={t('flow.insp.noLimit')}
										title={targetsGate(link.to)
											? t('flow.insp.noLimitTitleGate')
											: t('flow.insp.noLimitTitle')}
										onchange={(v) => {
											link.max = v ? -1 : 3;
											if (link.max === -1) link.then = undefined;
											onchange();
										}}
									/>
								{/if}
								<div class="field">
									<label for="ed-max-{depth}">{t('flow.insp.max')}</label>
									{#if link.max === -1}
										<div class="mono unl">{t('flow.insp.unlimited')}</div>
									{:else}
										<Stepper
											id="ed-max-{depth}"
											label={t('flow.insp.max')}
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
									<div class="help">{maxHelp(depth, link.then, canUnlimit)}</div>
								</div>
							{/if}
						</div>
					{/each}
					<div class="row">
						{#if !isReservedTarget(links[links.length - 1].to) && links[links.length - 1].max !== -1}
							<button class="btn-sm" onclick={() => appendLink(e)}
								>{I.add} {t('flow.insp.thenFallback')}</button
							>
						{/if}
						<button class="btn-sm btn-ghost" onclick={() => dropLink(s, kind, e)}
							>{t('flow.insp.removeLast')}</button
						>
					</div>
					<div class="help">{t('flow.insp.linksHelp')}</div>
				{/if}
			{:else if phaseSel}
				{@const ph = phaseSel}
				{@const firing = fires(ph.on)}
				<div class="ih">
					<span class="pill sm mono t-vio">{t('flow.insp.phaseKind')}</span>
					<span class="mono iid ell">{ph.id}</span>
					{#if ph.on.signal === RUN_START}<span
							class="pill sm flat sq t-brand"
							title={t('flow.insp.entrypointTitle')}>{t('flow.insp.entrypoint')}</span
						>{:else if phaseSig?.outcome}<span
							class="pill sm flat sq t-grn"
							title={t('flow.insp.outcomeTitle')}>{t('flow.insp.outcome')}</span
						>{/if}
					<span class="spacer"></span>
					<span class="fnt tiny"
						>{phaseSig?.outcome && !phaseStageCount
							? t('flow.insp.phaseThen', { then: ph.then ?? DONE })
							: t('flow.insp.phaseStages', { count: phaseStageCount })}</span
					>
				</div>
				<div class="field">
					<label for="ph-id">{t('flow.insp.id')}</label>
					<input
						id="ph-id"
						class="mono"
						value={ph.id}
						onchange={(e) => renamePhase(ph.id, e.currentTarget.value)}
					/>
					<div class="help">{t('flow.insp.phaseRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="ph-desc">{t('flow.insp.description')}</label>
					<input
						id="ph-desc"
						value={ph.description ?? ''}
						oninput={(e) => {
							ph.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="field">
					<label for="ph-on">{t('flow.insp.opensOn')}</label>
					<select
						id="ph-on"
						value={ph.on.signal}
						onchange={(e) => {
							ph.on.signal = e.currentTarget.value;
							const sig = signals.find((g) => g.slug === ph.on.signal);
							if (!fires(ph.on)) {
								// A person's act is the start: no policy, nothing to debounce.
								ph.on.start = undefined;
								ph.on.quiet_for = undefined;
							} else if (!sig?.debounced) {
								ph.on.quiet_for = undefined;
							}
							if (sig?.outcome) {
								// An outcome fires once and ends the run: no cap, and a
								// stage-less one needs its `then`.
								ph.on.max = undefined;
								if (!phaseStageCount) ph.then ??= DONE;
							} else {
								ph.then = undefined;
							}
							onchange();
						}}
					>
						<option value={RUN_START}>{t('flow.insp.opensOn.run_start')}</option>
						<option value={MANUAL}>{t('flow.insp.opensOn.manual')}</option>
						{#each triggers as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
						<optgroup label={t('flow.insp.opensOn.outcomes')}>
							{#each outcomes as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
						</optgroup>
						{#if firing && !triggers.some((g) => g.slug === ph.on.signal) && !outcomes.some((g) => g.slug === ph.on.signal)}
							<option value={ph.on.signal}>{ph.on.signal}</option>
						{/if}
					</select>
					<div class="help">
						{ph.on.signal === RUN_START
							? t('flow.insp.opensOnHelp.run_start')
							: ph.on.signal === MANUAL
								? t('flow.insp.opensOnHelp.manual')
								: (phaseSig?.description ?? '')}
					</div>
				</div>
				{#if firing}
					<div class="field">
						<label for="ph-start">{t('flow.insp.start')}</label>
						<select
							id="ph-start"
							value={ph.on.start ?? 'auto'}
							onchange={(e) => {
								const v = e.currentTarget.value;
								ph.on.start = v === 'auto' ? undefined : v;
								onchange();
							}}
						>
							{#each START_POLICIES as p (p)}<option value={p}>{t(`flow.insp.start.${p}`)}</option
								>{/each}
							{#each policyParams(doc) as p (p)}<option value={'$' + p}>${p}</option>{/each}
							{#if isParamRef(ph.on.start) && !policyParams(doc).includes(ph.on.start.slice(1))}
								<option value={ph.on.start}>{ph.on.start}</option>
							{/if}
						</select>
						<div class="help">{t('flow.insp.startHelp')}</div>
					</div>
					{#if phaseSig?.debounced}
						<div class="field">
							<label for="ph-quiet">{t('flow.insp.quietFor')}</label>
							<OptionalDuration
								id="ph-quiet"
								label={t('flow.insp.quietFor')}
								bind:value={ph.on.quiet_for}
								empty={t('flow.insp.noQuiet')}
								fallback="1h"
								{onchange}
							/>
							<div class="help">{t('flow.insp.quietForHelp')}</div>
						</div>
					{/if}
				{/if}
				{#if phaseSig?.outcome}
					{#if !phaseStageCount}
						<div class="field">
							<label for="ph-then">{t('flow.insp.then')}</label>
							<select
								id="ph-then"
								value={ph.then ?? DONE}
								onchange={(e) => {
									ph.then = e.currentTarget.value;
									onchange();
								}}
							>
								<option value={DONE}>{t('flow.insp.then.done')}</option>
								<option value={ABORT}>{t('flow.insp.then.abort')}</option>
							</select>
							<div class="help">{t('flow.insp.phaseThenHelp')}</div>
						</div>
					{:else}
						<div class="help">{t('flow.insp.outcomeStagesHelp')}</div>
					{/if}
				{:else}
					<div class="field">
						<label for="ph-max">{t('flow.insp.phaseMax')}</label>
						<input
							id="ph-max"
							class="mono"
							list="flow-int-params"
							value={ph.on.max ?? ''}
							placeholder={t('flow.insp.unlimited')}
							oninput={(e) => {
								const v = e.currentTarget.value.trim();
								ph.on.max = v === '' ? undefined : /^-?\d+$/.test(v) ? Number(v) : v;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.phaseMaxHelp')}</div>
					</div>
				{/if}
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
			{:else}
				<div class="ih">
					<span class="serif ttl">{t('flow.insp.formula')}</span>
					<span class="spacer"></span>
					<span class="fnt tiny">{t('flow.insp.nothingSelected')}</span>
				</div>
				<div class="field">
					<label for="fl-desc">{t('flow.insp.description')}</label>
					<input
						id="fl-desc"
						value={doc.description ?? ''}
						oninput={(e) => {
							doc.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="lbl">{t('flow.insp.params')}</div>
				<div class="help">{t('flow.insp.paramsHelp')}</div>
				<ParamsEditor {doc} {onchange} />
				<div class="lbl">{t('flow.insp.addStage')}</div>
				<div class="chips">
					{#each ADD as kind (kind)}
						<button class="btn-sm" onclick={() => addStage(kind)}
							><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
								kind}</button
						>
					{/each}
				</div>
				<div class="help">{t('flow.insp.canvasHelp')}</div>
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
