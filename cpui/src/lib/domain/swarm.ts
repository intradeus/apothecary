// The war room's picture: every sandbox the signed-in user has in flight,
// across all their workshops, polled as one snapshot. Observation only —
// nothing here is a run's history.

import type { EnvStatus, ResourceSample, RunState } from './run';

/** One subagent spawn call the agent has in flight. */
export interface Subagent {
	toolCallId: string;
	title: string;
	/** How many subagents the call runs (a batch spawn runs several). */
	count: number;
	since: string;
}

/** The agent on station in a run's agent stage. */
export interface CrewPresence {
	stageId: string;
	iteration: number;
	/** The harness kind running the stage. */
	harness: string;
	/** The stage's agent binding. */
	agent: string;
	since: string;
	/** Between a turn's start and its end. */
	turnActive: boolean;
	/** The title of the latest tool call in flight. */
	tool?: string;
	subagents: Subagent[];
}

/** One run's sandbox on the board. */
export interface SwarmUnit {
	runId: string;
	projectId: string;
	title: string;
	flowName: string;
	state: RunState;
	currentStage?: string;
	phase?: string;
	envStatus?: EnvStatus;
	createdAt: string;
	updatedAt: string;
	/** Questions waiting on a human. */
	openPings: number;
	/** A human has taken the agent's session over. */
	held: boolean;
	/** The newest resource sample; absent when none was taken. */
	resources?: ResourceSample;
	/** Absent outside an agent's attempt. */
	crew?: CrewPresence;
}

export interface Swarm {
	at: string;
	units: SwarmUnit[];
}
