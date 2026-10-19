// A ping: a question waiting on a human — a flow's gate, or an agent's own
// ask_human.

/** 'gate' is every human_gate stage (approve / reject with feedback; what it
 *  asks lives in its question). 'debug' is the flow's debug leaf: the failing
 *  stage is parked for you — continue re-runs it, reject abandons the run.
 *  The rest are raised by agents. */
export type PingType =
	'gate' | 'debug' | 'phase_confirm' | 'select' | 'clarification' | 'blocker' | 'permission';

export type PingStatus = 'pending' | 'answered' | 'withdrawn';

/** The shape of the answer a ping takes — declared on the ping, rendered by
 *  the card, validated by the engine from the same vocabulary:
 *  decision = approve | reject (+ feedback); choice = one option id | other;
 *  multi = any option ids | none; text = free text. */
export type AnswerKind = 'decision' | 'choice' | 'multi' | 'text';

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
	/** The answer choices of a choice or multi ping. */
	options: PingOption[];
	answer?: string;
	feedback?: string;
	answerAttachmentIds: string[];
	raisedAt: string;
	answeredAt?: string;
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
