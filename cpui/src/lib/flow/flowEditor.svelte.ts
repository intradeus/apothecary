// The flow editor's state (DESIGN 7.8): the flow, its document and its text, and
// everything that changes or saves them. What is selected lives in FlowSelection.
// FlowEditorPage wires this to the screen. One per flow: the route's layout keys
// the page on the flow's name.
import { errorMessage } from '$lib/core/errors';
import { goto } from '$app/navigation';
import type { BeforeNavigate } from '@sveltejs/kit';
import { api } from '$lib/api';
import type { ActionSpec, Flow, FlowVersion, SignalSpec } from '$lib/domain';
import { to } from '$lib/core/urls';
import { t, th } from '$lib/i18n/index.svelte';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toast } from '$lib/ui/toast.svelte';
import {
	autoLayout,
	docToYaml,
	placeChain,
	type FlowDoc,
	type NodeSize
} from '$lib/domain/flowdoc';
import { FlowSelection } from '$lib/flow/flowSelection.svelte';
import { rewire } from '$lib/flow/graph/rewire';

export type EditorTab = 'graph' | 'agents' | 'yaml';

/** The last validation (or save) result, shown in the Validate popover. */
export interface Validation {
	ok: boolean;
	title: string;
	stages?: number;
	hash?: string;
	errors?: string[];
	warnings?: string[];
}

export class FlowEditor {
	flow = $state<Flow | null>(null);
	yaml = $state('');
	doc = $state<FlowDoc | null>(null);
	layout = $state<Record<string, { x: number; y: number }>>({});
	rev = $state(0);
	// Graph and Agents edit the same document; YAML its text.
	tab = $state<EditorTab>('graph');
	// The agent open in the Agents tab.
	agentOpen = $state<string | undefined>();
	graphDirty = $state(false);
	versions = $state<FlowVersion[]>([]);
	error = $state('');
	busy = $state(false);
	validation = $state<Validation | null>(null);
	/** A one-line note over the canvas (a version loaded into the editor). */
	notice = $state<{ tone: 'ok' | 'info'; text: string } | null>(null);
	/** A validation request is in flight (aria-busy on Validate). */
	validating = $state(false);
	// The compiled-in action registry: what an `action` stage may name, and the
	// signature the inspector and the dataflow layer bind against.
	actions = $state<ActionSpec[]>([]);
	// The signal registry: what a wait stage blocks on, or a gate auto-answers on.
	signals = $state<SignalSpec[]>([]);
	// The flow document's JSON Schema: what the YAML tab checks as you type.
	flowSchema = $state<Record<string, unknown> | undefined>();

	validateOpen = $state(false);
	showVersions = $state(false);
	cloning = $state(false);

	/** What is selected, and the sequence open beside the flow. */
	readonly panels = new FlowSelection(this);

	// Each change fits the flow's canvas again.
	mainRefit = $state(0);

	// The layout was guessed by autoLayout (no stored one); it is redone once
	// the graph has measured its nodes.
	#autoPlaced = false;
	#layoutTimer: ReturnType<typeof setTimeout> | undefined;
	/** Counts saves, so a layout autosave never overwrites a newer flow. */
	#saves = 0;
	/** A node moved while an explicit save ran: the save took its layout before. */
	#movedDuringSave = false;
	#leaving = false;

	// The workshop that owns this flow. A flow's stages name that workshop's
	// commands and its agents know that repository, so the editor is always
	// editing one workshop's document — never a shared one.
	readonly #projectId: () => string;
	readonly #name: () => string;

	constructor(projectId: () => string, name: () => string) {
		this.#projectId = projectId;
		this.#name = name;
	}

	get projectId() {
		return this.#projectId();
	}
	/** The name the flow is opened under. */
	get name() {
		return this.#name();
	}

	// The document itself is still validated on its own terms: agent, harness,
	// model and run_ref are free text here, checked against the workshop's
	// configuration at run creation (preflight).

