// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).
//
// A ping's TYPE says who asked and why (a stage's gate, a phase
// confirmation, a thread selection, an agent's clarification/blocker/
// permission); its ANSWER KIND says what shape the answer takes. The card
// renders the kind; the type only colours the badge and tunes the wording.
import type { AnswerKind, PingOption } from '$lib/domain';

export const GATE_TYPE = 'gate';
export const PHASE_CONFIRM_TYPE = 'phase_confirm';
export const SELECT_TYPE = 'select';
export const SELECT_NONE = 'none';
export const isPhaseConfirm = (type: string) => type === PHASE_CONFIRM_TYPE;
/** A question an agent raised through ask_human — the kind a well-briefed
 *  agent should not need (shown as "unwanted"); everything else the engine
 *  asks on purpose. */
export const isAgentPing = (type: string) =>
	type === 'clarification' || type === 'blocker' || type === 'permission';
export const isWantedPing = (type: string) => !isAgentPing(type);

/** An option as the wire or an event payload may carry it — a bare string
 *  (older pings) or {id, label, detail} — in the frontend's shape. */
export function toPingOption(o: unknown): PingOption {
	if (typeof o === 'string') return { id: o, label: o };
	const r = (o ?? {}) as { id?: string; label?: string; detail?: string };
	const id = r.id ?? '';
	return { id, label: r.label || id, detail: r.detail || undefined };
}

/** The declared answer kind, inferred (as the engine does) when the source
 *  predates the field. */
export function answerKindOf(kind: unknown, type: string, options: unknown): AnswerKind {
	if (kind === 'decision' || kind === 'choice' || kind === 'multi' || kind === 'text') return kind;
	if (type === 'gate' || type === 'phase_confirm') return 'decision';
	if (type === 'select') return 'multi';
	return Array.isArray(options) && options.length ? 'choice' : 'text';
}

// PingLike is the subset PingCard needs, in the frontend's own vocabulary: a
// domain Ping satisfies it as-is, and the run page maps its raw ping_raised
// event payload onto it. Missing status ⇒ pending.
export interface PingLike {
	id: string;
	type: string;
	answerKind: AnswerKind;
	question: string;
	context?: string;
	// Typed payload (DESIGN-FILES §4): files = attached workspace paths,
	// link = a resolved URL; primaryRef = the derived primary for display.
	files?: string[];
	link?: string;
	primaryRef?: string;
	options?: PingOption[];
	status?: string; // pending | answered | withdrawn
	answer?: string;
	feedback?: string;
	answeredAt?: string;
	// Optional: shown in the card head when present.
	stageId?: string;
	raisedAt?: string;
}

/** A ping whose primary payload is a captured document (a plan), reviewed in
 *  the document workbench rather than answered inline. */
export const isDocumentPing = (p: Pick<PingLike, 'files' | 'link' | 'primaryRef'>) =>
	!p.link && !!(p.files?.length || p.primaryRef);

/** The call to action a ping row offers (DESIGN 7.2): a plan to review, a
 *  decision to make, or a question to answer. */
export function pingCta(
	p: Pick<PingLike, 'type' | 'answerKind' | 'files' | 'link' | 'primaryRef'>
): string {
	if (isDocumentPing(p)) return 'Review plan';
	if (p.type === 'permission' || p.answerKind === 'decision') return 'Decide';
	return 'Answer';
}

/** The first line of a ping's question as plain text, for one-line rows. */
export function pingHeadline(question: string): string {
	const line =
		question
			.split('\n')
			.map((l) => l.trim())
			.find((l) => l && !/^```/.test(l)) ?? '';
	return line
		.replace(/^#+\s*|^[-*>]\s+/, '')
		.replace(/[*_`]+/g, '')
		.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1');
}
