// A run's workspace as the review views see it: the checkout's files, the
// run's git changes, and the agent's live progress. Read through the
// workspace sidecar while the container is up.

export interface WorkspaceEntry {
	name: string;
	/** Repo-relative, slash-separated. */
	path: string;
	dir: boolean;
	size?: number;
	/** Matched by .gitignore — shown, but dimmed. */
	ignored: boolean;
}

export interface WorkspaceFile {
	path: string;
	size: number;
	binary: boolean;
	/** Only the first megabyte is served. */
	truncated: boolean;
	content: string;
}

/** A: added, M: modified, D: deleted, R: renamed, C: copied, ?: untracked. */
export type ChangeStatus = 'A' | 'M' | 'D' | 'R' | 'C' | '?';

export interface ChangedFile {
	path: string;
	status: ChangeStatus;
	oldPath?: string;
	additions: number;
	deletions: number;
	binary: boolean;
	/** In a commit on the run's branch, not only in the working tree. */
	committed: boolean;
}

export interface WorkspaceChanges {
	branch: string;
	head: string;
	/** The ref asked for (origin/<source branch>). */
	base: string;
	/** What the diff is against: where the branch forked from base. */
	mergeBase: string;
	commits: { sha: string; subject: string }[];
	files: ChangedFile[];
}

export interface FileDiff {
	path: string;
	/** Unified diff text. */
	diff: string;
	truncated: boolean;
}

/** Ephemeral progress of a running agent, pushed alongside the event stream.
 *  Superseded by the persisted agent_message / agent_tool that follows (same
 *  segment / toolCallId). */
export interface LiveDelta {
	stageId: string;
	iteration: number;
	turn?: string;
	kind: 'message' | 'thought' | 'tool' | 'turn_start' | 'turn_end';
	segment?: string;
	/** The segment's text so far (replaces, not appends). */
	text?: string;
	tool?: AgentToolCall;
}

/** One tool call of the agent, as the transcript records it. */
export interface AgentToolCall {
	toolCallId: string;
	title: string;
	/** read | edit | delete | move | search | execute | think | fetch | other */
	kind: string;
	/** pending | in_progress | completed | failed */
	status: string;
	input?: string;
	output?: string;
	locations: string[];
	diffs: { path: string; oldText?: string; newText: string }[];
	turn?: string;
}
