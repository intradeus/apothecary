// The engine, as this UI sees it.
//
// THE BOUNDARY. Components import from here and from $lib/domain, never from
// ./generated — that directory is machine-written from api/openapi.yaml and its
// types are the engine's, not ours. Everything below takes domain types in and
// hands domain types back; the generated SDK and the wire shapes stop at this
// file and its mappers.
//
// `task api` regenerates the client after an engine change. What breaks then
// is a mapper, which is the point: the compiler shows you every place the
// change actually lands instead of letting a renamed field reach a component
// as undefined.

import * as gen from './generated/sdk.gen';
import type * as Wire from './generated/types.gen';
import { unwrap, unwrapOptional, unwrapVoid } from './client';
import * as map from './mappers';
import type {
	ActionSpec,
	Attachment,
	Branch,
	BuildCache,
	ChannelEntry,
	EgressAuditRow,
	EgressDenial,
	EgressPolicy,
	EnvCheck,
	ExposedPort,
	Flow,
	FlowLayout,
	FlowValidation,
	FlowVersion,
	Integration,
	IntegrationDraft,
	KindDescriptor,
	Model,
	Ping,
	PingPayload,
	Preflight,
	Project,
	ProjectDraft,
	Repo,
	ResourceSample,
	Run,
	RunDetail,
	RunEvent,
	RunFiles,
	RunInput,
	RunPorts,
	Settings,
	SignalSpec,
	SourceTicket,
	Ticket,
	TicketDraft,
	UploadLimits,
	FileDiff,
	LiveDelta,
	WorkspaceChanges,
	WorkspaceEntry,
	WorkspaceFile
} from '$lib/domain';

/** Go marshals a nil slice as null, so a list endpoint can legitimately answer
 *  `null` rather than `[]`. Every list goes through here: absent and empty are
 *  the same thing to this app, and no component should have to know that Go
 *  thinks otherwise. */
const many = <W, D>(rows: W[] | null | undefined, to: (row: W) => D): D[] => (rows ?? []).map(to);

// --- request builders --------------------------------------------------------
//
// Domain → wire, at the only place each shape is ever built.

const runInputBody = (o?: RunInput) => ({
	...(o?.flow ? { flow: o.flow } : {}),
	...(o?.goal ? { goal: o.goal } : {}),
	...(o?.sourceBranch ? { source_branch: o.sourceBranch } : {}),
	...(o?.attachmentIds?.length ? { attachment_ids: o.attachmentIds } : {})
});

// Every field always travels: the endpoint replaces the ticket, so an absent
// key and an empty one would mean the same thing anyway — saying so is honest.
const ticketBody = (t: TicketDraft): Wire.TicketRequest => ({
	title: t.title,
	goal: t.goal,
	project_id: t.projectId,
	branch: t.branch ?? '',
	attachment_ids: t.attachmentIds ?? [],
	source_id: t.sourceIntegrationId ?? '',
	source_key: t.sourceKey ?? ''
});

const projectBody = (p: ProjectDraft): Wire.ProjectRequest => ({
	name: p.name,
	forge_id: p.forgeId,
	provisioner_id: p.provisionerId,
	harness_ids: p.harnessIds,
	repo: {
		url: p.repo.url,
		locator: p.repo.locator,
		default_branch: p.repo.defaultBranch,
		agents_md_path: p.repo.agentsMdPath
	},
	commands: p.commands,
	workspace: {
		extra_domains: p.workspace.extraDomains,
		denied_domains: p.workspace.deniedDomains,
		blocked_domains: p.workspace.blockedDomains,
		// Not editable here, but a project save replaces the whole object:
		// dropping them would quietly rewrite the firewall.
		egress_allowed_cidrs: p.workspace.allowedCidrs,
		egress_allowed_ports: p.workspace.allowedPorts,
		code_server_extensions: p.workspace.codeServerExtensions,
		...(p.workspace.egressMode ? { egress_mode: p.workspace.egressMode } : {}),
		editor: {
			theme: p.workspace.editor?.theme,
			font_size: p.workspace.editor?.fontSize,
			tab_size: p.workspace.editor?.tabSize,
			format_on_save: p.workspace.editor?.formatOnSave,
			word_wrap: p.workspace.editor?.wordWrap,
			trust_workspace: p.workspace.editor?.trustWorkspace
		}
	},
	default_flow: p.defaultFlow
});

