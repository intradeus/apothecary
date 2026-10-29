<script lang="ts">
	import type { Selection } from '$lib/FlowGraph.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Select from '$lib/ui/Select.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/format';
	import { EDGE_HUE, STAGE_HUE, STAGE_WORD } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { ms } from '$lib/ui/motion';
	import { fade } from 'svelte/transition';
	import { untrack } from 'svelte';
	import PortRows from '$lib/flow/PortRows.svelte';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import OptionalNumber from '$lib/ui/OptionalNumber.svelte';
	import ParamsEditor from '$lib/flow/ParamsEditor.svelte';
	import KeyValueEditor, { type KVRow } from '$lib/ui/KeyValueEditor.svelte';
	import YamlEditor from '$lib/flow/YamlEditor.svelte';
	import { parse as parseYaml, stringify as stringifyYaml } from 'yaml';
	import {
		boolParams,
		enumParamsWithin,
		intParams,
		isParamRef,
		policyParams,
		stringParams,
		durationParams,
		stringListParams
	} from '$lib/flow/params';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		ABORT,
		DEBUG,
		FAIL,
		DONE,
		END,
		isReservedTarget,
		START,
		MAIN,
		MANUAL,
		OPERATES_ON,
		START_POLICIES,
		AGENT_NAME,
		SCHEMA_NAME,
		MCP_BUILTINS,
		MCP_KINDS,
		MCP_NAME,
		SERVICE_DOS,
		addAgent,
		addMCPServer,
		flowServices,
		mcpKind,
		mcpUsers,
		removeMCPServer,
		renameMCPServer,
		serviceSource,
		addSchema,
		allStages,
		addMainChain,
		addOutcome,
		addSequence,
		addWatcher,
		appendSequenceStage,
		appendStage,
		chainSequence,
		hasMain,
		moveSequenceStage,
		removePhase,
		removeSchema,
		removeSequence,
		renameSchema,
		renameSequence,
		schemaUsers,
		sequenceUsers,
		setChainSequence,
		stagesOf,
		fires,
		paramDefault,
		routableFrom,
		EDGE_KINDS,
		SOURCE_VERDICT,
		FILE_TYPES,
		GATE_TYPES,
		VALUE_TYPES,
		chainLinks,
		edgeKindsFor,
		edgeOf,
		feedSourceFor,
		pruneEdgeTarget,
		sourcesFor,
		phaseOf,
		setEdge,
		movePhase,
		triggerLabel,
		verdictOutput,
		givesVerdict,
		type DocEdge,
		type DocPhase,
		type DocStage,
		type EdgeKind,
		type DocAgent,
		type DocMCPServer,
		type DocPort,
		type DocSequence,
		type FlowDoc,
		type MCPKind,
		type ServiceDo,
		type StageKind
	} from '$lib/flowdoc';

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
	// The stages a stage may route to: its sequence's, or its chain's.
	const routable = (s: DocStage) => (scopeSeq ? scopeSeq.stages : routableFrom(doc, s));

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
	let routesEl = $state<HTMLElement | null>(null);
	$effect(() => {
		const kind = focusRoute;
		if (!kind || !routesEl) return;
		untrack(() =>
			routesEl
				?.querySelector(`[data-route="${kind}"]`)
				?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
		);
	});

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
	// Any signal observed on the run's merge requests may end a wait, answer
	// a gate, open a watcher or fire an outcome: the section decides what it
	// does (FLOWLANG §1). manual — a person's start — opens a watcher only.
	const observed = $derived(signals.filter((g) => g.kind === 'observed'));
	// A chain's kind in the marker's hue: main brand, watchers violet, an
	// outcome the end it says.
	const kindHue = (ph: DocPhase) =>
		ph.kind === 'main'
			? 'brand'
			: ph.kind === 'watcher'
				? 'vio'
				: ph.then === ABORT
					? 'ros'
					: 'grn';
	// The run facts a condition names by path (flow.CondTracker, CondSubject,
	// CondChangeType compared with a flow.CondChangeTypes entry).
	const CHANGE_TYPES = ['feat', 'fix', 'chore'];
	const RUN_IF_FACTS = () => [
		{ value: 'run.ticket.tracker', label: t('flow.insp.runIf.tracker') },
		{ value: 'not run.ticket.tracker', label: t('flow.insp.runIf.notTracker') },
		{ value: 'run.subject', label: t('flow.insp.runIf.subject') },
		{ value: 'not run.subject', label: t('flow.insp.runIf.notSubject') },
		...CHANGE_TYPES.flatMap((c) => [
			{ value: 'run.ticket.change_type == ' + c, label: t('flow.insp.runIf.change', { type: c }) },
			{
				value: 'run.ticket.change_type != ' + c,
				label: t('flow.insp.runIf.notChange', { type: c })
			}
		])
	];

	const phaseSel = $derived(
		selection?.kind === 'phase' ? doc.phases?.find((p) => p.id === selection.id) : undefined
	);
	const phaseSig = $derived(
		phaseSel ? signals.find((g) => g.slug === phaseSel.on.signal) : undefined
	);
	const phaseStageCount = $derived(
		phaseSel ? doc.stages.filter((x) => x.phase === phaseSel.id).length : 0
	);

	// renamePhase renames a watcher or an outcome and every stage that
	// belongs to it. The main chain is `main`. Like every rename here, it
	// says whether it took: a refused one puts the field back.
	function renamePhase(oldId: string, newId: string): boolean {
		newId = newId.trim();
		const ph = doc.phases?.find((p) => p.id === oldId);
		if (!ph || ph.kind === 'main' || !newId || newId === oldId || newId === MAIN) return false;
		if (!/^[a-z][a-z0-9_-]*$/.test(newId) || doc.phases?.some((p) => p.id === newId)) return false;
		ph.id = newId;
		for (const s of doc.stages) if (s.phase === oldId) s.phase = newId;
		onchange();
		onselect({ kind: 'phase', id: newId }, 'rename');
		return true;
	}
	// The field of a rename the document refused shows the name in force.
	const renamed = (el: HTMLInputElement, current: string, took: boolean) => {
		if (!took) el.value = current;
	};

	function setOperatesOn(v: string) {
		doc.operates_on = v === 'merge_request' ? 'merge_request' : undefined;
		onchange();
	}

	// An outcome's then is its chain's end: the last stage's success goes
	// there unless it says otherwise.
	function setThen(ph: DocPhase, then: string) {
		const chain = doc.stages.filter((x) => x.phase === ph.id);
		const last = chain[chain.length - 1];
		if (last && last.on_success === (ph.then ?? DONE)) last.on_success = then;
		ph.then = then;
		onchange();
	}

	function addChain(kind: 'watcher' | 'outcome') {
		const ph = kind === 'watcher' ? addWatcher(doc) : addOutcome(doc);
		onchange();
		onopen({ kind: 'phase', id: ph.id });
	}

	async function deletePhase(ph: DocPhase) {
		const count = doc.stages.filter((x) => x.phase === ph.id).length;
		const ok = await confirmDialog({
			title: t(
				ph.kind === 'outcome' ? 'flow.insp.deleteOutcomeTitle' : 'flow.insp.deleteWatcherTitle',
				{
					id: ph.id
				}
			),
			body: [
				count
					? th('flow.insp.deleteChainBody', { id: ph.id, count })
					: th('flow.insp.deleteChainEmpty', { id: ph.id }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t(ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher')
		});
		if (!ok) return;
		removePhase(doc, ph.id);
		onchange();
		leave(null);
	}

	// What a watcher or an outcome runs: stages of its own, or a sequence in
	// their place (`sequence: X`) — one chain written once, run from several
	// places.
	const phaseRuns = $derived(
		phaseSel && phaseSel.kind !== 'main' ? (chainSequence(doc, phaseSel) ?? '') : ''
	);
	async function setRuns(ph: DocPhase, id: string) {
		const own = stagesOf(doc, ph.id);
		if (own.length && !chainSequence(doc, ph)) {
			const ok = await confirmDialog({
				title: t('flow.insp.runsReplaceTitle', { id: ph.id }),
				body: [
					th('flow.insp.runsReplaceBody', { id: ph.id, count: own.length }),
					t('flow.insp.deleteNote')
				].join(' '),
				action: t('flow.insp.runsReplace')
			});
			if (!ok) return;
		}
		setChainSequence(doc, ph.id, id || undefined);
		onchange();
	}

	function newMainChain() {
		addMainChain(doc);
		onchange();
		onopen({ kind: 'phase', id: MAIN });
	}

	// ---- agents: the flow carries their definitions (FLOWLANG §1) ----------
	// They are edited in the page's Agents tab: opening one goes there.
	function newAgent(name?: string) {
		const a = addAgent(doc, name);
		onchange();
		onopen({ kind: 'agent', name: a.name });
	}
	// ---- schemas: the structures the flow's own files hold (FLOWLANG §4) ---
	const schemaSel = $derived(
		selection?.kind === 'schema' ? doc.schemas?.find((q) => q.name === selection.name) : undefined
	);
	function newSchema() {
		const q = addSchema(doc);
		onchange();
		onopen({ kind: 'schema', name: q.name });
	}
	function renameSchemaTo(from: string, to: string): boolean {
		to = to.trim();
		if (!to || to === from || !SCHEMA_NAME.test(to) || doc.schemas?.some((q) => q.name === to))
			return false;
		renameSchema(doc, from, to);
		onchange();
		onselect({ kind: 'schema', name: to }, 'rename');
		return true;
	}
	async function deleteSchema(name: string) {
		if (schemaUsers(doc, name).length) return;
		const ok = await confirmDialog({
			title: t('flow.insp.deleteSchemaTitle', { name }),
			body: [th('flow.insp.deleteSchemaBody', { name }), t('flow.insp.deleteNote')].join(' '),
			action: t('flow.insp.deleteSchema')
		});
		if (!ok) return;
		removeSchema(doc, name);
		onchange();
		leave(null);
	}
	// The schema's body is edited as YAML text; the document takes it only
	// once it parses, so a half-typed line never reaches the draft.
	let schemaText = $state('');
	let schemaError = $state('');
	$effect(() => {
		const q = schemaSel;
		untrack(() => {
			schemaText = q ? stringifyYaml(q.json_schema ?? {}, { lineWidth: 0 }) : '';
			schemaError = '';
		});
	});
	$effect(() => {
		const text = schemaText;
		untrack(() => {
			const q = schemaSel;
			if (!q) return;
			let v: unknown;
			try {
				v = parseYaml(text) ?? {};
			} catch (err) {
				schemaError = err instanceof Error ? err.message.split('\n')[0] : String(err);
				return;
			}
			schemaError = '';
			if (JSON.stringify(v) === JSON.stringify(q.json_schema)) return;
			q.json_schema = v;
			onchange();
		});
	});
	// Which structure a port may name: the flow's schemas, and whatever it
	// names now even if no schema of that name is declared (shown as is).
	const schemaChoices = (current?: string) => {
		const names = (doc.schemas ?? []).map((q) => q.name);
		return current && !names.includes(current) ? [...names, current] : names;
	};

	// ---- MCP servers: tools the flow's agents may call (FLOWLANG §1) -------
	// A server is reached one way — builtin, command or url — and given env
	// (a command) or headers (a url) whose values may read a project secret.
	const mcpSel = $derived(
		selection?.kind === 'mcp' ? doc.mcp_servers?.find((m) => m.name === selection.name) : undefined
	);
	function newMCP() {
		const m = addMCPServer(doc);
		onchange();
		onopen({ kind: 'mcp', name: m.name });
	}
	function renameMCPTo(from: string, to: string): boolean {
		to = to.trim();
		if (!to || to === from || !MCP_NAME.test(to) || doc.mcp_servers?.some((m) => m.name === to))
			return false;
		renameMCPServer(doc, from, to);
		onchange();
		onselect({ kind: 'mcp', name: to }, 'rename');
		return true;
	}
	async function deleteMCP(name: string) {
		const users = mcpUsers(doc, name);
		const ok = await confirmDialog({
			title: t('flow.insp.deleteMCPTitle', { name }),
			body: [
				users.length
					? th('flow.insp.deleteMCPUsed', { name, count: users.length, agents: users.join(', ') })
					: th('flow.insp.deleteMCPBody', { name }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t('flow.insp.deleteMCP')
		});
		if (!ok) return;
		removeMCPServer(doc, name);
		onchange();
		leave(null);
	}
	// Switching how a server is reached keeps what still applies: a
	// command's words and env, a url's address and headers.
	function setMCPKind(m: DocMCPServer, kind: MCPKind) {
		if (kind === mcpKind(m)) return;
		m.builtin = kind === 'builtin' ? (m.builtin ?? MCP_BUILTINS[0]) : undefined;
		m.command = kind === 'command' ? (m.command ?? []) : undefined;
		m.url = kind === 'url' ? (m.url ?? '') : undefined;
		if (kind !== 'command') m.env = undefined;
		if (kind !== 'url') m.headers = undefined;
		onchange();
	}
	// The command and the env or headers are typed as text and rows, and
	// reach the document as they parse: a word per line, rows with a name.
	let mcpCommand = $state('');
	let mcpRows = $state<KVRow[]>([]);
	$effect(() => {
		const m = mcpSel;
		const kind = m ? mcpKind(m) : undefined;
		untrack(() => {
			mcpCommand = m ? (m.command ?? []).join('\n') : '';
			const kv = kind === 'url' ? m?.headers : kind === 'command' ? m?.env : undefined;
			mcpRows = Object.entries(kv ?? {}).map(([key, value]) => ({ key, value }));
		});
	});
	$effect(() => {
		const text = mcpCommand;
		untrack(() => {
			const m = mcpSel;
			if (!m || mcpKind(m) !== 'command') return;
			const words = text
				.split('\n')
				.map((w) => w.trim())
				.filter(Boolean);
			if (JSON.stringify(words) === JSON.stringify(m.command ?? [])) return;
			m.command = words;
			onchange();
		});
	});
	$effect(() => {
		const rows = mcpRows;
		untrack(() => {
			const m = mcpSel;
			const kind = m ? mcpKind(m) : 'builtin';
			if (!m || kind === 'builtin') return;
			const kv: Record<string, string> = {};
			for (const r of rows) if (r.key.trim()) kv[r.key.trim()] = r.value;
			const next = Object.keys(kv).length ? kv : undefined;
			const field = kind === 'url' ? 'headers' : 'env';
			if (JSON.stringify(next) === JSON.stringify(m[field])) return;
			m[field] = next;
			onchange();
		});
	});
	// A key the parser refuses: one given twice, or not a name — an
	// environment variable's, a header's.
	const ENV_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/;
	const HEADER_KEY = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
	const badKeys = $derived.by(() => {
		const re = mcpSel && mcpKind(mcpSel) === 'url' ? HEADER_KEY : ENV_KEY;
		const keys = mcpRows.map((r) => r.key.trim()).filter(Boolean);
		return new Set(keys.filter((k, i) => !re.test(k) || keys.indexOf(k) !== i));
	});

	// ---- services: the project's, named by a stage or read as a source -----
	// What a service stage may name: a string parameter (usually $app), or a
	// service the flow names already.
	const services = $derived(flowServices(doc));
	const serviceChoices = $derived([
		...new Set([...stringParams(doc).map((p) => '$' + p), ...services])
	]);
	function setServiceDo(s: DocStage, v: ServiceDo) {
		s.do = v === 'start' ? undefined : v;
		// A stop takes no time to be healthy.
		if (v === 'stop') s.timeout = undefined;
		onchange();
	}

	// ---- sequences: chains written once, run where a stage names them -----
	const seqSel = $derived(
		selection?.kind === 'sequence' ? doc.sequences?.find((q) => q.id === selection.id) : undefined
	);
	function newSequence() {
		const q = addSequence(doc);
		onchange();
		onopen({ kind: 'sequence', id: q.id });
	}
	function renameSequenceTo(from: string, to: string): boolean {
		to = to.trim();
		if (
			!to ||
			to === from ||
			to === MAIN ||
			!/^[a-z][a-z0-9_-]*$/.test(to) ||
			doc.sequences?.some((q) => q.id === to)
		)
			return false;
		renameSequence(doc, from, to);
		onchange();
		onselect({ kind: 'sequence', id: to }, 'rename');
		return true;
	}
	async function deleteSequence(q: DocSequence) {
		if (sequenceUsers(doc, q.id).length) return;
		const ok = await confirmDialog({
			title: t('flow.insp.deleteSequenceTitle', { id: q.id }),
			body: [
				th('flow.insp.deleteSequenceBody', { id: q.id, count: q.stages.length }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t('flow.insp.deleteSequence')
		});
		if (!ok) return;
		removeSequence(doc, q.id);
		onchange();
		leave(null);
	}
	// What a sequence stage may run: the flow's sequences, and the main chain
	// for the chains that are not it — never the sequence it is written in.
	const runnable = (s: DocStage) => [
		...(hasMain(doc) && s.phase !== MAIN ? [MAIN] : []),
		...(doc.sequences ?? []).map((q) => q.id).filter((id) => id !== scopeSeq?.id)
	];
	// The stages — in the chains and in sequences — that match, each as the
	// selection that opens it.
	const runners = (match: (s: DocStage) => boolean): { label: string; sel: Selection }[] => [
		...doc.stages
			.filter(match)
			.map((s) => ({ label: s.id, sel: { kind: 'stage', id: s.id } as const })),
		...(doc.sequences ?? []).flatMap((q) =>
			q.stages.filter(match).map((s) => ({
				label: `${q.id} › ${s.id}`,
				sel: { kind: 'stage', id: s.id, sequence: q.id } as const
			}))
		)
	];

	// What an input may read besides the engine's sources: every path a stage
	// of the flow writes.
	const writtenPaths = $derived(
		[
			...new Set(
				allStages(doc).flatMap((x) =>
					Object.values(x.output ?? {})
						.map((p) => p.to ?? '')
						.filter(Boolean)
				)
			)
		].sort()
	);
	const schemaNames = $derived((doc.schemas ?? []).map((q) => q.name));
	// An action's settings (`with:`): read one, write it back preserving order.
	function settingValue(s: DocStage, name: string): string {
		const v = s.with?.[name];
		return typeof v === 'string' ? v : '';
	}
	// A list setting holds items written here, or a list $parameter.
	function setSettingList(s: DocStage, name: string, value: string | string[]) {
		const cur = { ...(s.with ?? {}) };
		if (Array.isArray(value) ? value.length : value) cur[name] = value;
		else delete cur[name];
		s.with = Object.keys(cur).length ? cur : undefined;
		onchange();
	}
	function setSetting(s: DocStage, name: string, value: string) {
		const cur = { ...(s.with ?? {}) };
		if (value) cur[name] = value;
		else delete cur[name];
		s.with = Object.keys(cur).length ? cur : undefined;
		onchange();
	}

	// The inspector cross-fades between selections instead of jumping: the
	// outgoing and incoming views share one grid cell for the moment.
	const viewKey = $derived(
		selection?.kind === 'stage'
			? `s:${selection.sequence ?? ''}:${selection.id}`
			: selection?.kind === 'edge'
				? `s:${selection.sequence ?? ''}:${selection.stage}`
				: selection?.kind === 'phase'
					? `p:${selection.id}`
					: selection?.kind === 'sequence'
						? `q:${selection.id}`
						: selection?.kind === 'schema'
							? `j:${selection.name}`
							: selection?.kind === 'mcp'
								? `m:${selection.name}`
								: 'flow'
	);
	const xf = () => ({ duration: ms('--dur-fade', 160) });
	// The stages a chain or a sequence may add: a sequence stage once there
	// is something for it to run — another sequence, or the main chain again
	// (from anywhere but the main chain itself).
	const addKinds = (where: { phase?: string; sequence?: string }): StageKind[] => {
		const runs =
			(doc.sequences ?? []).some((q) => q.id !== where.sequence) ||
			(hasMain(doc) && where.phase !== MAIN);
		return [
			'agent',
			'command',
			'human_gate',
			'action',
			'wait',
			'service',
			...(runs ? ['sequence' as const] : [])
		];
	};
	const mainPhase = $derived(doc.phases?.find((p) => p.kind === 'main'));
	const sequenceSel = $derived(
		stage?.type === 'sequence' ? doc.sequences?.find((q) => q.id === stage.sequence) : undefined
	);

	// A stage is added at the end of a chain or a sequence, wired in after
	// its last stage; the flow panel adds to the main chain (or the first
	// chain there is).
	function addToChain(type: StageKind, phase: string) {
		const s = appendStage(doc, type, phase);
		onchange();
		onopen({ kind: 'stage', id: s.id });
	}
	function addToSequence(type: StageKind, id: string) {
		const s = appendSequenceStage(doc, type, id);
		if (!s) return;
		onchange();
		onopen({ kind: 'stage', id: s.id, sequence: id });
	}
	function addStage(type: StageKind) {
		const phase = mainPhase?.id ?? doc.phases?.[0]?.id;
		if (phase) addToChain(type, phase);
	}

	function renameStage(oldId: string, newId: string): boolean {
		newId = newId.trim();
		if (
			!newId ||
			newId === oldId ||
			[END, DONE, ABORT, DEBUG, START, FAIL].includes(newId) ||
			!/^[a-z][a-z0-9_-]*$/.test(newId)
		)
			return false;
		// Ids are unique in the chains, and in each sequence on its own.
		if (scopeStages.some((s) => s.id === newId)) return false;
		for (const s of scopeStages) {
			if (s.id === oldId) s.id = newId;
			if (s.on_success === oldId) s.on_success = newId;
			for (const kind of EDGE_KINDS) {
				for (const link of chainLinks(edgeOf(s, kind))) {
					if (link.to === oldId) link.to = newId;
				}
			}
		}
		onchange();
		onselect({ kind: 'stage', id: newId, ...inSeq }, 'rename');
		return true;
	}

	async function deleteStage(id: string) {
		const seq = scopeSeq;
		const refs = scopeStages.filter(
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
		const rest = (seq ? seq.stages : doc.stages).filter((s) => s.id !== id);
		for (const s of rest) {
			// A success that led there ends the chain — in a sequence it goes
			// on to the next stage, or out.
			if (s.on_success === id) s.on_success = seq ? undefined : END;
			for (const kind of EDGE_KINDS) {
				setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
			}
		}
		if (seq) seq.stages = rest;
		else doc.stages = rest;
		onchange();
		leave(seq ? { kind: 'sequence', id: seq.id } : null);
	}

	// A new route starts somewhere legal: a stage of the same chain (a route
	// may not leave it). A failure or timeout retries the stage itself; a
	// gate's rejection goes back to whatever leads into the gate.
	function addEdge(s: NonNullable<typeof stage>, kind: EdgeKind) {
		const mine = routable(s);
		const feeder = mine.find((x) => x.on_success === s.id && x.id !== s.id);
		const to = s.type === 'human_gate' ? (feeder ?? mine.find((x) => x.id !== s.id))?.id : s.id;
		setEdge(s, kind, to ? { to, max: 3 } : { to: DEBUG });
		onchange();
	}

	const targetsGate = (id: string) => scopeStages.find((x) => x.id === id)?.type === 'human_gate';
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

	/** Delete what is selected (⌫ / Del on the canvas): a stage, a chain, an
	 *  agent or a sequence asks first; an edge goes at once (re-adding it is
	 *  one click). */
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
	}

	// appendLink adds a fallback ("then") after the chain's last link.
	function appendLink(e: DocEdge) {
		let last = e;
		while (last.then) last = last.then;
		if (isReservedTarget(last.to) || last.max === -1) return; // nothing follows a reserved target or an unlimited link
		// The usual fallback once retries are spent: a human with the workspace.
		last.then = { to: DEBUG };
		onchange();
	}

	// removeLink takes one link out of a route, the links after it moving up
	// (the route goes with its only link).
	function removeLink(s: DocStage, kind: EdgeKind, depth: number) {
		const links = chainLinks(edgeOf(s, kind));
		if (depth === 0) setEdge(s, kind, links[0]?.then);
		else links[depth - 1].then = links[depth].then;
		onchange();
	}
	// A link's limit: how many times it may send the run to its target
	// (a whole number from 1), or none — a human decides each time.
	function setMax(link: DocEdge, text: string) {
		const n = Number(text);
		if (text.trim() === '' || !Number.isInteger(n) || n < 1) return;
		link.max = n;
		onchange();
	}
	function setUnlimited(link: DocEdge, on: boolean) {
		link.max = on ? -1 : 3;
		if (on) link.then = undefined;
		onchange();
	}

	// What the back button names: the panel `back` opens.
	const backLabel = $derived.by(() => {
		const b = back;
		if (b === undefined) return '';
		if (b === null) return t('flow.insp.formula');
		switch (b.kind) {
			case 'stage':
				return t('flow.insp.backStage', { id: b.sequence ? `${b.sequence} › ${b.id}` : b.id });
			case 'edge':
				return t('flow.insp.backStage', {
					id: b.sequence ? `${b.sequence} › ${b.stage}` : b.stage
				});
			case 'phase': {
				const ph = doc.phases?.find((p) => p.id === b.id);
				return ph?.kind === 'main'
					? t('flow.insp.chainMain')
					: t(ph?.kind === 'outcome' ? 'flow.insp.backOutcome' : 'flow.insp.backWatcher', {
							id: b.id
						});
			}
			case 'agent':
				return t('flow.insp.backAgent', { name: b.name });
			case 'sequence':
				return t('flow.insp.backSequence', { id: b.id });
			case 'schema':
				return t('flow.insp.backSchema', { name: b.name });
			case 'mcp':
				return t('flow.insp.backMCP', { name: b.name });
		}
	});
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
				{@const s = stage}
				{@const only = scopeSeq?.stages.length === 1}
				<div class="ih">
					<StageChip type={s.type} />
					{#if scopeSeq}<span class="mono fnt">{scopeSeq.id}.</span>{/if}
					<span class="mono iid ell">{s.id}</span>
					<span class="spacer"></span>
					<button
						class="btn-danger btn-sm"
						disabled={only}
						onclick={() => deleteStage(s.id)}
						title={only ? t('flow.insp.onlyStage') : t('flow.insp.deleteTitle')}
						>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="field">
					<label for="st-id">{t('flow.insp.id')}</label>
					<input
						id="st-id"
						class="mono"
						value={s.id}
						onchange={(e) =>
							renamed(e.currentTarget, s.id, renameStage(s.id, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.renameHelp')}</div>
				</div>
				{#if scopeSeq}
					{@const q = scopeSeq}
					<div class="field">
						<span class="lbl-in">{t('flow.insp.inSequence')}</span>
						<div class="row">
							<span class="mono">{q.id}</span>
							<span class="fnt small ell grow">{q.description ?? ''}</span>
							<button class="btn-sm" onclick={() => onopen({ kind: 'sequence', id: q.id })}
								>{t('flow.insp.editSequence')}</button
							>
						</div>
						<div class="help">{t('flow.insp.inSequenceHelp')}</div>
					</div>
				{:else if doc.phases?.length}
					<div class="field">
						<label for="st-phase">{t('flow.insp.chain')}</label>
						<Select
							id="st-phase"
							value={s.phase}
							options={doc.phases.map((ph) => ({
								value: ph.id,
								label:
									ph.kind === 'main'
										? t('flow.insp.chainMain')
										: t(`flow.insp.chainOption.${ph.kind}`, {
												id: ph.id,
												trigger: triggerLabel(ph.on)
											})
							}))}
							onchange={(v) => {
								movePhase(doc, s.id, v);
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.phaseHelp')}</div>
					</div>
				{/if}

				{#if s.type !== 'sequence'}
					<div class="field">
						<label for="st-run-if">{t('flow.insp.runIf')}</label>
						<Select
							id="st-run-if"
							value={s.run_if ?? ''}
							options={[
								{ value: '', label: t('flow.insp.runIf.none') },
								...RUN_IF_FACTS(),
								...boolParams(doc).flatMap((p) => [
									{ value: '$' + p, label: '$' + p },
									{ value: 'not $' + p, label: 'not $' + p }
								]),
								...(s.run_if &&
								!RUN_IF_FACTS().some((o) => o.value === s.run_if) &&
								!boolParams(doc).some((p) => s.run_if === '$' + p || s.run_if === 'not $' + p)
									? [{ value: s.run_if, label: s.run_if }]
									: [])
							]}
							onchange={(v) => {
								s.run_if = v || undefined;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.runIfHelp')}</div>
					</div>
				{/if}
				<datalist id="flow-params">
					{#each stringParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
				<datalist id="flow-duration-params">
					{#each durationParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>

				{#if s.type === 'agent'}
					<div class="field">
						<label for="st-agent">{t('flow.insp.agent')}</label>
						<input
							id="st-agent"
							list="agent-names"
							bind:value={s.agent}
							oninput={onchange}
							placeholder={agents[0]?.name ?? ''}
						/>
						<datalist id="agent-names">
							{#each agents as a (a.name)}<option value={a.name}></option>{/each}
						</datalist>
						<div class="help">{t('flow.insp.agentHelp')}</div>
						{#if agentDecl}
							{@const name = agentDecl.name}
							<div class="chips">
								<button class="btn-sm" onclick={() => onopen({ kind: 'agent', name })}
									>{t('flow.insp.editAgent', { name })}</button
								>
							</div>
						{:else}
							<div class="chips">
								<button class="btn-sm" onclick={() => newAgent(s.agent)}
									>{s.agent && AGENT_NAME.test(s.agent)
										? t('flow.insp.declareAgent', { name: s.agent })
										: t('flow.insp.newAgent')}</button
								>
							</div>
						{/if}
					</div>
					<!-- Every agent stage says what it runs on: a kind or a model, or
					     the parameter the workshop binds. No silent fallback. -->
					<div class="field">
						<label for="st-harness"
							>{t('flow.insp.harness')}
							{#if !s.harness}<span class="hue-amb">{t('flow.insp.required')}</span>{/if}</label
						>
						<input
							id="st-harness"
							list="flow-params"
							bind:value={s.harness}
							oninput={onchange}
							placeholder="$harness"
						/>
						<div class="help">{t('flow.insp.stageHarnessHelp')}</div>
					</div>
					<div class="field">
						<label for="st-model"
							>{t('flow.insp.model')}
							{#if !s.model}<span class="hue-amb">{t('flow.insp.required')}</span>{/if}</label
						>
						<input
							id="st-model"
							list="flow-params"
							bind:value={s.model}
							oninput={onchange}
							placeholder="$model"
						/>
						<div class="help">{t('flow.insp.stageModelHelp')}</div>
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
							list="flow-duration-params"
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
						{#key s.agent}
							<PortRows
								stage={s}
								field="input"
								declared={declInputs}
								sources={sourcesFor('agent', services, phaseOf(doc, s)?.on.signal === 'ci_failed')}
								paths={writtenPaths}
								schemas={schemaNames}
								{onchange}
							/>
						{/key}
					{:else if s.agent}
						<div class="help" class:hue-amb={!agentDecl}>
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
						{#key s.agent}
							<PortRows
								stage={s}
								field="output"
								declared={declOutputs}
								schemas={schemaNames}
								{onchange}
							/>
						{/key}
					{/if}
					{#if declVerdict}
						<div class="help">
							{@html th('flow.insp.verdictHelp', { sentinel: SOURCE_VERDICT })}
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
							placeholder={t('flow.insp.runRefPlaceholder')}
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
					<div class="field">
						<label for="st-cmd-timeout">{t('flow.insp.timeout')}</label>
						<input
							id="st-cmd-timeout"
							class="mono"
							list="flow-duration-params"
							bind:value={s.timeout}
							oninput={onchange}
							placeholder={paramDefault(doc, 'timeout') ?? '30m'}
						/>
						<div class="help">{t('flow.insp.commandTimeoutHelp')}</div>
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
					<PortRows
						stage={s}
						field="input"
						paths={writtenPaths}
						schemas={schemaNames}
						valuePlaceholder="artifacts/thresholds.json"
						{onchange}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.writes')}</div>
						<div class="help">{t('flow.insp.writesHelp')}</div>
					</div>
					<PortRows
						stage={s}
						field="output"
						schemas={schemaNames}
						valuePlaceholder="artifacts/report.json"
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
					<PortRows
						stage={s}
						field="input"
						sources={sourcesFor('human_gate', services)}
						paths={writtenPaths}
						schemas={schemaNames}
						types={GATE_TYPES}
						links={1}
						{onchange}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.autoBy')}</div>
						<div class="help">{t('flow.insp.autoByHelp')}</div>
					</div>
					<div class="grid2">
						<div class="field">
							<label for="st-auto-ok">{t('flow.insp.approveWhen')}</label>
							<Select
								id="st-auto-ok"
								value={s.auto_approve ?? ''}
								options={[
									{ value: '', label: '—' },
									...observed.map((sig) => ({
										value: sig.slug,
										label: sig.title,
										disabled: sig.slug === s.auto_reject
									}))
								]}
								onchange={(v) => {
									s.auto_approve = v || undefined;
									onchange();
								}}
							/>
						</div>
						<div class="field">
							<label for="st-auto-no">{t('flow.insp.rejectWhen')}</label>
							<Select
								id="st-auto-no"
								value={s.auto_reject ?? ''}
								options={[
									{ value: '', label: '—' },
									...observed.map((sig) => ({
										value: sig.slug,
										label: sig.title,
										disabled: sig.slug === s.auto_approve
									}))
								]}
								onchange={(v) => {
									s.auto_reject = v || undefined;
									onchange();
								}}
							/>
						</div>
					</div>
					{#if s.auto_approve || s.auto_reject}
						<div class="help">{t('flow.insp.manualStillOk')}</div>
					{/if}
				{:else if s.type === 'action'}
					<div class="field">
						<label for="st-action">{t('flow.insp.action')}</label>
						<Select
							id="st-action"
							value={s.action ?? ''}
							options={[
								{ value: '', label: '—' },
								...actions.map((a) => ({ value: a.slug, label: a.title }))
							]}
							onchange={(v) => {
								s.action = v || undefined;
								// Prefill the new action's ports with their conventional paths.
								const sig = actions.find((a) => a.slug === s.action);
								const ins: Record<string, DocPort> = {};
								for (const d of sig?.inputs ?? [])
									if (d.defaultPath) ins[d.name] = { from: d.defaultPath, type: d.type };
								const outs: Record<string, DocPort> = {};
								for (const d of sig?.outputs ?? [])
									outs[d.name] = { to: d.defaultPath ?? `artifacts/${d.name}`, type: d.type };
								s.input = Object.keys(ins).length ? ins : undefined;
								s.output = Object.keys(outs).length ? outs : undefined;
								s.with = undefined;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.actionHelp')}</div>
					</div>
					{#if actionSig}
						{#if actionSig.description}<div class="help">{actionSig.description}</div>{/if}
						{#if actionSig.requires === 'tracker'}
							<div class="help">{t('flow.insp.tracker')}</div>
						{:else if actionSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: portLabel(actionSig.requires) })}
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
						{#each actionSig.settings as d (d.name)}
							{@const cur = settingValue(s, d.name)}
							{@const fits = d.kind === 'enum' ? enumParamsWithin(doc, d.values ?? []) : []}
							<div class="io-bind card-in field">
								<label for="act-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.required}<span class="hue-amb">{t('flow.insp.required')}</span>{:else}<span
											class="fnt">{t('flow.insp.optional')}</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								{#if d.kind === 'enum'}
									<Select
										id="act-{d.name}"
										value={cur || d.defaultValue || ''}
										options={[
											...(!d.required && !d.defaultValue ? [{ value: '', label: '—' }] : []),
											...(d.values ?? []).map((v) => ({ value: v, label: v })),
											...fits.map((p) => ({ value: '$' + p, label: '$' + p })),
											...(isParamRef(cur) && !fits.includes(cur.slice(1))
												? [{ value: cur, label: cur }]
												: [])
										]}
										onchange={(v) => setSetting(s, d.name, v)}
									/>
									{#if fits.length}<div class="help">{t('flow.insp.enumParamHelp')}</div>{/if}
								{:else if d.kind === 'list'}
									{@const items = s.with?.[d.name]}
									<Select
										id="act-{d.name}"
										value={typeof items === 'string' ? items : ''}
										options={[
											{ value: '', label: t('flow.insp.listWritten') },
											...stringListParams(doc).map((p) => ({ value: '$' + p, label: '$' + p })),
											...(typeof items === 'string' &&
											items &&
											!stringListParams(doc).includes(items.slice(1))
												? [{ value: items, label: items }]
												: [])
										]}
										onchange={(v) =>
											setSettingList(s, d.name, v || (Array.isArray(items) ? items : []))}
									/>
									{#if typeof items !== 'string'}
										<input
											class="mono"
											aria-label={d.name}
											value={(items ?? []).join(', ')}
											placeholder={t('flow.insp.paramListPlaceholder')}
											onchange={(e) =>
												setSettingList(
													s,
													d.name,
													e.currentTarget.value
														.split(',')
														.map((v) => v.trim())
														.filter(Boolean)
												)}
										/>
									{/if}
									<div class="help">{t('flow.insp.listSettingHelp')}</div>
								{:else}
									<input
										id="act-{d.name}"
										list="flow-params"
										value={cur}
										placeholder={t('flow.insp.textPlaceholder')}
										oninput={(e) => setSetting(s, d.name, e.currentTarget.value.trim())}
									/>
									<div class="help">{t('flow.insp.textInputHelp')}</div>
								{/if}
							</div>
						{/each}
						{#key s.action}
							{#if actionSig.inputs.length}
								<div class="grp"><div class="lbl">{t('flow.insp.reads')}</div></div>
								<PortRows
									stage={s}
									field="input"
									declared={actionSig.inputs}
									paths={writtenPaths}
									schemas={schemaNames}
									{onchange}
								/>
							{/if}
							{#if actionSig.outputs.length}
								<div class="grp"><div class="lbl">{t('flow.insp.writes')}</div></div>
								<PortRows
									stage={s}
									field="output"
									declared={actionSig.outputs}
									schemas={schemaNames}
									{onchange}
								/>
							{/if}
						{/key}
					{:else if s.action}
						<div class="help hue-amb">{t('flow.insp.unknownAction', { name: s.action })}</div>
					{/if}
				{:else if s.type === 'service'}
					{@const readers = runners((x) =>
						Object.values(x.input ?? {}).some(
							(p) => !!s.service && p.from === serviceSource(s.service)
						)
					)}
					<div class="field">
						<label for="st-service">{t('flow.insp.service')}</label>
						<input
							id="st-service"
							class="mono"
							list="flow-services"
							bind:value={s.service}
							oninput={onchange}
							placeholder="$app"
						/>
						<datalist id="flow-services">
							{#each serviceChoices as v (v)}<option value={v}></option>{/each}
						</datalist>
						<div class="help">{t('flow.insp.serviceHelp')}</div>
					</div>
					<div class="field">
						<label for="st-do">{t('flow.insp.serviceDo')}</label>
						<Select
							id="st-do"
							value={s.do ?? 'start'}
							options={SERVICE_DOS.map((d) => ({ value: d, label: t(`flow.insp.serviceDo.${d}`) }))}
							onchange={(v) => setServiceDo(s, v as ServiceDo)}
						/>
					</div>
					{#if s.do !== 'stop'}
						<div class="field">
							<label for="st-svc-timeout">{t('flow.insp.timeout')}</label>
							<input
								id="st-svc-timeout"
								class="mono"
								list="flow-duration-params"
								bind:value={s.timeout}
								oninput={onchange}
								placeholder={t('flow.insp.serviceOwnTimeout')}
							/>
							<div class="help">{t('flow.insp.serviceTimeoutHelp')}</div>
						</div>
					{/if}
					<div class="grp">
						<div class="lbl">{t('flow.insp.serviceAddress')}</div>
						<div class="help">
							{@html th('flow.insp.serviceAddressHelp', {
								source: serviceSource(s.service || '$app')
							})}
						</div>
						{#if readers.length}
							<div class="chips">
								{#each readers as u (u.label)}<button
										class="btn-sm mono"
										onclick={() => onopen(u.sel)}>{u.label}</button
									>{/each}
							</div>
						{/if}
					</div>
				{:else if s.type === 'sequence'}
					{@const options = runnable(s)}
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
						<div class="grp">
							<div class="lbl">{t('flow.insp.sequenceStages')}</div>
							{#each sequenceSel.stages as q (q.id)}
								<div class="seq-row">
									<StageChip type={q.type} />
									<span class="mono">{s.id}.{q.id}</span>
									{#if q.sequence}<span class="mut small">→ {q.sequence}</span>{/if}
								</div>
							{/each}
						</div>
						<div class="help">{t('flow.insp.sequenceEditHelp')}</div>
					{/if}
				{:else if s.type === 'wait'}
					<div class="field">
						<label for="st-for">{t('flow.insp.waitFor')}</label>
						<Select
							id="st-for"
							value={s.for ?? ''}
							options={[
								{ value: '', label: '—' },
								...observed.map((sig) => ({ value: sig.slug, label: sig.title }))
							]}
							onchange={(v) => {
								s.for = v || undefined;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.waitHelp')}</div>
					</div>
					{#if waitSig}
						{#if waitSig.description}<div class="help">{waitSig.description}</div>{/if}
						{#if waitSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: portLabel(waitSig.requires) })}
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

				<!-- Every way out of the stage, one row each in the same shape: its
				     kind, where it goes — and for a failure route, how many times,
				     then what. -->
				<div class="routes" bind:this={routesEl}>
					<div class="lbl">{t('flow.insp.routes')}</div>
					<div class="route" class:hl={focusRoute === 'success'} data-route="success">
						<div class="link">
							<span
								class="rk mono t-{EDGE_HUE.success}"
								title={givesVerdict(s, agents)
									? t('flow.insp.successCarries')
									: t('flow.insp.successTitle')}>success</span
							>
							<span class="to">
								<Select
									id="st-succ"
									label={t('flow.insp.successTitle')}
									value={s.on_success ?? ''}
									options={[
										// In a sequence, saying nothing goes on to the next stage —
										// or, from the last, out by the sequence stage's success.
										...(scopeSeq
											? [
													{
														value: '',
														label:
															scopeSeq.stages.at(-1)?.id === s.id
																? t('flow.insp.leaveOption')
																: t('flow.insp.nextOption', {
																		next: scopeSeq.stages[scopeSeq.stages.indexOf(s) + 1]?.id ?? ''
																	})
													}
												]
											: []),
										...routable(s)
											.filter((x) => x.id !== s.id)
											.map((x) => ({ value: x.id, label: x.id })),
										{ value: END, label: t('flow.insp.endOption') },
										{ value: DONE, label: t('flow.insp.doneOption') },
										{ value: ABORT, label: t('flow.insp.abortOption') }
									]}
									onchange={(v) => {
										s.on_success = v || undefined;
										onchange();
									}}
								/>
							</span>
						</div>
					</div>
					{#each edgeKindsFor(s.type) as kind (kind)}
						{@const e = edgeOf(s, kind)}
						{#if e}
							{@const links = chainLinks(e)}
							{@const last = links[links.length - 1]}
							<div class="route" class:hl={focusRoute === kind} data-route={kind}>
								{#each links as link, depth (depth)}
									<div class="link">
										<span
											class="rk mono t-{EDGE_HUE[kind]}"
											class:then={depth > 0}
											title={t('flow.insp.routeCarries', {
												kind,
												sentinel: feedSourceFor(kind)
											})}>{depth > 0 ? 'then' : kind}</span
										>
										<span class="to">
											<Select
												id="rt-{kind}-{depth}"
												label={depth > 0 ? `${kind} · then #${depth}` : kind}
												value={link.to}
												options={[
													...routable(s).map((x) => ({ value: x.id, label: x.id })),
													...(scopeSeq ? [{ value: FAIL, label: t('flow.insp.failOption') }] : []),
													{ value: END, label: t('flow.insp.endOption') },
													{ value: DONE, label: t('flow.insp.doneOption') },
													{ value: ABORT, label: t('flow.insp.abortOption') },
													{ value: DEBUG, label: t('flow.insp.debugOption') }
												]}
												onchange={(v) => retarget(s.type, link, v)}
											/>
										</span>
										{#if !isReservedTarget(link.to)}
											{#if link.max === -1}
												<button
													class="btn-sm btn-sel unl"
													title={t('flow.insp.limitAgain')}
													onclick={() => setUnlimited(link, false)}>∞</button
												>
											{:else}
												<span class="x" aria-hidden="true">×</span>
												<input
													id="rt-{kind}-{depth}-max"
													class="mono max"
													type="number"
													min="1"
													aria-label={t('flow.insp.max')}
													title={t('flow.insp.maxTitle')}
													value={link.max ?? 1}
													oninput={(ev) => setMax(link, ev.currentTarget.value)}
												/>
												{#if unlimitedOK(s.type, link.to)}
													<button
														class="btn-sm unl"
														aria-label={t('flow.insp.noLimit')}
														title={targetsGate(link.to)
															? t('flow.insp.noLimitTitleGate')
															: t('flow.insp.noLimitTitle')}
														onclick={() => setUnlimited(link, true)}>∞</button
													>
												{/if}
											{/if}
										{/if}
										<button
											class="btn-icon"
											title={t('flow.insp.removeLink')}
											aria-label={t('flow.insp.removeLink')}
											onclick={() => removeLink(s, kind, depth)}>{I.close}</button
										>
									</div>
								{/each}
								{#if !isReservedTarget(last.to) && last.max !== -1}
									<button class="btn-sm btn-ghost add-then" onclick={() => appendLink(e)}
										>{I.add} {t('flow.insp.thenLink')}</button
									>
								{/if}
							</div>
						{/if}
					{/each}
					<div class="chips">
						{#each edgeKindsFor(s.type).filter((k) => !edgeOf(s, k)) as kind (kind)}
							<button class="btn-sm hue-{EDGE_HUE[kind]}" onclick={() => addEdge(s, kind)}
								>{I.add} {kind}</button
							>
						{/each}
					</div>
					<div class="help">
						{t(scopeSeq ? 'flow.insp.endsHelpSequence' : 'flow.insp.endsHelp')}
					</div>
					{#if edgeKindsFor(s.type).length}
						<div class="help">{t('flow.insp.routesHelp')}</div>
					{/if}
					{#if edgeKindsFor(s.type).includes('on_timeout')}
						<div class="help">{t('flow.insp.timeoutsHelp')}</div>
					{/if}
				</div>
			{:else if phaseSel}
				{@const ph = phaseSel}
				{@const firing = fires(ph.on)}
				<div class="ih">
					<span class="pill sm flat sq mono t-{kindHue(ph)}">{t(`flow.insp.kind.${ph.kind}`)}</span>
					{#if ph.kind !== 'main'}<span class="mono iid ell">{ph.id}</span>{/if}
					<span class="spacer"></span>
					<span class="fnt tiny"
						>{ph.kind === 'outcome' && !phaseStageCount
							? t('flow.insp.phaseThen', { then: ph.then ?? DONE })
							: t('flow.insp.phaseStages', { count: phaseStageCount })}</span
					>
					{#if ph.kind !== 'main'}
						<button
							class="btn-danger btn-sm"
							onclick={() => deletePhase(ph)}
							title={t(
								ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher'
							)}>{t('flow.insp.delete')}</button
						>
					{/if}
				</div>
				{#if ph.kind === 'main'}
					<div class="help">{t('flow.insp.mainHelp')}</div>
					<div class="field">
						<label for="ph-operates">{t('flow.insp.operatesOn')}</label>
						<Select
							id="ph-operates"
							value={doc.operates_on ?? 'ticket'}
							options={OPERATES_ON.map((r) => ({
								value: r,
								label: t(`flow.insp.operatesOn.${r}`)
							}))}
							onchange={(v) => setOperatesOn(v)}
						/>
						<div class="help">{t('flow.insp.operatesOnHelp')}</div>
					</div>
					{@render addTo(ph)}
				{:else}
					<div class="help">
						{t(ph.kind === 'outcome' ? 'flow.insp.outcomeHelp' : 'flow.insp.watcherHelp')}
					</div>
					<div class="field">
						<label for="ph-id">{t('flow.insp.id')}</label>
						<input
							id="ph-id"
							class="mono"
							value={ph.id}
							onchange={(e) =>
								renamed(e.currentTarget, ph.id, renamePhase(ph.id, e.currentTarget.value))}
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
						<label for="ph-on"
							>{t(ph.kind === 'outcome' ? 'flow.insp.firesOn' : 'flow.insp.opensOn')}</label
						>
						<Select
							id="ph-on"
							value={ph.on.signal}
							options={[
								...(ph.kind === 'watcher'
									? [{ value: MANUAL, label: t('flow.insp.opensOn.manual') }]
									: []),
								...observed.map((sig) => ({ value: sig.slug, label: sig.title })),
								...(firing && !observed.some((g) => g.slug === ph.on.signal)
									? [{ value: ph.on.signal, label: ph.on.signal }]
									: [])
							]}
							onchange={(v) => {
								ph.on.signal = v;
								const sig = signals.find((g) => g.slug === ph.on.signal);
								if (!fires(ph.on)) {
									// A person's act is the start: no policy, nothing to debounce.
									ph.on.start = undefined;
									ph.on.quiet_for = undefined;
								} else if (!sig?.debounced) {
									ph.on.quiet_for = undefined;
								}
								onchange();
							}}
						/>
						<div class="help">
							{ph.on.signal === MANUAL
								? t('flow.insp.opensOnHelp.manual')
								: (phaseSig?.description ?? '')}
						</div>
					</div>
					{#if firing}
						<div class="field">
							<label for="ph-start">{t('flow.insp.start')}</label>
							<Select
								id="ph-start"
								value={ph.on.start ?? 'auto'}
								options={[
									...START_POLICIES.map((p) => ({ value: p, label: t(`flow.insp.start.${p}`) })),
									...policyParams(doc).map((p) => ({ value: '$' + p, label: '$' + p })),
									...(isParamRef(ph.on.start) && !policyParams(doc).includes(ph.on.start.slice(1))
										? [{ value: ph.on.start, label: ph.on.start }]
										: [])
								]}
								onchange={(v) => {
									ph.on.start = v === 'auto' ? undefined : v;
									onchange();
								}}
							/>
							<div class="help">
								{t(ph.kind === 'outcome' ? 'flow.insp.startHelpOutcome' : 'flow.insp.startHelp')}
							</div>
						</div>
						{#if phaseSig?.debounced || ph.on.quiet_for}
							<div class="field">
								<label for="ph-quiet">{t('flow.insp.quietFor')}</label>
								<OptionalDuration
									id="ph-quiet"
									label={t('flow.insp.quietFor')}
									bind:value={ph.on.quiet_for}
									empty={t('flow.insp.noQuiet')}
									fallback="1h"
									params={durationParams(doc)}
									{onchange}
								/>
								<div class="help">{t('flow.insp.quietForHelp')}</div>
							</div>
						{/if}
					{/if}
					{#if ph.kind === 'outcome'}
						<div class="field">
							<label for="ph-then">{t('flow.insp.then')}</label>
							<Select
								id="ph-then"
								value={ph.then ?? DONE}
								options={[
									{ value: DONE, label: t('flow.insp.then.done') },
									{ value: ABORT, label: t('flow.insp.then.abort') }
								]}
								onchange={(v) => setThen(ph, v)}
							/>
							<div class="help">
								{t(phaseStageCount ? 'flow.insp.outcomeStagesHelp' : 'flow.insp.phaseThenHelp')}
							</div>
						</div>
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
					{@const choices = [
						...(hasMain(doc) ? [MAIN] : []),
						...(doc.sequences ?? []).map((q) => q.id)
					]}
					<div class="field">
						<label for="ph-runs">{t('flow.insp.runs')}</label>
						<!-- Bound through phaseRuns: a replacement refused leaves the field on
						     what the chain runs. -->
						<Select
							id="ph-runs"
							bind:value={() => phaseRuns, (v) => void setRuns(ph, v)}
							options={[
								{
									value: '',
									label:
										ph.kind === 'outcome' && !phaseStageCount
											? t('flow.insp.runsNothing')
											: t('flow.insp.runsOwn', { count: phaseStageCount })
								},
								...choices.map((id) => ({
									value: id,
									label: id === MAIN ? t('flow.insp.runsMain') : t('flow.insp.runsSequence', { id })
								}))
							]}
						/>
						<div class="help">
							{choices.length ? t('flow.insp.runsHelp') : t('flow.insp.runsHelpNone')}
						</div>
					</div>
					{@render addTo(ph)}
				{/if}
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
			{:else if seqSel}
				{@const q = seqSel}
				{@const users = runners((x) => x.type === 'sequence' && x.sequence === q.id)}
				<div class="ih">
					<span class="pill sm flat sq mono t-{STAGE_HUE.sequence}"
						>{t('flow.insp.kind.sequence')}</span
					>
					<span class="mono iid ell">{q.id}</span>
					<span class="spacer"></span>
					<span class="fnt tiny">{t('flow.insp.phaseStages', { count: q.stages.length })}</span>
					<button
						class="btn-danger btn-sm"
						disabled={users.length > 0}
						onclick={() => deleteSequence(q)}
						title={users.length ? t('flow.insp.sequenceInUse') : t('flow.insp.deleteSequence')}
						>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="help">{t('flow.insp.sequencePanelHelp')}</div>
				<div class="field">
					<label for="sq-id">{t('flow.insp.id')}</label>
					<input
						id="sq-id"
						class="mono"
						value={q.id}
						onchange={(e) =>
							renamed(e.currentTarget, q.id, renameSequenceTo(q.id, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.sequenceRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="sq-desc">{t('flow.insp.description')}</label>
					<input
						id="sq-desc"
						value={q.description ?? ''}
						oninput={(e) => {
							q.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.sequenceStages')}</div>
					{#each q.stages as x, i (x.id)}
						<div class="seq-line">
							<button
								class="rowbtn seq-row"
								onclick={() => onopen({ kind: 'stage', id: x.id, sequence: q.id })}
							>
								<StageChip type={x.type} />
								<span class="mono">{x.id}</span>
								{#if x.sequence}<span class="mut small">→ {x.sequence}</span>{/if}
							</button>
							<button
								class="btn-icon"
								disabled={i === 0}
								title={t('flow.insp.moveUp')}
								aria-label={t('flow.insp.moveUp')}
								onclick={() => {
									moveSequenceStage(doc, q.id, x.id, -1);
									onchange();
								}}>↑</button
							>
							<button
								class="btn-icon"
								disabled={i === q.stages.length - 1}
								title={t('flow.insp.moveDown')}
								aria-label={t('flow.insp.moveDown')}
								onclick={() => {
									moveSequenceStage(doc, q.id, x.id, 1);
									onchange();
								}}>↓</button
							>
						</div>
					{/each}
					<div class="help">{t('flow.insp.sequenceOrderHelp')}</div>
				</div>
				<div class="lbl">{t('flow.insp.addToSequence')}</div>
				<div class="chips">
					{#each addKinds({ sequence: q.id }) as kind (kind)}
						<button class="btn-sm" onclick={() => addToSequence(kind, q.id)}
							><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
								kind}</button
						>
					{/each}
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.runBy')}</div>
					{#if users.length}
						<div class="chips">
							{#each users as u (u.label)}<button class="btn-sm mono" onclick={() => onopen(u.sel)}
									>{u.label}</button
								>{/each}
						</div>
					{:else}
						<div class="help">{t('flow.insp.sequenceUnused')}</div>
					{/if}
				</div>
			{:else if schemaSel}
				{@const q = schemaSel}
				{@const users = schemaUsers(doc, q.name)}
				<div class="ih">
					<span class="pill sm flat sq mono t-az">{t('flow.insp.kind.schema')}</span>
					<span class="mono iid ell">{q.name}</span>
					<span class="spacer"></span>
					<button
						class="btn-danger btn-sm"
						disabled={users.length > 0}
						onclick={() => deleteSchema(q.name)}
						title={users.length ? t('flow.insp.schemaInUse') : t('flow.insp.deleteSchema')}
						>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="help">{t('flow.insp.schemaPanelHelp')}</div>
				<div class="field">
					<label for="sc-name">{t('flow.insp.schemaName')}</label>
					<input
						id="sc-name"
						class="mono"
						value={q.name}
						onchange={(e) =>
							renamed(e.currentTarget, q.name, renameSchemaTo(q.name, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.schemaRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="sc-desc">{t('flow.insp.description')}</label>
					<input
						id="sc-desc"
						value={q.description ?? ''}
						placeholder={t('flow.insp.schemaDescPlaceholder')}
						oninput={(e) => {
							q.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="field">
					<div class="lbl">{t('flow.insp.schemaBody')}</div>
					<div class="schema-body card-in">
						<YamlEditor bind:value={schemaText} label={t('flow.insp.schemaBody')} />
					</div>
					{#if schemaError}
						<div class="help hue-amb">
							{t('flow.insp.schemaBodyInvalid', { error: schemaError })}
						</div>
					{/if}
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.schemaUsedBy')}</div>
					{#if users.length}
						<div class="chips">
							{#each users as u, i (i)}{#if 'agent' in u}<button
										class="btn-sm mono"
										onclick={() => onopen({ kind: 'agent', name: u.agent })}
										>{u.agent}.{u.port}</button
									>{:else}<button
										class="btn-sm mono"
										onclick={() => onopen({ kind: 'stage', id: u.stage, sequence: u.sequence })}
										>{u.stage}.{u.port}</button
									>{/if}{/each}
						</div>
					{:else}
						<div class="help">{t('flow.insp.schemaUnused')}</div>
					{/if}
				</div>
			{:else if mcpSel}
				{@const m = mcpSel}
				{@const users = mcpUsers(doc, m.name)}
				{@const kind = mcpKind(m)}
				<div class="ih">
					<span class="pill sm flat sq mono t-tea">{t('flow.insp.kind.mcp')}</span>
					<span class="mono iid ell">{m.name}</span>
					<span class="spacer"></span>
					<button
						class="btn-danger btn-sm"
						onclick={() => deleteMCP(m.name)}
						title={t('flow.insp.deleteMCP')}>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="help">{t('flow.insp.mcpPanelHelp')}</div>
				<div class="field">
					<label for="mc-name">{t('flow.insp.schemaName')}</label>
					<input
						id="mc-name"
						class="mono"
						value={m.name}
						onchange={(e) =>
							renamed(e.currentTarget, m.name, renameMCPTo(m.name, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.mcpRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="mc-desc">{t('flow.insp.description')}</label>
					<input
						id="mc-desc"
						value={m.description ?? ''}
						placeholder={t('flow.insp.mcpDescPlaceholder')}
						oninput={(e) => {
							m.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="field">
					<label for="mc-kind">{t('flow.insp.mcpKind')}</label>
					<Select
						id="mc-kind"
						value={kind}
						options={MCP_KINDS.map((k) => ({ value: k, label: t(`flow.insp.mcpKind.${k}`) }))}
						onchange={(v) => setMCPKind(m, v as MCPKind)}
					/>
				</div>
				{#if kind === 'builtin'}
					<div class="field">
						<label for="mc-builtin">{t('flow.insp.mcpBuiltin')}</label>
						<Select
							id="mc-builtin"
							value={m.builtin ?? ''}
							options={[
								...MCP_BUILTINS.map((b) => ({
									value: b,
									label: t(`flow.insp.mcpBuiltin.${b}` as 'flow.insp.mcpBuiltin.browser')
								})),
								...(m.builtin && !MCP_BUILTINS.includes(m.builtin)
									? [{ value: m.builtin, label: m.builtin }]
									: [])
							]}
							onchange={(v) => {
								m.builtin = v;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.mcpBuiltinHelp')}</div>
					</div>
				{:else if kind === 'command'}
					<div class="field">
						<label for="mc-command">{t('flow.insp.mcpCommand')}</label>
						<textarea
							id="mc-command"
							class="mono"
							rows="3"
							bind:value={mcpCommand}
							placeholder={'npx\n-y\n@modelcontextprotocol/server-github'}></textarea>
						<div class="help">{t('flow.insp.mcpCommandHelp')}</div>
					</div>
				{:else}
					<div class="field">
						<label for="mc-url">{t('flow.insp.mcpUrl')}</label>
						<input
							id="mc-url"
							class="mono"
							type="url"
							value={m.url ?? ''}
							placeholder="https://mcp.example.com/"
							oninput={(e) => {
								m.url = e.currentTarget.value.trim();
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.mcpUrlHelp')}</div>
					</div>
				{/if}
				{#if kind !== 'builtin'}
					<div class="field">
						<div class="lbl">
							{kind === 'url' ? t('flow.insp.mcpHeaders') : t('flow.insp.mcpEnv')}
						</div>
						<KeyValueEditor
							bind:rows={mcpRows}
							keyPlaceholder={kind === 'url' ? 'Authorization' : 'TOKEN'}
							valuePlaceholder={kind === 'url' ? 'Bearer ${secrets.name}' : '${secrets.name}'}
							keyWidth={120}
							addLabel={kind === 'url' ? t('flow.insp.mcpAddHeader') : t('flow.insp.mcpAddEnv')}
							invalid={badKeys}
						/>
						<div class="help">{t('flow.insp.mcpSecretsHelp')}</div>
					</div>
				{/if}
				<div class="grp">
					<div class="lbl">{t('flow.insp.mcpUsedBy')}</div>
					{#if users.length}
						<div class="chips">
							{#each users as name (name)}<button
									class="btn-sm mono"
									onclick={() => onopen({ kind: 'agent', name })}>{name}</button
								>{/each}
						</div>
					{:else}
						<div class="help">{t('flow.insp.mcpUnused')}</div>
					{/if}
				</div>
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
				<div class="field">
					<label for="fl-operates">{t('flow.insp.operatesOn')}</label>
					<Select
						id="fl-operates"
						value={doc.operates_on ?? 'ticket'}
						options={OPERATES_ON.map((r) => ({ value: r, label: t(`flow.insp.operatesOn.${r}`) }))}
						onchange={(v) => setOperatesOn(v)}
					/>
					<div class="help">{t('flow.insp.operatesOnHelp')}</div>
				</div>
				<div class="field">
					<label for="fl-ask">{t('flow.insp.ask')}</label>
					<input
						id="fl-ask"
						list="flow-params"
						value={doc.ask?.harness ?? ''}
						placeholder={t('flow.insp.askPlaceholder')}
						oninput={(e) => {
							const v = e.currentTarget.value.trim();
							doc.ask = v ? { harness: v } : undefined;
							onchange();
						}}
					/>
					<div class="help">{t('flow.insp.askHelp')}</div>
				</div>
				<div class="lbl">{t('flow.insp.params')}</div>
				<div class="help">{t('flow.insp.paramsHelp')}</div>
				<ParamsEditor {doc} {onchange} />
				<div class="lbl">{t('flow.insp.schemas')}</div>
				<div class="help">{t('flow.insp.schemasHelp')}</div>
				<div class="chips">
					{#each doc.schemas ?? [] as q (q.name)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'schema', name: q.name })}>{q.name}</button
						>{/each}
					<button class="btn-sm" onclick={newSchema}>{I.add} {t('flow.insp.addSchema')}</button>
				</div>
				<div class="lbl">{t('flow.insp.mcpServers')}</div>
				<div class="help">{t('flow.insp.mcpServersHelp')}</div>
				<div class="chips">
					{#each doc.mcp_servers ?? [] as m (m.name)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'mcp', name: m.name })}>{m.name}</button
						>{/each}
					<button class="btn-sm" onclick={newMCP}>{I.add} {t('flow.insp.addMCP')}</button>
				</div>
				<div class="lbl">{t('flow.insp.agents')}</div>
				<div class="help">{t('flow.insp.agentsHelp')}</div>
				<div class="chips">
					{#each doc.agents ?? [] as a (a.name)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'agent', name: a.name })}>{a.name}</button
						>{/each}
					<button class="btn-sm" onclick={() => newAgent()}
						>{I.add} {t('flow.insp.addAgent')}</button
					>
				</div>
				<div class="lbl">{t('flow.insp.sequences')}</div>
				<div class="help">{t('flow.insp.sequencesHelp')}</div>
				<div class="chips">
					{#each doc.sequences ?? [] as q (q.id)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'sequence', id: q.id })}>{q.id}</button
						>{/each}
					<button class="btn-sm" onclick={newSequence}>{I.add} {t('flow.insp.addSequence')}</button>
				</div>
				<div class="lbl">{t('flow.insp.addChain')}</div>
				<div class="chips">
					{#if !mainPhase}
						<button class="btn-sm" onclick={newMainChain}
							><span class="ldot sm flat hue-brand"></span>{t('flow.insp.addMain')}</button
						>
					{/if}
					<button class="btn-sm" onclick={() => addChain('watcher')}
						><span class="ldot sm flat hue-vio"></span>{t('flow.insp.addWatcher')}</button
					>
					<button class="btn-sm" onclick={() => addChain('outcome')}
						><span class="ldot sm flat hue-grn"></span>{t('flow.insp.addOutcome')}</button
					>
				</div>
				<div class="help">
					{t(mainPhase ? 'flow.insp.addChainHelp' : 'flow.insp.addChainHelpNoMain')}
				</div>
				{#if doc.phases?.length}
					{@const into = mainPhase ?? doc.phases[0]}
					<div class="lbl">
						{into.kind === 'main'
							? t('flow.insp.addStageMain')
							: t('flow.insp.addStageTo', { id: into.id })}
					</div>
					<div class="chips">
						{#each addKinds({ phase: into.id }) as kind (kind)}
							<button class="btn-sm" onclick={() => addStage(kind)}
								><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
									kind}</button
							>
						{/each}
					</div>
				{/if}
				<div class="help">{t('flow.insp.canvasHelp')}</div>
			{/if}
		</div>
	{/key}
</div>

<!-- A chain's own way to grow: a stage of each kind, appended after its
     last stage and wired in. -->
{#snippet addTo(ph: DocPhase)}
	<div class="lbl">{t('flow.insp.addToChain')}</div>
	<div class="chips">
		{#each addKinds({ phase: ph.id }) as kind (kind)}
			<button class="btn-sm" onclick={() => addToChain(kind, ph.id)}
				><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ?? kind}</button
			>
		{/each}
	</div>
	<div class="help">{t('flow.insp.addToChainHelp')}</div>
{/snippet}

<style>
	/* The inspector sits on control glass, given by the page (DESIGN 7.8). */
	.insp {
		display: grid;
		padding: 12px 14px 16px;
		font-size: var(--fs-ui);
	}
	/* A sequence stage's sequence, stage by stage, as it will run. */
	.seq-row {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		padding: 2px 0;
	}
	.seq-row.rowbtn {
		padding: 4px 6px;
	}
	.seq-line {
		display: flex;
		align-items: center;
		gap: 2px;
	}
	/* An agent's declared input or output: its name, then what describes it. */
	.port {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.prompt {
		min-height: 180px;
		resize: vertical;
	}
	.lbl-in {
		font-size: var(--fs-label, inherit);
	}
	/* The way back to the panel this one was opened from. */
	.back {
		align-self: flex-start;
		margin: -4px 0 -2px -6px;
		color: var(--mut);
	}
	/* A stage's routes: one row per link, each in its route's hue. */
	.routes {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.route {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 6px 8px;
		border-radius: var(--r-row);
		border: 1px solid var(--ln);
		transition:
			border-color var(--dur-fade) var(--ease-out),
			box-shadow var(--dur-fade) var(--ease-out);
	}
	/* The route an edge picked on the canvas. */
	.route.hl {
		border-color: var(--brandLn);
		box-shadow: 0 0 0 3px var(--brandBg);
	}
	.link {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.rk {
		flex: none;
		width: 84px;
		font-size: var(--fs-id);
		font-weight: 600;
	}
	.rk.then {
		padding-left: 12px;
		opacity: 0.75;
	}
	.to {
		flex: 1;
		min-width: 0;
	}
	.x {
		color: var(--mut);
	}
	.max {
		width: 56px;
	}
	.unl {
		min-width: 28px;
		padding-inline: 6px;
	}
	.add-then {
		align-self: flex-start;
		margin-left: 84px;
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
	.schema-body {
		height: 260px;
		padding: 0;
		overflow: hidden;
	}
</style>
