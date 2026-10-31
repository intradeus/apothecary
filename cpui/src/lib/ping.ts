// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).
//
// A ping's TYPE says who asked and why (a stage's gate, a phase
// confirmation, a thread selection, an agent's clarification/blocker/
// permission); its ANSWER KIND says what shape the answer takes — a yes/no,
// or a form (ACP's elicitation form: every question). The card renders the
// kind; the type only colours the badge and tunes the wording.
import type {
	AnswerKind,
	CuratedItem,
	Curation,
	FieldType,
	FormContent,
	FormValue,
	PingField,
	PingForm,
	PingOption,
	ReviewComment
} from '$lib/domain';
import type { Hue } from '$lib/ui/hues';
import { I } from '$lib/ui/icons';
import { t } from '$lib/i18n/index.svelte';

export const GATE_TYPE = 'gate';
/** The flow's debug leaf: a parked stage, facts only — the card is the wording. */
export const DEBUG_TYPE = 'debug';
export const isDebug = (type: string) => type === DEBUG_TYPE;
export const PHASE_CONFIRM_TYPE = 'phase_confirm';
export const SELECT_TYPE = 'select';
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

/** The declared answer kind; absent, as the engine reads it: the engine's
 *  own decisions are yes/no, anything else a form. */
export function answerKindOf(kind: unknown, type: string): AnswerKind {
	if (kind === 'yes_no' || kind === 'form') return kind;
	return type === 'gate' || type === 'phase_confirm' || type === 'debug' ? 'yes_no' : 'form';
}

const FIELD_TYPES: FieldType[] = ['string', 'number', 'integer', 'boolean', 'array'];
const num = (v: unknown) => (typeof v === 'number' ? v : undefined);

/** A form as the wire or a ping_raised payload carries it, in the
 *  frontend's shape. */
export function toPingForm(o: unknown): PingForm | undefined {
	const r = o as { fields?: unknown[] | null } | null | undefined;
	if (!r || !Array.isArray(r.fields)) return undefined;
	return {
		fields: r.fields.map((raw): PingField => {
			const f = (raw ?? {}) as Record<string, unknown>;
			const type = FIELD_TYPES.includes(f.type as FieldType) ? (f.type as FieldType) : 'string';
			return {
				id: String(f.id ?? ''),
				type,
				title: (f.title as string) || undefined,
				description: (f.description as string) || undefined,
				required: f.required === true,
				options: ((f.options as unknown[] | null) ?? []).map(toPingOption),
				format: (f.format as PingField['format']) || undefined,
				pattern: (f.pattern as string) || undefined,
				minLength: num(f.min_length),
				maxLength: num(f.max_length),
				minimum: num(f.minimum),
				maximum: num(f.maximum),
				minItems: num(f.min_items),
				maxItems: num(f.max_items),
				default: (f.default ?? undefined) as FormValue | undefined
			};
		})
	};
}

/** An answer's content as the wire or a ping_answered payload carries it. */
export function toFormContent(o: unknown): FormContent | undefined {
	return o && typeof o === 'object' && !Array.isArray(o) ? (o as FormContent) : undefined;
}

/** One document's curation as the wire or a ping_answered payload carries
 *  it, in the frontend's shape. */
export function toCuration(o: unknown): Curation {
	const r = (o ?? {}) as { summary?: string | null; items?: unknown[] | null };
	return {
		summary: typeof r.summary === 'string' ? r.summary : undefined,
		items: (r.items ?? []).map((raw): CuratedItem => {
			const it = (raw ?? {}) as Record<string, unknown>;
			return {
				item: String(it.item ?? ''),
				keep: it.keep !== false,
				body: typeof it.body === 'string' && it.body !== '' ? it.body : undefined,
				resolve: typeof it.resolve === 'boolean' ? it.resolve : undefined
			};
		})
	};
}

/** An answer's curations by path, or undefined when it carried none. */
export function toCuratedMap(o: unknown): Record<string, Curation> | undefined {
	if (!o || typeof o !== 'object' || Array.isArray(o)) return undefined;
	const out: Record<string, Curation> = {};
	for (const [path, c] of Object.entries(o as Record<string, unknown>)) out[path] = toCuration(c);
	return Object.keys(out).length ? out : undefined;
}

/** The port types of a shown file a person curates at the gate: a review to
 *  post, a round's replies. The threads beside them (mr_threads) are context. */
export const CURATABLE_TYPES = ['review_threads', 'review_replies'];
export const THREADS_TYPE = 'mr_threads';

/** The review document a ping shows for curation — the first shown file of a
 *  curatable type — or undefined: the page keys on the port type alone,
 *  never on the flow or the file's name. */