// --- runs --------------------------------------------------------------------

export const runs = {
	list: async (filter?: { project?: string; ticket?: string }): Promise<Run[]> =>
		many(
			await unwrap(gen.listRuns({ query: { project: filter?.project, ticket: filter?.ticket } })),
			map.toRun
		),

	get: async (id: string): Promise<RunDetail> =>
		map.toRunDetail(await unwrap(gen.getRun({ path: { id } }))),

	abort: (id: string) => unwrapVoid(gen.abortRun({ path: { id } })),
	/** Park the run: the workspace stays up and nothing moves until play. */
	pause: (id: string) => unwrapVoid(gen.pauseRun({ path: { id } })),
	/** Continue mid-stage, like crash recovery; retry budgets are untouched. */
	play: (id: string) => unwrapVoid(gen.playRun({ path: { id } })),
	archive: (id: string) => unwrapVoid(gen.archiveRun({ path: { id } })),
	/** Try again: re-enter the failed stage in the same workspace. */
	resume: (id: string) => unwrapVoid(gen.resumeRun({ path: { id } })),
	/** Run the ticket again as a NEW run in a fresh workspace. */
	restart: async (id: string, opts?: RunInput): Promise<string> =>
		(await unwrap(gen.restartRun({ path: { id }, body: runInputBody(opts) }))).run_id,
	/** Recreate the container in place (keeps the checkout and the commits).
	 *  Blocks until done; the run page shows progress through the workspace
	 *  log tail and the rebuilding badge.
	 *
	 *  noCache rebuilds the image from scratch. The build cache is keyed by what
	 *  the build reads, so a changed Dockerfile already rebuilds itself — this is
	 *  for what a cache key cannot see, like a floating base tag that moved. */
	rebuild: (id: string, noCache = false) =>
		unwrapVoid(gen.rebuildWorkspace({ path: { id }, body: { no_cache: noCache } })),

	/** Lazily start code-server in the workspace and get its browser URL.
	 *  Blocks while code-server boots on first open. */
	openEditor: async (id: string): Promise<string> =>
		(await unwrap(gen.openEditor({ path: { id } }))).url,

	files: async (id: string): Promise<RunFiles> =>
		map.toRunFiles(await unwrap(gen.getRunFiles({ path: { id } }))),

	resources: async (id: string): Promise<ResourceSample[]> =>
		many((await unwrap(gen.getRunResources({ path: { id } }))).samples, map.toResourceSample),

	ports: async (id: string): Promise<RunPorts> =>
		map.toRunPorts(await unwrap(gen.getRunPorts({ path: { id } }))),

	/** Publish a workspace port and get its URL. Idempotent per port. */
	exposePort: async (id: string, port: number): Promise<ExposedPort> => {
		const r = await unwrap(gen.exposeRunPort({ path: { id }, body: { port } }));
		return { port: r.port, url: r.url };
	},

	/** A stage's captured log (latest attempt). */
	stageLog: (id: string, stage: string, n = 65536): Promise<string> =>
		unwrap(gen.getStageLog({ path: { id, stage }, query: { n } })) as Promise<string>,

	/** Live tail of the workspace's exec output — an optional provisioner
	 *  capability, so null means "not available here", not "failed". */
	workspaceLog: (id: string, n = 65536): Promise<string | null> =>
		unwrapOptional(gen.getWorkspaceLog({ path: { id }, query: { n } })) as Promise<string | null>,

	/** SSE stream of a run's events: replays from afterSeq, then follows live.
	 *  Returns the unsubscribe. EventSource is the browser's, not the SDK's —
	 *  OpenAPI describes the endpoint but cannot hand us a stream.
	 *
	 *  onDelta receives the agent's live progress (text being written, tool
	 *  calls in flight). Deltas are not in the log: they carry no id, so a
	 *  reconnect never resumes from one, and the persisted event that follows
	 *  supersedes each. */
	events(
		id: string,
		afterSeq: number,
		onEvent: (e: RunEvent) => void,
		onDelta?: (d: LiveDelta) => void
	): () => void {
		const es = new EventSource(`/api/runs/${id}/events?after_seq=${afterSeq}`);
		es.onmessage = (m) => onEvent(map.toRunEvent(JSON.parse(m.data) as Wire.Event));
		if (onDelta) {
			es.addEventListener('delta', (m) => {
				const d = map.toLiveDelta(JSON.parse((m as MessageEvent).data));
				if (d) onDelta(d);
			});
		}
		return () => es.close();
	},

	/** Send the running agent a message. Delivered into its live session when
	 *  the current turn ends — or at once, cancelling the turn, with interrupt.
	 *  Rejects (409) when no agent is running to take it. */
	sendMessage: async (id: string, text: string, interrupt = false): Promise<string> =>
		(await unwrap(gen.sendRunMessage({ path: { id }, body: { text, interrupt } }))).message_id
};

