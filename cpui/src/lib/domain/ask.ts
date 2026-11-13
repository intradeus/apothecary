// The Ask pane: a run's read-only sessions beside its stages, answering
// questions about the code. Frontend-owned shapes; the mappers translate
// (the engine calls a session a thread).

export interface AskMessage {
	seq: number;
	/** The session the message belongs to. */
	sessionId: string;
	id: string;
	role: 'question' | 'answer';
	text: string;
	/** A question's asker. */
	userId?: string;
	/** An answer's model. */
	model?: string;
	costUsd: number;
	turns: number;
	/** An answer that failed: why. */
	error?: string;
	at: string;
}

/** One of the run's Ask sessions: its own conversation, harness session
 *  and model, and at most one answer in flight. */
export interface AskSession {
	id: string;
	/** The harness session it continues (none until its first answer). */
	harnessSession?: string;
	/** What its next answer runs on. */
	model?: string;
	busy: boolean;
	createdAt: string;
}

export interface AskState {
	enabled: boolean;
	/** Why the pane is off, when it is. */
	reason?: string;
	harness?: string;
	/** What the harness can reach (empty: unknown — any model may be named). */
	models: string[];
	/** The run's sessions, oldest first. */
	sessions: AskSession[];
	/** Every session's messages, in the run's order. */
	messages: AskMessage[];
}

/** One event of the pane's stream, about one session. */
export type AskFrame =
	| { kind: 'message'; sessionId: string; message: AskMessage }
	| { kind: 'delta'; sessionId: string; text: string; tool?: string }
	| { kind: 'state'; sessionId: string; busy: boolean; model?: string }
	| { kind: 'session'; session: AskSession };
