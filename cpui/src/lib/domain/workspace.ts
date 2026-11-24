// A run's workspace as the review views see it: the checkout's files, the
// run's git changes, and the agent's live progress. Read through the
// workspace sidecar while the container is up.

export interface WorkspaceEntry {
	name: string;
	/** Repo-relative, slash-separated; absolute for an absolute listing. */
	path: string;
	dir: boolean;
	size?: number;
	/** Matched by .gitignore — shown, but dimmed. */
	ignored: boolean;
	/** Modification time in nanoseconds since the epoch, as a decimal string
	 *  (beyond JavaScript's safe integers); '' when unknown. */
	mtime: string;
}

export interface WorkspaceFile {
	path: string;
	size: number;
	binary: boolean;
	/** Only the first megabyte is served. */
	truncated: boolean;
	content: string;
	/** As on WorkspaceEntry: taken before the read. */
	mtime: string;
}

/** Where a viewed file or folder lives: a checkout ('' is the primary) and a
 *  repo-relative path, or an absolute path in the workspace. */
export type FileLoc = { repo: string; path: string } | { abs: string };

/** One path's state, from a stat only: what live refresh compares. */
export interface PathStat {
	exists: boolean;
	dir: boolean;
	size: number;
	/** As on WorkspaceEntry. */
	mtime: string;
	/** Why it could not be read (permission), when it exists. */
	error?: string;
}

/** A path a message mentioned, found in the workspace. */
export interface ResolvedPath {
	loc: FileLoc;
	dir: boolean;
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

/** A shell living in the workspace's sidecar: shared by everyone who may
 *  open a shell on the run, it outlives the page and ends with the shell or
 *  the workspace. */
export interface TerminalSession {
	id: string;
	/** 1-based, for display. */
	n: number;
	/** The checkout it opened in ('' is the primary). */
	repo: string;
	/** The checkout's absolute directory. */
	dir: string;
	startedAt: string;
	/** How many views are attached right now. */
	attached: number;
	/** A program other than the shell runs in it: it keeps the workspace awake. */
	busy: boolean;
	/** What runs in it ('' when only the shell). */
	command: string;
}

/** Ephemeral progress of a running agent, pushed alongside the event stream.
 *  Superseded by the persisted agent_message / agent_tool that follows (same
 *  segment / toolCallId). */
export interface LiveDelta {
	stageId: string;
	attempt: number;
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

/** One byte range of the workspace's live log. Keeping up reads on from
 *  `end`; going back reads what ends at `start`. `size` is the log's whole
 *  length when it was read: below what was kept up to, the log was replaced. */
export interface LogRange {
	text: string;
	start: number;
	end: number;
	size: number;
}