// --- workspace (files, changes, terminal) -------------------------------------

export const workspace = {
	/** One directory level of the checkout; '' is the root. */
	tree: async (runId: string, path = ''): Promise<WorkspaceEntry[]> =>
		many(
			await unwrap(gen.getWorkspaceTree({ path: { id: runId }, query: { path } })),
			map.toWorkspaceEntry
		),

	file: async (runId: string, path: string): Promise<WorkspaceFile> =>
		map.toWorkspaceFile(
			await unwrap(gen.getWorkspaceFile({ path: { id: runId }, query: { path } }))
		),

	/** The run's changes against the branch it forked from. */
	changes: async (runId: string): Promise<WorkspaceChanges> =>
		map.toWorkspaceChanges(await unwrap(gen.getWorkspaceChanges({ path: { id: runId } }))),

	diff: async (runId: string, path: string): Promise<FileDiff> =>
		map.toFileDiff(await unwrap(gen.getWorkspaceDiff({ path: { id: runId }, query: { path } }))),

	/** WebSocket URL of a terminal in the workspace (binary frames are
	 *  keystrokes/output; a JSON text frame {type:'resize',cols,rows} resizes).
	 *  Not in the generated client: a protocol switch is not an OpenAPI call. */
	terminalUrl: (runId: string, cols: number, rows: number): string => {
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		return `${proto}//${location.host}/api/runs/${runId}/terminal?cols=${cols}&rows=${rows}`;
	}
};

// --- egress ------------------------------------------------------------------

/** How the audit views filter: by verdict, by host substring, capped. */
export interface EgressAuditFilter {
	verdict?: 'allow' | 'deny';
	host?: string;
	limit?: number;
}

export const egress = {
	/** The compiled-in "sensible defaults" allowlist the project form offers. */
	defaults: async (): Promise<string[]> => (await unwrap(gen.getEgressDefaults())).domains ?? [],

	denials: async (runId: string): Promise<EgressDenial[]> =>
		many(await unwrap(gen.listEgressDenials({ path: { id: runId } })), map.toEgressDenial),

	/** The rule in force right now; null when the run has no live gateway. */
	policy: async (runId: string): Promise<EgressPolicy | null> => {
		const w = await unwrapOptional(gen.getRunEgressPolicy({ path: { id: runId } }));
		return w ? map.toEgressPolicy(w) : null;
	},

	/** Allow a host instantly (no restart) and persist it to the project. */
	allow: async (runId: string, host: string): Promise<boolean> =>
		(await unwrap(gen.allowEgressHost({ path: { id: runId }, body: { host } }))).persisted,

	/** allow=true permits, allow=false force-blocks (overriding the base
	 *  allowlist). Live, and persisted to the project. */
	setRule: async (runId: string, host: string, allow: boolean): Promise<boolean> =>
		(await unwrap(gen.setEgressRule({ path: { id: runId }, body: { host, allow } }))).persisted,

	runAudit: async (runId: string, opts?: EgressAuditFilter): Promise<EgressAuditRow[]> =>
		many(
			await unwrap(gen.getRunEgressAudit({ path: { id: runId }, query: { ...opts } })),
			map.toEgressAuditRow
		),

	projectAudit: async (projectId: string, opts?: EgressAuditFilter): Promise<EgressAuditRow[]> =>
		many(
			await unwrap(gen.getProjectEgressAudit({ path: { id: projectId }, query: { ...opts } })),
			map.toEgressAuditRow
		)
};