export function curatable(
	p: Pick<PingLike, 'files' | 'fileTypes'>
): { path: string; type: string } | undefined {
	for (const path of p.files ?? []) {
		const type = p.fileTypes?.[path];
		if (type && CURATABLE_TYPES.includes(type)) return { path, type };
	}
	return undefined;
}

/** How the card asks a form: `text` — one free-text field, the textarea is
 *  the answer; `pick` — one field to pick one option of, a button each;
 *  `fields` — anything else, every field drawn. */
export function formShape(form: PingForm | undefined): 'text' | 'pick' | 'fields' {
	const f = form?.fields ?? [];
	if (f.length !== 1) return 'fields';
	if (f[0].type === 'string' && !f[0].format && !f[0].pattern)
		return f[0].options.length ? 'pick' : 'text';
	return 'fields';
}

/** The values a form starts with: each field's default; an array none
 *  picked unless it says otherwise. */
export function formDefaults(form: PingForm | undefined): Record<string, FormValue | undefined> {
	const out: Record<string, FormValue | undefined> = {};
	for (const f of form?.fields ?? []) {
		if (f.type === 'array') out[f.id] = Array.isArray(f.default) ? [...f.default] : [];
		else out[f.id] = f.default;
	}
	return out;
}

const blank = (v: FormValue | undefined) =>
	v === undefined || (typeof v === 'string' && v.trim() === '') || Number.isNaN(v);

/** The answers a form is sent with: blanks dropped, text trimmed. */
export function formContent(
	form: PingForm | undefined,
	values: Record<string, FormValue | undefined>
): FormContent {
	const out: FormContent = {};
	for (const f of form?.fields ?? []) {
		const v = values[f.id];
		if (blank(v)) continue;
		out[f.id] = typeof v === 'string' ? v.trim() : v!;
	}
	return out;
}

/** The required fields still unanswered (an array's empty pick is an
 *  answer — "none"). */
export function formMissing(
	form: PingForm | undefined,
	values: Record<string, FormValue | undefined>
): PingField[] {
	return (form?.fields ?? []).filter((f) => f.required && blank(values[f.id]));
}

/** What a field says it asks. */
export const fieldLabel = (f: PingField) => f.title || f.id;

/** One field's answer as a person reads it: option labels, yes/no. */
export function fieldAnswerText(f: PingField, v: FormValue | undefined): string {
	const label = (id: string) => f.options.find((o) => o.id === id)?.label ?? id;
	if (v === undefined) return '—';
	if (Array.isArray(v)) return v.length ? v.map(label).join(', ') : t('ping.noneSelected');
	if (typeof v === 'boolean') return v ? t('ping.yes') : t('ping.no');
	return typeof v === 'string' ? label(v) : String(v);
}

/** A form answer in one line, for the answered row and the transcript. */
export function answerSummary(
	p: Pick<PingLike, 'answerKind' | 'form' | 'answer' | 'content' | 'feedback'>
): string {
	if (p.answerKind !== 'form') return p.answer ?? '';
	if (p.answer === 'decline') return t('ping.verdict.declined');
	const fields = p.form?.fields ?? [];
	if (fields.length === 1) return fieldAnswerText(fields[0], p.content?.[fields[0].id]);
	return fields
		.filter((f) => p.content?.[f.id] !== undefined)
		.map((f) => `${fieldLabel(f)}: ${fieldAnswerText(f, p.content?.[f.id])}`)
		.join(' · ');
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
	/** Each structured file's port type, by path (what offers curation). */
	fileTypes?: Record<string, string>;
	link?: string;
	primaryRef?: string;
	/** What a form ping asks. */
	form?: PingForm;
	status?: string; // pending | answered | withdrawn
	answer?: string;
	feedback?: string;
	/** An accepted form's values. */
	content?: FormContent;
	/** The answer's comments on the files shown. */
	review?: ReviewComment[];
	/** What the person made of each review document shown, by path. */
	curated?: Record<string, Curation>;
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
	p: Pick<PingLike, 'type' | 'answerKind' | 'files' | 'fileTypes' | 'link' | 'primaryRef'>
): string {
	const cur = curatable(p);
	if (cur) return t('ping.cta.curate', { name: cur.path.split('/').pop() ?? cur.path });
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
	if (p.answer === 'reject')
		return { glyph: I.loop, word: t('ping.verdict.rejected'), hue: 'gold' };
	if (p.answer === 'decline')
		return { glyph: I.none, word: t('ping.verdict.declined'), hue: 'gold' };
	return { glyph: I.ok, word: t('ping.verdict.answered'), hue: 'grn' };
}
