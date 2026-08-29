// Files, both directions: what humans give the factory (attachments) and what
// the engine captures out of a workspace (snapshots).

/** A file a human gave the factory (upload or tracker import). Ownership is
 *  structural — exactly one of ticketId / runId after the claim (pingId rides
 *  runId for answer files); neither = unclaimed. */
export interface Attachment {
	id: string;
	ticketId?: string;
	runId?: string;
	pingId?: string;
	filename: string;
	mime: string;
	size: number;
	sha256: string;
	createdAt: string;
}

export type SnapshotKind = 'output' | 'log' | 'ping_file';

/** An engine-captured, immutable record of workspace content. Outlives the
 *  run; content may expire under retention while the record remains. */
export interface Snapshot {
	id: string;
	runId: string;
	kind: SnapshotKind;
	stageId: string;
	iteration?: number;
	pingId?: string;
	/** Output role ('plan', 'verdict'). */
	name?: string;
	/** The workspace path captured. */
	path?: string;
	filename: string;
	mime: string;
	size: number;
	sha256: string;
	truncated: boolean;
	createdAt: string;
}

export interface RunFiles {
	attachments: Attachment[];
	snapshots: Snapshot[];
}

/** The upload policy, mirrored client-side so a bad pick fails inline. */
export interface UploadLimits {
	maxBytes: number;
	maxFiles: number;
	mimeAllow: string[];
}
