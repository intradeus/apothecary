// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).

// A gate ping takes approve|reject; everything else (ask_human) is a
// free-text or closed-options question. Every human_gate stage raises the one
// 'gate' type — what it is asking lives in its question text, and what it
// hands you lives in its payload (an artifact to review, or a link).
export const GATE_TYPE = 'gate';
export const isGatePing = (type: string) => type === GATE_TYPE;

// PingLike is the subset PingCard needs. The api Ping type satisfies it; the
// run page maps its event payload onto it. Missing status ⇒ pending.
export interface PingLike {
  ping_id: string;
  type: string;
  question: string;
  context?: string;
  // Typed payload (DESIGN-FILES §4): files = attached workspace paths,
  // link = a resolved URL; payload_ref = the derived primary for display.
  files?: string[];
  link?: string;
  payload_ref?: string;
  options?: string[];
  status?: string; // pending | answered | withdrawn
  answer?: string;
  feedback?: string;
  answered_at?: string;
}
