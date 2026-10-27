// A ping: a question waiting on a human — a flow's gate, or an agent's own
// question (ask_human, or its harness's own question tool over ACP).

/** 'gate' is every human_gate stage (approve / reject with feedback; what it
 *  asks lives in its question). 'debug' is the flow's debug leaf: the failing
 *  stage is parked for you — continue re-runs it, reject abandons the run.
 *  The rest are raised by agents. */
export type PingType =
	'gate' | 'debug' | 'phase_confirm' | 'select' | 'clarification' | 'blocker' | 'permission';

export type PingStatus = 'pending' | 'answered' | 'withdrawn';

/** The shape of the answer a ping takes — declared on the ping, rendered by
 *  the card, validated by the engine from the same vocabulary:
 *  yes_no = approve | reject (+ feedback); form = accept with a value per
 *  field | decline (+ the reason, in feedback). */
export type AnswerKind = 'yes_no' | 'form';

/** A form field's type — ACP's elicitation form (MCP elicitation's JSON
 *  Schema): string (free text; with options, pick one), array (with
 *  options, pick any), number, integer, boolean. */
export type FieldType = 'string' | 'number' | 'integer' | 'boolean' | 'array';

/** One field of a question's form. `id` keys its answer; `title` is the
 *  question it asks (the id when absent). */
export interface PingField {
	id: string;
	type: FieldType;
	title?: string;
	description?: string;
	required: boolean;
	/** A string's choices (pick one) or an array's (pick any). */
	options: PingOption[];
	format?: 'email' | 'uri' | 'date' | 'date-time';
	pattern?: string;
	minLength?: number;
	maxLength?: number;
	minimum?: number;
	maximum?: number;
	minItems?: number;
	maxItems?: number;
	/** Pre-fills the field: its type's value, or an array's option ids. */
	default?: FormValue;
}

/** What a question asks: its fields, in the order they are read. */
export interface PingForm {
	fields: PingField[];
}

/** One field's answer: text or a picked option id, a number, yes/no, or an
 *  array's picked option ids. */
export type FormValue = string | number | boolean | string[];
export type FormContent = Record<string, FormValue>;

/** One answer choice: the id is what the answer names, the label what the
 *  human reads, the detail an optional second line. */
export interface PingOption {
	id: string;
	label: string;
	detail?: string;
}

export interface Ping {
	id: string;
	runId: string;
	type: PingType;
	status: PingStatus;
	stageId?: string;
	question: string;
	context?: string;
	/** Attached workspace paths; the first is the primary reviewed document. */
	files: string[];
	/** A resolved URL payload (a gate's mr_url / workspace sentinel). */
	link?: string;
	/** The derived primary: the link, else the first file. */
	primaryRef?: string;
	answerKind: AnswerKind;
	/** What a form ping asks. */
	form?: PingForm;
	/** The phase the ping belongs to (a confirmation's phase to start, the
	 *  debug leaf's phase). */
	phase?: string;
	/** On a debug ping: giving up ends that phase run and the run goes back
	 *  to waiting — the phase is a watcher. False: giving up abandons the
	 *  run. */
	endsPhase: boolean;
	/** approve | reject (yes_no), accept | decline (form). */
	answer?: string;
	/** The answer's note — a decline's reason; on a review of the files
	 *  shown, its summary. */
	feedback?: string;
	/** An accepted form's values, by field id. */
	content?: FormContent;
	answerAttachmentIds: string[];
	/** What the person said about the files shown, comment by comment. */
	review: ReviewComment[];
	raisedAt: string;
	answeredAt?: string;
}

/** One comment of a review: on one of the files the ping showed — on a
 *  stretch of its lines, or (no lines) the whole file, always so on an
 *  image. The engine renders the comments into what the agent reads; the
 *  page only collects them. */
export interface ReviewComment {
	path: string;
	/** Absent: the whole file. */
	startLine?: number;
	endLine?: number;
	/** The section the lines sit in (a markdown file's). */
	heading?: string;
	/** The text selected. */
	quote?: string;
	body: string;
	/** The answer's attachments this comment is about. */
	attachmentIds: string[];
}

/** One of a ping's attached files: the declared ref joined with its
 *  raise-time snapshot. captured:false = the capture failed — shown as such,
 *  never silently substituted with live workspace content. */
export interface PingFile {
	path: string;
	captured: boolean;
	snapshotId?: string;
	mime?: string;
	size?: number;
}

export interface PingPayload {
	files: PingFile[];
	link?: string;
}
