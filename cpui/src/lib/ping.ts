// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).
//
// A ping's TYPE says who asked and why (a stage's gate, a phase
// confirmation, a thread selection, an agent's clarification/blocker/
// permission); its ANSWER KIND says what shape the answer takes. The card
// renders the kind; the type only colours the badge and tunes the wording.
import type { AnswerKind, PingOption, ReviewComment } from '$lib/domain';
import type { Hue } from '$lib/ui/hues';
import { I } from '$lib/ui/icons';
import { t } from '$lib/i18n/index.svelte';

export const GATE_TYPE = 'gate';
/** The flow's debug leaf: a parked stage, facts only — the card is the wording. */
export const DEBUG_TYPE = 'debug';
export const isDebug = (type: string) => type === DEBUG_TYPE;
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

/** A review comment as the wire or a ping_answered payload carries it, in
 *  the frontend's shape. */
export function toReviewComment(o: unknown): ReviewComment {
	const r = (o ?? {}) as {
		path?: string;
		start_line?: number;
		end_line?: number;
		heading?: string;
		quote?: string;
		body?: string;
		attachment_ids?: string[] | null;
	};
	return {
		path: r.path ?? '',
		startLine: r.start_line || undefined,
		endLine: r.end_line || undefined,
		heading: r.heading || undefined,
		quote: r.quote || undefined,
		body: r.body ?? '',
		attachmentIds: r.attachment_ids ?? []
	};
}

/** The declared answer kind, inferred (as the engine does) when the source
 *  predates the field. */
export function answerKindOf(kind: unknown, type: string, options: unknown): AnswerKind {
	if (kind === 'yes_no' || kind === 'choice' || kind === 'multi' || kind === 'text') return kind;
	if (type === 'gate' || type === 'phase_confirm' || type === 'debug') return 'yes_no';
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
	/** The answer's comments on the files shown. */
	review?: ReviewComment[];
	answeredAt?: string;
	// Optional: shown in the card head when present.
	stageId?: string;
	raisedAt?: string;
	// The phase the ping belongs to; on a debug ping, endsPhase says giving
	// up ends that phase run, not the run.
	phase?: string;
	endsPhase?: boolean;
}

/** A ping that shows files (documents, YAML, code, images — and maybe a
 *  link beside them), reviewed in the review workbench rather than answered
 *  inline. */
export const isDocumentPing = (p: Pick<PingLike, 'files' | 'link' | 'primaryRef'>) =>
	!!(p.files?.length || (!p.link && p.primaryRef));

/** The file a review opens on: the first shown. A ping's primaryRef is its
 *  link when it has one, so the files come first. */
export const primaryFile = (p: Pick<PingLike, 'files' | 'link' | 'primaryRef'>) =>
	p.files?.[0] ?? (p.link ? '' : (p.primaryRef ?? ''));

/** The call to action a ping row offers (DESIGN 7.2): a file to review
 *  (named), a decision to make, or a question to answer. */
export function pingCta(
	p: Pick<PingLike, 'type' | 'answerKind' | 'files' | 'link' | 'primaryRef'>
): string {
	if (isDocumentPing(p)) {
		if ((p.files?.length ?? 0) > 1) return t('ping.cta.reviewFiles', { count: p.files!.length });
		const name = primaryFile(p).split('/').pop();
		return name ? t('ping.cta.reviewFile', { name }) : t('ping.cta.review');
	}
	if (p.type === 'permission' || p.answerKind === 'yes_no') return t('ping.cta.decide');
	return t('ping.cta.answer');
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

/** How an answered (or withdrawn) ping reads: the verdict's glyph, word and
 *  hue. One table for the inbox history, the card's answered line and the
 *  review page's banner. */
export function answerMeta(p: Pick<PingLike, 'status' | 'answer'>): {
	glyph: string;
	word: string;
	hue: Hue;
} {
	if (p.status === 'withdrawn')
		return { glyph: I.none, word: t('ping.verdict.withdrawn'), hue: 'mut' };
	if (p.answer === 'approve') return { glyph: I.ok, word: t('ping.verdict.approved'), hue: 'grn' };
	if (p.answer === 'reject') return { glyph: I.loop, word: t('ping.verdict.rejected'), hue: 'amb' };
	return { glyph: I.ok, word: t('ping.verdict.answered'), hue: 'grn' };
}