// --- pings -------------------------------------------------------------------

export const pings = {
	list: async (status: 'pending' | 'answered' | 'all' = 'pending', limit = 100): Promise<Ping[]> =>
		many(await unwrap(gen.listPings({ query: { status, limit } })), map.toPing),

	pending: async (): Promise<Ping[]> =>
		many(await unwrap(gen.listPings({ query: { status: 'pending' } })), map.toPing),

	get: async (pingId: string): Promise<Ping> =>
		map.toPing(await unwrap(gen.getPing({ path: { pid: pingId } }))),

	/** A ping's attached files: the declared refs joined with their raise-time
	 *  snapshots, primary first. What the review workbench renders. */
	payload: async (pingId: string): Promise<PingPayload> =>
		map.toPingPayload(await unwrap(gen.getPingFiles({ path: { pid: pingId } }))),

	answer: (pingId: string, answer: string, feedback?: string, attachmentIds?: string[]) =>
		unwrapVoid(
			gen.answerPing({
				path: { pid: pingId },
				body: {
					answer,
					...(feedback ? { feedback } : {}),
					...(attachmentIds?.length ? { attachment_ids: attachmentIds } : {})
				}
			})
		)
};

// --- tickets -----------------------------------------------------------------

export const tickets = {
	list: async (project?: string): Promise<Ticket[]> =>
		many(await unwrap(gen.listTickets({ query: { project } })), map.toTicket),

	get: async (id: string): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.getTicket({ path: { id } }))),

	create: async (draft: TicketDraft): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.createTicket({ body: ticketBody(draft) }))),

	update: async (id: string, draft: TicketDraft): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.updateTicket({ path: { id }, body: ticketBody(draft) }))),

	remove: (id: string) => unwrapVoid(gen.deleteTicket({ path: { id } })),

	/** A ticket's attachments — shared by every run of it, so they hang off the
	 *  ticket rather than a run. */
	attachments: async (id: string): Promise<Attachment[]> =>
		many((await unwrap(gen.getTicketAttachments({ path: { id } }))).attachments, map.toAttachment),

	/** Start one attempt. The input is per-attempt and never modifies the
	 *  ticket. Returns the new run's id. */
	startRun: async (id: string, opts?: RunInput): Promise<string> =>
		(await unwrap(gen.startTicketRun({ path: { id }, body: runInputBody(opts) }))).run_id
};

// --- projects ----------------------------------------------------------------

export const projects = {
	list: async (): Promise<Project[]> => many(await unwrap(gen.listProjects()), map.toProject),

	get: async (id: string): Promise<Project> =>
		map.toProject(await unwrap(gen.getProject({ path: { id } }))),

	create: async (draft: ProjectDraft): Promise<Project> =>
		map.toProject(await unwrap(gen.createProject({ body: projectBody(draft) }))),

	update: async (id: string, draft: ProjectDraft): Promise<Project> =>
		map.toProject(await unwrap(gen.updateProject({ path: { id }, body: projectBody(draft) }))),

	remove: (id: string) => unwrapVoid(gen.deleteProject({ path: { id } })),

	/** Branches of the project's repository, for the source-branch picker. */
	branches: async (id: string): Promise<Branch[]> =>
		many(await unwrap(gen.listProjectBranches({ path: { id } })), map.toBranch),

	preflight: async (id: string): Promise<Preflight> =>
		map.toPreflight(await unwrap(gen.preflightProject({ path: { id } }))),

	/** Prove the workspace really builds. keep leaves the container up. */
	startEnvCheck: async (id: string, keep: boolean): Promise<string> =>
		(await unwrap(gen.startEnvCheck({ path: { id }, body: { keep } }))).check_id,

	envCheck: async (checkId: string): Promise<EnvCheck> =>
		map.toEnvCheck(await unwrap(gen.getEnvCheck({ path: { id: checkId } }))),

	/** The project's latest check, running or not; null when none has run
	 *  since the engine started. */
	latestEnvCheck: async (id: string): Promise<EnvCheck | null> => {
		const w = await unwrapOptional(gen.getLatestEnvCheck({ path: { id } }));
		return w ? map.toEnvCheck(w) : null;
	},

	/** Stop a running check; it still destroys its workspace. */
	cancelEnvCheck: (checkId: string) => unwrapVoid(gen.cancelEnvCheck({ path: { id: checkId } })),

	/** The devcontainer build cache: what makes a second run of this project
	 *  fast. null when the provisioner keeps none — a different answer from an
	 *  empty one, and the UI says so. */
	buildCache: async (id: string): Promise<BuildCache | null> => {
		const w = await unwrapOptional(gen.getBuildCache({ path: { id } }));
		return w ? map.toBuildCache(w) : null;
	},

	/** Empty it. The next build repopulates it, so the cost of being wrong is
	 *  one slow build. */
	clearBuildCache: (id: string) => unwrapVoid(gen.clearBuildCache({ path: { id } }))
};