	readonly dirty = $derived(
		this.flow !== null && (this.graphDirty || this.yaml !== this.flow.yaml)
	);
	readonly stageCount = $derived(this.doc?.stages.length ?? 0);

	load = async () => {
		this.error = '';
		try {
			this.flow = await api.flows.get(this.projectId, this.name);
			this.yaml = this.flow.yaml;
			this.versions = await api.flows.versions(this.projectId, this.flow.name);
			const r = await api.flows.validate(this.flow.yaml);
			if (r.ok && r.doc) {
				this.doc = r.doc;
				this.layout = this.flow.layout?.nodes ?? autoLayout(this.doc);
				this.#autoPlaced = !this.flow.layout?.nodes;
				if (r.warnings?.length)
					this.validation = {
						ok: true,
						title: t('page.flow.validWarn'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					};
				this.rev++;
			} else {
				this.tab = 'yaml';
				this.validation = { ok: false, title: t('page.flow.noParse'), errors: r.errors ?? [] };
				this.validateOpen = true;
			}
		} catch (e) {
			this.error = errorMessage(e);
		}
		// Contracts for the binding fields and the dataflow view. Best-effort:
		// without them the graph still renders explicit bindings.
		try {
			[this.actions, this.signals] = await Promise.all([
				api.system.actions(),
				api.system.signals()
			]);
		} catch {
			/* binding hints stay off */
		}
		api.flows.schema().then(
			(s) => (this.flowSchema = s),
			() => {
				/* the YAML tab still has the parser's checks */
			}
		);
	};

	#currentYAML(): string {
		if (this.doc && (this.tab !== 'yaml' || this.graphDirty)) return docToYaml(this.doc);
		return this.yaml;
	}

	markChanged = () => {
		this.graphDirty = true;
		this.notice = null;
		this.#placeMarkers();
		this.rev++;
		this.panels.relayoutSequence();
	};

