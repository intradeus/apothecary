// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).

// A gate ping takes approve|reject; everything else (ask_human) is a
// free-text or closed-options question. Every human_gate stage raises the one
// 'gate' type — what it is asking lives in its question text, and what it
// hands you lives in its payload (an artifact to review, or a link).
export const GATE_TYPE = 'gate';
export const isGatePing = (type: string) => type === GATE_TYPE;

// PingLike is the subset PingCard needs, in the frontend's own vocabulary: a
// domain Ping satisfies it as-is, and the run page maps its raw ping_raised
// event payload onto it. Missing status ⇒ pending.
export interface PingLike {
	id: string;
	type: string;
	question: string;
	context?: string;
	// Typed payload (DESIGN-FILES §4): files = attached workspace paths,
	// link = a resolved URL; primaryRef = the derived primary for display.
	files?: string[];
	link?: string;
	primaryRef?: string;
	options?: string[];
	status?: string; // pending | answered | withdrawn
	answer?: string;
	feedback?: string;
	answeredAt?: string;
}
