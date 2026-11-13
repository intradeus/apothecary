// A project: the repository, the integrations and the workspace policy every
// run of it inherits.

import type { ServiceConfig } from './service';

export interface Project {
	id: string;
	name: string;
	repoAccountId: string;
	provisionerId: string;
	/** The available harness integrations (no default — the flow names the
	 *  harness kind). One integration per kind. */
	harnessIds: string[];
	repo: ProjectRepo;
	repos: ExtraRepo[];
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
	/** Names a new ticket's base branch from {ticket_type}, {ticket_number}
	 *  and {ticket_title} ($lib/core/branchname); '' is the default. */
	branchFormat: string;
}

/** An additional repository of a multi-repo project: cloned beside the
 *  primary as ~/repos/<name>, branched like it, and published as its own
 *  merge request when a run changes it. Without a locator it is context
 *  only (cloned, never published). */
export interface ExtraRepo {
	name: string;
	url: string;
	locator?: string;
	defaultBranch: string;
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
	/** The workshop's services, in the order the run page lists them. */
	services: ServiceConfig[];
	/** The workshop's managed browser: one container its runs share, a
	 *  Chromium per agent session — what a flow's `builtin: browser` MCP
	 *  server drives. */
	browserEnabled: boolean;
	/** Docker in this workshop's workspaces: the run's own docker daemon
	 *  (testcontainers, compose, `docker run`) without the repo declaring the
	 *  docker-outside-of-docker feature itself — the engine adds it to the
	 *  build. The instance's daemon door decides whether it is allowed. */
	docker: boolean;
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
	/** cancelled: stopped by hand, or superseded by a newer check. */
	status: 'running' | 'pass' | 'fail' | 'cancelled';
	keep: boolean;
	/** The verdict is in; the throwaway workspace is still being destroyed. */
	tearingDown: boolean;
	steps: EnvCheckStep[];
	startedAt: string;
	finishedAt?: string;
}

export interface EnvCheckStep {
	id: string;
	status: 'pass' | 'fail' | 'skip';
	detail?: string;
}

/** A workshop secret: its name and when it was last set. The value never
 *  comes back — flows read it as ${secrets.<name>} in an MCP server's env
 *  or headers. */
export interface ProjectSecret {
	name: string;
	updatedAt: string;
}

/** The workshop's browser as it stands. available false = this apothecary
 *  runs no workshop browsers. image is what the container runs, current
 *  what this build would: they differ until the browser is next idle, when
 *  it is recreated. Stopped after a while with no session, started on
 *  demand. */
export interface BrowserStatus {
	enabled: boolean;
	available: boolean;
	container?: string;
	state: 'absent' | 'running' | 'stopped';
	image?: string;
	current?: string;
	sessions: number;
	error?: string;
}

/** The project's devcontainer build cache: what makes its second run fast.
 *  available:false means this provisioner keeps no cache of its own — an
 *  honest "not applicable", not an empty one. */
export interface BuildCache {
	available: boolean;
	bytes: number;
}