	// placeMarkers makes room for a chain added since the layout was made —
	// in the inspector or the YAML — at the end of its section; everything
	// placed keeps its place, the sections below moving down. (A stage added
	// to an existing chain is placed by the graph, after its chain's last.)
	#placeMarkers() {
		if (!this.doc) return;
		for (const ph of this.doc.phases ?? []) placeChain(this.doc, this.layout, ph.id);
	}

	switchTab = (next: EditorTab) => {
		if (next === this.tab) return;
		if (next === 'yaml') {
			if (this.doc && this.graphDirty) this.yaml = docToYaml(this.doc);
			this.tab = 'yaml';
			return;
		}
		// Graph and Agents share the document: nothing to re-read between them.
		if (this.tab !== 'yaml') {
			this.tab = next;
			return;
		}
		// yaml -> graph: re-parse through the server (single validation source).
		api.flows
			.validate(this.yaml)
			.then((r) => {
				if (r.ok && r.doc) {
					this.doc = r.doc;
					// Place stages added in the YAML editor via the success-chain layout
					// rather than dropping them all on one point (which overlaps them).
					const auto = autoLayout(this.doc);
					for (const s of this.doc.stages)
						if (!(s.id in this.layout)) this.layout[s.id] = auto[s.id];
					this.#placeMarkers();
					this.graphDirty = false;
					this.validation = null;
					this.rev++;
					this.tab = next;
				} else {
					this.tab = 'yaml';
					this.validation = {
						ok: false,
						title: t('page.flow.fixYaml'),
						errors: r.errors?.length ? r.errors : [t('page.flow.invalidFlow')]
					};
					this.validateOpen = true;
				}
			})
			.catch((e) => {
				this.validation = {
					ok: false,
					title: t('page.flow.validationFailed'),
					errors: [String(e)]
				};
				this.validateOpen = true;
			});
	};

	onlink = (source: string, handle: string, target: string) => {
		if (!this.doc) return;
		const r = rewire(this.doc, source, handle, target);
		if (r === 'changed') this.markChanged();
		else if (r !== 'same') this.notice = { tone: 'info', text: t('page.flow.crossPhase', r) };
	};

	onmove = (id: string, x: number, y: number) => {
		this.layout[id] = { x, y };
		if (this.busy) this.#movedDuringSave = true;
		this.#autosaveLayout();
	};

	#autosaveLayout() {
		// Layout-only autosave (DESIGN3 §2.2): same YAML = same hash = no new
		// version; deferred while real edits are pending (they save together).
		clearTimeout(this.#layoutTimer);
		this.#layoutTimer = setTimeout(async () => {
			if (!this.flow || this.dirty || this.flow.builtin) return;
			const seq = ++this.#saves;
			try {
				const saved = await api.flows.save(this.projectId, this.flow.name, this.flow.yaml, {
					nodes: this.layout
				});
				// An explicit save started meanwhile holds the newer flow.
				if (seq === this.#saves) this.flow = saved;
			} catch {
				/* next explicit save carries it */
			}
		}, 800);
	}

	// autoArrange re-runs the success-chain layout with the nodes' measured
	// sizes, discarding manual positions, and persists the fresh map (via
	// onmove's layout-only autosave path).
	autoArrange = (sizes: Record<string, NodeSize> = {}) => {
		if (!this.doc) return;
		this.layout = autoLayout(this.doc, sizes);
		this.rev++;
		this.mainRefit++; // and show the whole of it
		const first = this.doc.stages[0]?.id;
		if (first) this.onmove(first, this.layout[first].x, this.layout[first].y);
	};

	// onmeasured: a flow opened without a stored layout was placed with guessed
	// sizes; now that the nodes are measured, place them for real. Not
	// persisted — nothing was dragged; the next drag or arrange saves.
	onmeasured = (sizes: Record<string, NodeSize>) => {
		if (!this.doc || !this.#autoPlaced) return;
		this.#autoPlaced = false;
		this.layout = autoLayout(this.doc, sizes);
		this.rev++;
	};

	// An agent is edited in the Agents tab, wherever it is opened from — a
	// stage's "Edit agent", the flow panel's list, a schema's users.
	openAgent = (name: string) => {
		this.agentOpen = name;
		this.switchTab('agents');
	};

	validate = async () => {
		this.validating = true;
		try {
			const r = await api.flows.validate(this.#currentYAML());
			this.validation = r.ok
				? {
						ok: true,
						title: r.warnings?.length ? t('page.flow.validWarn') : t('page.flow.valid'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					}
				: { ok: false, title: t('page.flow.invalid'), errors: r.errors ?? [] };
			if (r.ok)
				toast(
					r.warnings?.length
						? t('page.flow.validToastWarn', {
								count: r.stages ?? this.stageCount,
								warnings: r.warnings.length
							})
						: t('page.flow.validToast', { count: r.stages ?? this.stageCount }),
					{ tone: r.warnings?.length ? 'warn' : 'ok' }
				);
		} catch (e) {
			this.validation = {
				ok: false,
				title: t('page.flow.validationFailed'),
				errors: [errorMessage(e)]
			};
		} finally {
			this.validating = false;
		}
		this.validateOpen = true;
	};

	save = async () => {
		if (!this.flow || this.busy || !this.dirty) return;
		this.busy = true;
		this.error = '';
		clearTimeout(this.#layoutTimer); // the layout goes with this save
		this.#movedDuringSave = false;
		this.#saves++;
		const wasBuiltin = this.flow.builtin;
		try {
			const y = this.#currentYAML();
			this.flow = await api.flows.save(this.projectId, this.flow.name, y, { nodes: this.layout });
			this.yaml = this.flow.yaml;
			// The engine took it: what follows reads it back, and a failure there
			// says nothing about the save.
			const r = await api.flows.validate(this.flow.yaml).catch(() => null);
			if (r?.ok && r.doc) {
				this.doc = r.doc;
				this.rev++;
			}
			this.graphDirty = false;
			this.validation = r?.warnings?.length
				? {
						ok: true,
						title: t('page.flow.savedWarn'),
						stages: r.stages,
						hash: this.flow.hash,
						warnings: r.warnings
					}
				: null;
			this.notice = null;
			toast(
				wasBuiltin
					? t('page.flow.savedCopy', { hash: this.flow.hash.slice(0, 8) })
					: t('page.flow.saved', { hash: this.flow.hash.slice(0, 8) })
			);
			this.versions = await api.flows
				.versions(this.projectId, this.flow.name)
				.catch(() => this.versions);
			if (this.#movedDuringSave) this.#autosaveLayout();
		} catch (e) {
			this.validation = { ok: false, title: t('page.flow.saveRefused'), errors: [errorMessage(e)] };
			this.validateOpen = true;
		} finally {
			this.busy = false;
		}
	};

	// Clone rewrites the immutable top-level name (docToYaml/templates always
	// emit `name:` at column 0) and creates a fresh flow from the current editor
	// content plus node positions. Works on built-ins too — fork a default under
	// your own name without shadowing it.
	clone = async (name: string) => {
		if (!this.flow) return;
		const y = this.#currentYAML().replace(/^name:[^\n]*$/m, `name: ${name}`);
		await api.flows.create(this.projectId, name, y, { nodes: this.layout });
		this.cloning = false;
		toast(t('page.flow.cloned', { name }));
		this.#leaving = true;
		await goto(to.flow(this.projectId, name));
	};

	loadVersion = async (v: FlowVersion) => {
		if (
			this.dirty &&
			!(await confirmDialog({
				title: t('page.flow.replaceTitle'),
				body: th('page.flow.replaceBody', { hash: v.hash.slice(0, 8) }),
				action: t('page.flow.loadVersion'),
				tone: 'warn'
			}))
		)
			return;
		try {
			const full = await api.flows.version(this.projectId, v.hash);
			this.yaml = full.yaml; // into the YAML tab; Save makes it current again
			this.tab = 'yaml';
			this.graphDirty = false;
			this.showVersions = false;
			this.notice = {
				tone: 'info',
				text: t('page.flow.loaded', { hash: v.hash.slice(0, 8) })
			};
		} catch (e) {
			this.error = errorMessage(e);
		}
	};

	remove = async () => {
		if (!this.flow) return;
		const name = this.flow.name;
		const reset = this.flow.shadowsBuiltin;
		const ok = await confirmDialog(
			reset
				? {
						title: t('page.flow.resetTitle', { name }),
						body: th('page.flow.resetBody', { name }),
						action: t('page.flow.resetAction')
					}
				: {
						title: t('page.flow.deleteTitle', { name }),
						body: th('page.flow.deleteBody', { name }),
						action: t('page.flow.deleteAction')
					}
		);
		if (!ok) return;
		try {
			await api.flows.remove(this.projectId, name);
			toast(reset ? t('page.flow.resetDone', { name }) : t('page.flow.deleted', { name }));
			this.#leaving = true;
			await goto(to.workshop(this.projectId, 'flows'));
		} catch (e) {
			this.error = errorMessage(e);
		}
	};

	// Leaving with unsaved changes asks first (DESIGN 4.9).
	beforeLeave = (nav: BeforeNavigate) => {
		if (!this.dirty || this.#leaving) return;
		if (nav.type === 'leave') {
			nav.cancel(); // the browser shows its own "leave site?" prompt
			return;
		}
		nav.cancel();
		const dest = nav.to?.url;
		void confirmDialog({
			title: t('page.flow.leaveTitle'),
			body: th('page.flow.leaveBody', { name: this.flow?.name ?? '' }),
			action: t('page.flow.leaveAction')
		}).then((ok) => {
			if (ok && dest) {
				this.#leaving = true;
				void goto(dest);
			}
		});
	};
}
