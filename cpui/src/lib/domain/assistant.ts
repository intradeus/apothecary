// The assistant beside a page (a flow, a ticket, a skill, a run, a
// dashboard) or none (the command palette's): a person's conversation with
// an agent in their own pod, which acts two ways, both through the person:
// it fills the page's inputs, and it asks the person to confirm acts.
// Frontend-owned shapes; the mappers translate.

export type AssistantKind = 'flow' | 'ticket' | 'skill' | 'run' | 'dashboard' | 'general';

export const ASSISTANT_KINDS: readonly AssistantKind[] = [
	'flow',
	'ticket',
	'skill',
	'run',
	'dashboard',
	'general'
];

/** One act a request asks the person to confirm: a write tool of the
 *  platform and its arguments, then what came of it. */
export interface AssistantAct {
	tool: string;
	/** What the tool does, for the person. */
	title?: string;
	arguments: unknown;
	/** The route's answer once confirmed; none: not run. */
	status?: number;
	result?: string;
}

/** Acts an answer asks the person to confirm, and their state. */
export interface AssistantRequest {
	why: string;
	acts: AssistantAct[];
	/** running: confirmed, its acts running, or interrupted (never run
	 *  again). */
	state: 'pending' | 'running' | 'done' | 'declined';
}

/** One of the person's harness integrations the assistant runs on. */
export interface AssistantIntegration {
	id: string;
	name: string;
	kind: string;
}

/** One repository's folder in the person's pod, as its last fetch left it. */
export interface AssistantRepo {
	workshop: string;
	name: string;
	ok: boolean;
	head?: string;
	error?: string;
	at: string;
}

export interface AssistantStatus {
	enabled: boolean;
	/** Why it cannot run, when it cannot. */
	reason?: string;
	/** The harness integration and model a new session starts with. */
	choice: { integration: string; model: string };
	integrations: AssistantIntegration[];
	models: string[];
	pod: { state: string; error?: string };
	repos: AssistantRepo[];
	pages: Partial<Record<AssistantKind, { presets: string[]; repos: boolean }>>;
}

export interface AssistantSession {
	id: string;
	kind: AssistantKind;
	workshop?: string;
	/** The flow's name, the ticket's key, the skill's id, or a draft id. */
	subject: string;
	integration?: string;
	model?: string;
	createdAt: string;
	updatedAt: string;
}

export interface AssistantMessage {
	sessionId: string;
	seq: number;
	role: 'person' | 'assistant';
	text: string;
	/** What the answer filled the page with, when it did. */
	fill?: unknown;
	/** What it asked the person to confirm, when it did. */
	request?: AssistantRequest;
	error?: string;
	costUsd: number;
	at: string;
}

/** One event on the person's assistant stream. */
export type AssistantFrame =
	| { kind: 'message'; seq: number; sessionId: string; message: AssistantMessage }
	| { kind: 'delta'; seq: number; sessionId: string; text: string; tool?: string }
	| { kind: 'state'; seq: number; sessionId: string; busy: boolean }
	| { kind: 'status'; seq: number; sessionId: string; text: string }
	| {
			kind: 'fill';
			seq: number;
			sessionId: string;
			page: AssistantKind;
			subject: string;
			fill: unknown;
	  }
	| {
			/** A request proposed mid-answer (seq 0), or one decided (its
			 *  message, replaced). */
			kind: 'request';
			seq: number;
			sessionId: string;
			message: AssistantMessage;
	  }
	| { kind: 'repos'; seq: number; workshop: string; text?: string; repos?: AssistantRepo[] }
	| { kind: 'gap' };

// --- fills -------------------------------------------------------------------

/** A ticket form's fields a fill may set. */
export interface TicketFields {
	title: string;
	goal: string;
	changeType: string;
	typeId: string;
}

/** A skill editor's fields a fill sets. */
export interface SkillFields {
	name: string;
	files: { path: string; content: string }[];
}

/** A fill applied to what the page holds: the page's next values and the
 *  fields that changed (the changed marker). null: not this page's fill. */
export interface Filled<T> {
	next: T;
	changed: string[];
}

const isObject = (v: unknown): v is Record<string, unknown> =>
	typeof v === 'object' && v !== null && !Array.isArray(v);

/** A flow fill: the whole document. */
export function flowFill(cur: string, fill: unknown): Filled<string> | null {
	if (!isObject(fill) || typeof fill.yaml !== 'string') return null;
	return { next: fill.yaml, changed: fill.yaml === cur ? [] : ['yaml'] };
}

/** A ticket fill: only the fields it sets; the rest stay as typed. */
export function ticketFill(cur: TicketFields, fill: unknown): Filled<TicketFields> | null {
	if (!isObject(fill)) return null;
	const next = { ...cur };
	const set = (key: keyof TicketFields, wire: string) => {
		const v = fill[wire];
		if (typeof v === 'string') next[key] = v;
	};
	set('title', 'title');
	set('goal', 'goal');
	set('changeType', 'change_type');
	set('typeId', 'type_id');
	const changed = (Object.keys(next) as (keyof TicketFields)[]).filter((k) => next[k] !== cur[k]);
	return { next, changed };
}

/** A skill fill: its name and every file. */
export function skillFill(cur: SkillFields, fill: unknown): Filled<SkillFields> | null {
	if (!isObject(fill) || typeof fill.name !== 'string' || !Array.isArray(fill.files)) return null;
	const files = fill.files.filter(
		(f): f is { path: string; content: string } =>
			isObject(f) && typeof f.path === 'string' && typeof f.content === 'string'
	);
	const next: SkillFields = { name: fill.name, files: files.map((f) => ({ ...f })) };
	const changed: string[] = [];
	if (next.name !== cur.name) changed.push('name');
	const before = new Map(cur.files.map((f) => [f.path, f.content]));
	for (const f of next.files) if (before.get(f.path) !== f.content) changed.push(f.path);
	for (const f of cur.files) if (!next.files.some((n) => n.path === f.path)) changed.push(f.path);
	return { next, changed };
}

/** A new draft's subject: a session's id before the first save. */
export const draftSubject = () =>
	`draft-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

/** A run's fill: a change to a flow, taken to the flow editor. */
export interface RunFill {
	flow: string;
	yaml: string;
}

/** A run page's fill, when it is one. */
export function runFill(fill: unknown): RunFill | null {
	if (!isObject(fill) || typeof fill.flow !== 'string' || typeof fill.yaml !== 'string')
		return null;
	if (!fill.flow || !fill.yaml) return null;
	return { flow: fill.flow, yaml: fill.yaml };
}

/** A dashboard's fill: every panel, in order. */
export function dashboardFill<P>(
	cur: { panels: P[] },
	fill: unknown
): Filled<{ panels: P[] }> | null {
	if (!isObject(fill) || !Array.isArray(fill.panels)) return null;
	const next = { panels: fill.panels.filter(isObject) as P[] };
	const changed = JSON.stringify(next.panels) === JSON.stringify(cur.panels) ? [] : ['panels'];
	return { next, changed };
}
