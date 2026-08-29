// A project: the repository, the integrations and the workspace policy every
// run of it inherits.

export interface Project {
	id: string;
	name: string;
	forgeId: string;
	provisionerId: string;
	/** The available harness integrations (no default — the flow names the
	 *  harness kind). One integration per kind. */
	harnessIds: string[];
	repo: ProjectRepo;
	commands: Record<string, string>;
	workspace: WorkspacePolicy;
	defaultFlow: string;
	createdAt: string;
	updatedAt: string;
}

export interface ProjectRepo {
	url: string;
	locator: string;
	defaultBranch: string;
	agentsMdPath: string;
}

export interface WorkspacePolicy {
	extraDomains: string[];
	/** 'deny' = allow-all plus deniedDomains; anything else = deny-all plus the
	 *  derived allowlist ∪ extraDomains. */
	egressMode?: string;
	deniedDomains: string[];
	/** Force-deny override that wins in BOTH modes, managed live from the run
	 *  page. */
	blockedDomains: string[];
	/** The 'allow' mode escape hatch for legitimate raw (non-HTTP/TLS) egress
	 *  the default backstop drops. Carried through the form untouched — the UI
	 *  does not edit these, and dropping them on save would silently change the
	 *  firewall. */
	allowedCidrs: string[];
	allowedPorts: number[];
	codeServerExtensions: string[];
	editor?: EditorPrefs;
}

/** code-server defaults, applied as VS Code user settings when the browser
 *  editor opens — no image rebuild, unlike extensions. */
export interface EditorPrefs {
	theme?: string;
	fontSize?: number;
	tabSize?: number;
	formatOnSave?: boolean;
	wordWrap?: boolean;
	/** Disable VS Code Workspace Trust prompts. */
	trustWorkspace?: boolean;
}

/** What creating or replacing a project needs: everything but the identity
 *  and timestamps the engine owns. */
export type ProjectDraft = Omit<Project, 'id' | 'createdAt' | 'updatedAt'>;

/** One branch a run can fork off from; isDefault marks the repo's default so
 *  the picker can pre-select it. */
export interface Branch {
	name: string;
	isDefault: boolean;
}

export interface Repo {
	name: string;
	url: string;
	locator: string;
	defaultBranch: string;
}

/** id is the exact `provider/model` string flows use. */
export interface Model {
	id: string;
	name: string;
}

/** One readiness check on a project. */
export interface PreflightStep {
	id: string;
	ok: boolean;
	error?: string;
}

export interface Preflight {
	ok: boolean;
	steps: PreflightStep[];
}

/** A throwaway provision that proves the project's workspace really builds. */
export interface EnvCheck {
	id: string;
	projectId: string;
	status: 'running' | 'pass' | 'fail';
	keep: boolean;
	steps: EnvCheckStep[];
	startedAt: string;
	finishedAt?: string;
}

export interface EnvCheckStep {
	id: string;
	status: 'pass' | 'fail' | 'skip';
	detail?: string;
}

/** The project's devcontainer build cache: what makes its second run fast.
 *  available:false means this provisioner keeps no cache of its own — an
 *  honest "not applicable", not an empty one. */
export interface BuildCache {
	available: boolean;
	bytes: number;
}