// --- integrations ------------------------------------------------------------

export const integrations = {
	list: async (): Promise<Integration[]> =>
		many(await unwrap(gen.listIntegrations()), map.toIntegration),

	create: async (draft: IntegrationDraft): Promise<Integration> =>
		map.toIntegration(await unwrap(gen.createIntegration({ body: { ...draft } }))),

	update: async (
		id: string,
		patch: { name?: string; config?: Record<string, unknown>; secrets?: Record<string, string> }
	): Promise<Integration> =>
		map.toIntegration(
			await unwrap(
				gen.updateIntegration({ path: { id }, body: { name: patch.name ?? '', ...patch } })
			)
		),

	remove: (id: string) => unwrapVoid(gen.deleteIntegration({ path: { id } })),

	/** Check the credentials against the real service. */
	test: async (id: string): Promise<{ ok: boolean; error?: string }> => {
		const r = await unwrap(gen.testIntegration({ path: { id } }));
		return { ok: r.ok, error: r.error };
	},

	repos: async (id: string): Promise<Repo[]> =>
		many(await unwrap(gen.listIntegrationRepos({ path: { id } })), map.toRepo),

	models: async (id: string): Promise<Model[]> =>
		many(await unwrap(gen.listIntegrationModels({ path: { id } })), map.toModel),

	/** Browse a tracker to import from. An empty query means the tracker's own
	 *  default; a "jql:" prefix passes raw JQL through. */
	sourceTickets: async (id: string, q = '', limit = 25): Promise<SourceTicket[]> =>
		many(
			await unwrap(gen.searchSourceTickets({ path: { id }, query: { q, limit } })),
			map.toSourceTicket
		),

	sourceTicket: async (id: string, key: string): Promise<SourceTicket> =>
		map.toSourceTicket(await unwrap(gen.getSourceTicket({ path: { id, key } })))
};

// --- flows -------------------------------------------------------------------

