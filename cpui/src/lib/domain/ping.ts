// A ping: a question waiting on a human — a flow's gate, or an agent's own
// question (ask_human, or its harness's own question tool over ACP).

/** 'gate' is every human_gate stage (approve / reject with feedback; what it
 *  asks lives in its question). 'debug' is the flow's debug leaf: the failing
 *  stage is parked for you — continue re-runs it, reject abandons the run.
 *  'manual' is a manual stage: the run's workspace is in your hands — Done
 *  follows on_success, give up is a dead end. The rest are raised by agents. */
export type PingType =
	| 'gate'
	| 'debug'
	| 'manual'
	| 'phase_confirm'
	| 'select'
	| 'clarification'
	| 'blocker'
	| 'permission';

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
	/** Each shown file's port type, by path, for the files with a structured
	 *  type (review_threads, review_replies, mr_threads): what the page keys
	 *  on to offer curation. A plain document is absent. */
	fileTypes: Record<string, string>;
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
	/** What the person made of each review document the gate showed, by
	 *  path: which items are posted and how they read. */
	curated?: Record<string, Curation>;
	raisedAt: string;
	answeredAt?: string;
}

/** One decision over a review document as the gate showed it: `item` is
 *  "notes/<i>" or "replies/<i>", keep says whether it is posted, body the
 *  text as rewritten (absent: as written), resolve a reply's flag. */
export interface CuratedItem {
	item: string;
	keep: boolean;
	body?: string;
	resolve?: boolean;
}

/** A person's curation of one review document: a rewritten summary (a
 *  review's) and the items decided; an item absent is posted as written. */
export interface Curation {
	summary?: string;
	items: CuratedItem[];
}

/** What a gate's answer carries: the verdict, a note for the agent, files,
 *  the review's comments, a form's values, and the curation of each review
 *  document shown. */
export interface PingAnswerInput {
	answer: string;
	feedback?: string;
	attachmentIds?: string[];
	review?: ReviewComment[];
	content?: FormContent;
	curated?: Record<string, Curation>;
}

/** The kinds of a review note, and what a reply did about its thread. */
export const REVIEW_KINDS = ['blocking', 'suggestion', 'question', 'nit'] as const;
export const REPLY_DIDS = ['fixed', 'declined', 'answered'] as const;

/** One note of a review: a new thread on a line of a file. The body is
 *  posted; rationale, evidence and example are for the person at the gate. */
export interface ReviewNote {
	path: string;
	line: number;
	kind: string;
	body: string;
	rationale?: string;
	evidence?: string;
	example?: string;
}

/** A reply the reviewer writes in a thread already on the merge request. */
export interface ReviewContinuation {
	thread: string;
	body: string;
	resolve: boolean;
	rationale?: string;
}

/** A review_threads document as the gate showed it. */
export interface ReviewDocument {
	summary: string;
	notes: ReviewNote[];
	replies: ReviewContinuation[];
}

/** One entry of a review_replies document: what a round did about one
 *  thread. The reply is posted; the rest is the owner's. */
export interface ReviewReply {
	thread: string;
	did: string;
	reply: string;
	resolve: boolean;
	rationale?: string;
	evidence?: string;
}

/** One comment of a thread on the merge request. */
export interface ThreadComment {
	id: string;
	author: string;
	at?: string;
	/** Apothecary wrote it. */
	ours: boolean;
	/** Written after apothecary's last word in the thread. */
	new: boolean;
	/** What an earlier reply of apothecary's said it did. */
	did?: string;
	body: string;
}

/** One review thread on the merge request, as the engine read it. */
export interface ReviewThread {
	id: string;
	path?: string;
	line?: number;
	resolved: boolean;
	/** Apothecary opened it (a review note). */
	ours: boolean;
	kind?: string;
	hunk?: string;
	comments: ThreadComment[];
}

/** An mr_threads document: the threads loaded, and the notes a person
 *  declined to post earlier in the run. */
export interface ThreadsDocument {
	mergeRequest?: string;
	threads: ReviewThread[];
	dropped: ReviewNote[];
}

/** One shown file of a review type, decoded from its raise-time snapshot.
 *  Not captured, or with an error, it can only be approved as written. */
export interface CurationDocument {
	path: string;
	type: string;
	captured: boolean;
	error?: string;
	review?: ReviewDocument;
	replies?: ReviewReply[];
}

/** A question's review documents as the gate showed them, and the threads
 *  beside them when the gate showed an mr_threads file. */
export interface CurationView {
	documents: CurationDocument[];
	threads?: ThreadsDocument;
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
	/** The file's port type when it is a structured one. */
	type?: string;
}

export interface PingPayload {
	files: PingFile[];
	link?: string;
}
