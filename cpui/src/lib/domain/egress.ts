// What a workspace reached, what it was refused, and the rule in force now.

/** A host's rule now. held = denied by default and nobody ruled on it yet:
 *  refused for now, still asking. blocked = someone wrote the rule (a
 *  force-block or a denylist entry): reviewed, it never asks again. */
export type EgressStatus = 'allowed' | 'held' | 'blocked';

/** A host the gateway blocked for a run — the visible diagnosis for "the
 *  firewall ate my model provider". Only held ones raise the banner. */
export interface EgressDenial {
	host: string;
	count: number;
	last: string;
	status: EgressStatus;
}

export interface EgressAuditRow {
	project: string;
	run: string;
	host: string;
	dstIp: string;
	port: number;
	/** tls | http | other */
	proto: string;
	verdict: 'allow' | 'deny';
	count: number;
	bytesOut: number;
	bytesIn: number;
	firstSeen: string;
	lastSeen: string;
}

/** The policy the gateway enforces right now — what a host's CURRENT rule is
 *  computed from (audit rows carry only historical verdicts). */
export interface EgressPolicy {
	mode: 'allow' | 'deny';
	domains: string[];
	blockedDomains: string[];
}
