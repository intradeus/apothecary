// The war room's numbers: what the HUD counts and how full a unit's bars
// are. Pure, so components only draw.

import type { Hue } from '$lib/design/hues';
import type { EnvStatus, ResourceSample, RunState, SwarmUnit } from '$lib/domain';
import { isTerminal } from '$lib/core/format';

/** A unit's ring hue: what its state means (DESIGN 3.1, as roles). */
export const UNIT_HUE: Record<RunState, Hue> = {
	pending: 'muted',
	provisioning: 'running',
	running: 'running',
	waiting_on_human: 'attention',
	in_review: 'accent',
	paused: 'muted',
	completed: 'success',
	abandoned: 'warning',
	failed: 'danger',
	archived: 'muted'
};

const clamp01 = (n: number) => Math.min(1, Math.max(0, n));

/** CPU as a fraction of the unit's own cores (cpus × 100 % is full);
 *  undefined without a sample. Unknown cores read as one. */
export const cpuFraction = (s: ResourceSample | undefined): number | undefined =>
	s ? clamp01(s.cpuPerc / (Math.max(1, s.cpus) * 100)) : undefined;

/** Memory used of its limit; undefined without a sample or a limit. */
export const memFraction = (s: ResourceSample | undefined): number | undefined =>
	s && s.memLimit > 0 ? clamp01(s.memUsed / s.memLimit) : undefined;

/** The subagents a unit's agent has in flight. */
export const drones = (u: SwarmUnit): number =>
	(u.crew?.subagents ?? []).reduce((n, s) => n + s.count, 0);

/** A human is wanted: a question open, or the run failed. */
export const alerting = (u: SwarmUnit): boolean =>
	u.state === 'waiting_on_human' || u.openPings > 0 || u.state === 'failed';

const DOWN: Partial<Record<EnvStatus, true>> = {
	absent: true,
	stopped: true,
	destroying: true,
	error: true
};

/** The run has a sandbox up: started, not over, its workspace not down. */
export const online = (u: SwarmUnit): boolean =>
	u.state !== 'pending' && !isTerminal(u.state) && !(u.envStatus && DOWN[u.envStatus]);

export interface HudStats {
	units: number;
	online: number;
	/** Units with a sample: what the totals below add up. */
	sampled: number;
	/** Cores busy across the sampled units (cpu % / 100). */
	cores: number;
	memUsed: number;
	memLimit: number;
	/** Units with an agent on station. */
	agents: number;
	drones: number;
	alerts: number;
}

/** The HUD's counters for a snapshot. A unit with no sample adds nothing to
 *  the load: unknown is not idle. */
export function hudStats(units: readonly SwarmUnit[]): HudStats {
	const out: HudStats = {
		units: units.length,
		online: 0,
		sampled: 0,
		cores: 0,
		memUsed: 0,
		memLimit: 0,
		agents: 0,
		drones: 0,
		alerts: 0
	};
	for (const u of units) {
		if (online(u)) out.online++;
		if (u.crew) out.agents++;
		out.drones += drones(u);
		if (alerting(u)) out.alerts++;
		const s = u.resources;
		if (!s) continue;
		out.sampled++;
		out.cores += s.cpuPerc / 100;
		out.memUsed += s.memUsed;
		out.memLimit += s.memLimit;
	}
	return out;
}
