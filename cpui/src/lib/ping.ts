// Shared ping shape + helpers for the inbox and the run page, which render
// the same request/response UI (PingCard) from two sources: the durable Ping
// projection (inbox) and the ping_raised event payload (run page).

// Gate pings (plan_approval / final_review / manual_debug) take
// approve|reject; everything else is a free-text or closed-options question.
export const GATE_TYPES = ['plan_approval', 'final_review', 'manual_debug'];

// PingLike is the subset PingCard needs. The api Ping type satisfies it; the
// run page maps its event payload onto it. Missing status ⇒ pending.
export interface PingLike {
  ping_id: string;
  type: string;
  question: string;
  context?: string;
  payload_ref?: string;
  options?: string[];
  status?: string; // pending | answered | withdrawn
  answer?: string;
  feedback?: string;
  answered_at?: string;
}
