// The Ask pane: a run's read-only session beside its stages, answering
// questions about the code. Frontend-owned shapes; the mappers translate.

export interface AskMessage {
	seq: number;
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

export interface AskState {
	enabled: boolean;
	/** Why the pane is off, when it is. */
	reason?: string;
	harness?: string;
	/** What the next answer runs on, and what the harness can reach (empty:
	 *  unknown — any model may be named). */
	model?: string;
	models: string[];
	busy: boolean;
	messages: AskMessage[];
}

/** One event of the pane's stream. */
export type AskFrame =
	| { kind: 'message'; message: AskMessage }
	| { kind: 'delta'; text: string; tool?: string }
	| { kind: 'state'; busy: boolean; model?: string };