// A flow belongs to one project, so every call names it. The two exceptions
// are honest ones: validate is pure parsing, and templates belong to nobody
// until a project adopts one.
export const flows = {
	/** Every project's flows — the cross-project overview. */
	listAll: async (): Promise<Flow[]> => many(await unwrap(gen.listAllFlows()), map.toFlow),

	/** One project's flows, plus the templates it has not adopted. */
	list: async (projectId: string): Promise<Flow[]> =>
		many(await unwrap(gen.listFlows({ path: { id: projectId } })), map.toFlow),

	/** The compiled-in templates, owned by nobody. */
	templates: async (): Promise<Flow[]> => many(await unwrap(gen.listFlowTemplates()), map.toFlow),

	/** One of a project's flows. Falls back to the built-in template of that
	 *  name (marked builtin) when the project has not saved its own. */
	get: async (projectId: string, name: string): Promise<Flow> =>
		map.toFlow(await unwrap(gen.getFlow({ path: { id: projectId, name } }))),

	create: async (
		projectId: string,
		name: string,
		yaml: string,
		layout?: FlowLayout
	): Promise<Flow> =>
		map.toFlow(
			await unwrap(
				gen.createFlow({
					path: { id: projectId },
					body: { name, yaml, ...(layout ? { layout } : {}) }
				})
			)
		),

	/** Saving a template's name for the first time creates this project's copy. */
	save: async (projectId: string, name: string, yaml: string, layout?: FlowLayout): Promise<Flow> =>
		map.toFlow(
			await unwrap(
				gen.updateFlow({
					path: { id: projectId, name },
					body: { yaml, ...(layout ? { layout } : {}) }
				})
			)
		),

	remove: (projectId: string, name: string) =>
		unwrapVoid(gen.deleteFlow({ path: { id: projectId, name } })),

	/** Clone a flow into another project. The copy is a new document with its
	 *  own history: editing it never reaches back to the original. */
	copy: async (
		projectId: string,
		name: string,
		toProjectId: string,
		newName?: string
	): Promise<Flow> =>
		map.toFlow(
			await unwrap(
				gen.copyFlow({
					path: { id: projectId, name },
					body: { to_project_id: toProjectId, ...(newName ? { name: newName } : {}) }
				})
			)
		),

	/** Copy a compiled-in template into a project. A template belongs to no
	 *  project, so it has its own route rather than a project id it does not
	 *  have; copying it under its own name adopts it (the project gets its own
	 *  editable document). */
	copyTemplate: async (name: string, toProjectId: string, newName?: string): Promise<Flow> =>
		map.toFlow(
			await unwrap(
				gen.copyFlowTemplate({
					path: { name },
					body: { to_project_id: toProjectId, ...(newName ? { name: newName } : {}) }
				})
			)
		),

	/** Project-independent: the parser checks the document itself. */
	validate: async (yaml: string): Promise<FlowValidation> =>
		map.toFlowValidation(await unwrap(gen.validateFlow({ body: { yaml } }))),

	versions: async (projectId: string, name: string): Promise<FlowVersion[]> =>
		many(await unwrap(gen.listFlowVersions({ path: { id: projectId, name } })), map.toFlowVersion),

	version: async (projectId: string, hash: string): Promise<FlowVersion> =>
		map.toFlowVersion(await unwrap(gen.getFlowVersion({ path: { id: projectId, hash } })))
};

// --- files -------------------------------------------------------------------

export const files = {
	/** Human uploads for tickets, run goals and ping answers. */
	upload: async (list: File[]): Promise<Attachment[]> => {
		const body = new FormData();
		for (const f of list) body.append('files', f);
		// The browser sets the multipart boundary; the SDK must not touch it.
		const r = await unwrap(gen.uploadAttachments({ body: body as never }));
		return many(r.attachments, map.toAttachment);
	},

	limits: async (): Promise<UploadLimits> =>
		map.toUploadLimits(await unwrap(gen.getAttachmentLimits())),

	/** Direct links: the browser fetches these itself (img src, download). */
	attachmentUrl: (id: string, download = false) =>
		`/api/attachments/${id}${download ? '?download=1' : ''}`,
	snapshotUrl: (id: string, download = false) =>
		`/api/snapshots/${id}${download ? '?download=1' : ''}`
};

// --- system ------------------------------------------------------------------

export const system = {
	kinds: async (): Promise<KindDescriptor[]> =>
		many(await unwrap(gen.listKinds()), map.toKindDescriptor),

	actions: async (): Promise<ActionSpec[]> =>
		many(await unwrap(gen.listActions()), map.toActionSpec),

	signals: async (): Promise<SignalSpec[]> =>
		many(await unwrap(gen.listSignals()), map.toSignalSpec),

	settings: async (): Promise<Settings> => map.toSettings(await unwrap(gen.getSettings())),

	saveSettings: async (s: Settings): Promise<Settings> =>
		map.toSettings(await unwrap(gen.updateSettings({ body: map.fromSettings(s) }))),

	channels: async (): Promise<ChannelEntry[]> =>
		many(await unwrap(gen.listChannels()), map.toChannelEntry),

	addChannel: async (kind: string, config: Record<string, string>): Promise<ChannelEntry> =>
		map.toChannelEntry(await unwrap(gen.addChannel({ body: { kind, ...config } }))),

	removeChannel: (id: string) => unwrapVoid(gen.deleteChannel({ path: { id } }))
};

/** Everything, grouped. Import the group you need — `api.runs.list()` reads
 *  better at a call site than a flat namespace of sixty functions. */
export const api = {
	runs,
	workspace,
	egress,
	pings,
	tickets,
	projects,
	integrations,
	flows,
	files,
	system
};
