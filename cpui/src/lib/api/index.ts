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
import { ApiError, unwrap, unwrapOptional, unwrapVoid } from './client';
import * as map from './mappers';
import type {
	AskFrame,
	AskMessage,
	AskSession,
	AskState,
	AuthStatus,
	CliLogin,
	Limits,
	Me,
	Notice,
	SshKey,
	Token,
	User
} from '$lib/domain';
import type { Credential, CredentialImport, ProviderInfo, SignInPending } from '$lib/domain';
import type { Swarm } from '$lib/domain';
import type {
	AnalyticsFilter,
	AnalyticsQuery,
	AnalyticsResult,
	AnalyticsValue,
	Catalog,
	Dashboard,
	DashboardDraft,
	DrillRun,
	Grain,
	MeasureRef,
	Scorecard,
	ScorecardDraft,
	TimeRange
} from '$lib/domain';
import type {
	Skill,
	SkillDetail,
	SkillDraft,
	SkillImportResult,
	Skillset,
	SkillsetDraft,
	SkillsetRef,
	SkillsetRefresh,
	SkillsetVersion
} from '$lib/domain';
import type {
	MergeRequestRow,
	ActionSpec,
	Attachment,
	Automation,
	AutomationDraft,
	HarnessSpec,
	Branch,
	BrowserStatus,
	BuildCache,
	ChannelEntry,
	EgressAuditRow,
	EgressDenial,
	EgressPolicy,
	LogRange,
	EnvCheck,
	ExposedPort,
	FileDiff,
	Flow,
	FlowBinding,
	FlowLayout,
	FlowValidation,
	FlowVersion,
	Integration,
	IntegrationDraft,
	KindDescriptor,
	LiveDelta,
	MergeRequestLive,
	Model,
	Ping,
	PingAnswerInput,
	PingPayload,
	CurationView,
	Preflight,
	Project,
	ProjectDraft,
	ProjectSecret,
	Repo,
	ResourceSample,
	Run,
	RunCheckout,
	RunDetail,
	RunEvent,
	RunFiles,
	RunInput,
	RunPhase,
	RunPorts,
	RunSsh,
	RunServices,
	ServiceConfig,
	ServiceStatus,
	Settings,
	SignalSpec,
	SourceTicket,
	Ticket,
	TicketComment,
	TicketDraft,
	TrackerStatus,
	TrackerSyncCounts,
	TrackerType,
	UploadLimits,
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
	...(o?.attachmentIds?.length ? { attachment_ids: o.attachmentIds } : {}),
	...(o?.params && Object.keys(o.params).length ? { params: o.params } : {}),
	...(o?.mr ? { mr: { iid: o.mr.iid, ...(o.mr.repo ? { repo: o.mr.repo } : {}) } } : {}),
	// Defined travels, empty included: on a rerun, [] clears the original's
	// skillsets where an absent key would keep them.
	...(o?.skillsets !== undefined
		? { skillsets: o.skillsets.map((s) => ({ id: s.id, ...(s.seq ? { seq: s.seq } : {}) })) }
		: {})
});

// A skill's whole folder, bytes base64'd; the engine normalises modes.
const skillBody = (d: SkillDraft): Wire.SkillRequest => ({
	name: d.name,
	files: d.files.map((f) => ({
		path: f.path,
		content_b64: map.base64FromBytes(f.bytes),
		...(f.mode ? { mode: f.mode } : {})
	}))
});

const skillsetBody = (d: SkillsetDraft): Wire.SkillsetRequest => ({
	name: d.name,
	...(d.description ? { description: d.description } : {}),
	skill_ids: d.skillIds
});

// Every field always travels: the endpoint replaces the ticket, so an absent
// key and an empty one would mean the same thing anyway — saying so is honest.
const ticketBody = (t: TicketDraft): Wire.TicketRequest => ({
	title: t.title,
	goal: t.goal,
	project_id: t.projectId,
	branch: t.branch ?? '',
	change_type: t.changeType ?? '',
	attachment_ids: t.attachmentIds ?? [],
	source_id: t.sourceIntegrationId ?? '',
	source_key: t.sourceKey ?? ''
});

const trackerStatuses = (r: Wire.TrackerStatusList) => ({
	statuses: many(r.statuses, map.toTrackerStatus),
	warnings: r.warnings ?? []
});

// Empty optionals stay off the wire: the engine reads an absent probe as
// "up once started", and the name check is the engine's.
const serviceBody = (s: ServiceConfig): Wire.ServiceConfig => {
	const h = s.health;
	const health: Wire.ServiceHealth = {
		...(h?.http ? { http: h.http } : {}),
		...(h?.tcp ? { tcp: h.tcp } : {}),
		...(h?.command ? { command: h.command } : {}),
		...(h?.timeout ? { timeout: h.timeout } : {}),
		...(h?.interval ? { interval: h.interval } : {})
	};
	return {
		name: s.name,
		start: s.start,
		...(s.description ? { description: s.description } : {}),
		...(s.stop ? { stop: s.stop } : {}),
		...(Object.keys(health).length ? { health } : {}),
		...(s.ports.length
			? {
					ports: s.ports.map((p) => ({
						port: p.port,
						...(p.label ? { label: p.label } : {}),
						...(p.path ? { path: p.path } : {})
					}))
				}
			: {})
	};
};

const projectBody = (p: ProjectDraft): Wire.ProjectRequest => ({
	name: p.name,
	repo_account_id: p.repoAccountId,
	provisioner_id: p.provisionerId,
	harness_ids: p.harnessIds,
	repo: {
		url: p.repo.url,
		locator: p.repo.locator,
		default_branch: p.repo.defaultBranch,
		agents_md_path: p.repo.agentsMdPath
	},
	repos: (p.repos ?? []).map((r) => ({
		name: r.name,
		url: r.url,
		locator: r.locator ?? '',
		default_branch: r.defaultBranch
	})),
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
		services: p.workspace.services.map(serviceBody),
		browser: { enabled: p.workspace.browserEnabled },
		docker: p.workspace.docker,
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

/** How long a stream the engine refused waits before asking again. */
const STREAM_RETRY_MS = 3000;

export const runs = {
	list: async (filter?: { project?: string; ticket?: string }): Promise<Run[]> =>
		many(
			await unwrap(gen.listRuns({ query: { project: filter?.project, ticket: filter?.ticket } })),
			map.toRun
		),

	get: async (id: string): Promise<RunDetail> =>
		map.toRunDetail(await unwrap(gen.getRun({ path: { id } }))),

	/** Every phase of the run's flow: what opens it, its runs, its live run,
	 *  the signals holding for it. */
	phases: async (id: string): Promise<RunPhase[]> =>
		many(await unwrap(gen.getRunPhases({ path: { id } })), map.toRunPhase),

	/** Start a phase now, by hand, on one checkout's merge request ('' = the
	 *  run's single open one, else none); only while the run is idle. */
	startPhase: (id: string, phase: string, subject = '') =>
		unwrapVoid(gen.startPhase({ path: { id, phase }, body: subject ? { subject } : {} })),

	/** Each merge request as its repository host reports it now (polled by the run page). */
	mrStatus: async (id: string): Promise<MergeRequestLive[]> =>
		many(await unwrap(gen.getRunMrStatus({ path: { id } })), map.toMergeRequestLive),

	abort: (id: string) => unwrapVoid(gen.abortRun({ path: { id } })),
	/** Park the run: the workspace stays up and nothing moves until play. */
	pause: (id: string) => unwrapVoid(gen.pauseRun({ path: { id } })),
	/** Continue mid-stage, like crash recovery; retry budgets are untouched. */
	play: (id: string) => unwrapVoid(gen.playRun({ path: { id } })),
	archive: (id: string) => unwrapVoid(gen.archiveRun({ path: { id } })),
	/** Retry: re-enter the failed stage in the same workspace. */
	retry: (id: string) => unwrapVoid(gen.retryRun({ path: { id } })),
	/** Rerun: the ticket again as a NEW run in a fresh workspace. */
	rerun: async (id: string, opts?: RunInput): Promise<string> =>
		(await unwrap(gen.rerunRun({ path: { id }, body: runInputBody(opts) }))).run_id,
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

	/** How to reach the run's workspace over SSH (TODO 46): the command, the
	 *  VS Code link and the door's fingerprint — or why nothing lets one in yet.
	 *  Starts the sidecar when it is down, so the first call can take a moment. */
	ssh: async (id: string): Promise<RunSsh> =>
		map.toRunSsh(await unwrap(gen.getRunSsh({ path: { id } }))),

	/** Publish a workspace port and get its URL. Idempotent per port. */
	exposePort: async (id: string, port: number): Promise<ExposedPort> => {
		const r = await unwrap(gen.exposeRunPort({ path: { id }, body: { port } }));
		return { port: r.port, url: r.url };
	},

	/** The workshop's services and how each stands in the run's workspace,
	 *  read live on each call (poll while on screen). */
	services: async (id: string): Promise<RunServices> =>
		map.toRunServices(await unwrap(gen.getRunServices({ path: { id } }))),

	/** Start a service by hand: it stays up until a person stops it or the
	 *  workspace goes. Resolves once the workspace runs it (state starting);
	 *  whether it comes up shows on the list. */
	startService: async (id: string, name: string): Promise<ServiceStatus> =>
		map.toServiceStatus(await unwrap(gen.startRunService({ path: { id, name } }))),

	restartService: async (id: string, name: string): Promise<ServiceStatus> =>
		map.toServiceStatus(await unwrap(gen.restartRunService({ path: { id, name } }))),

	/** Stop a service, whoever started it. */
	stopService: async (id: string, name: string): Promise<ServiceStatus> =>
		map.toServiceStatus(await unwrap(gen.stopRunService({ path: { id, name } }))),

	/** The last `tail` lines of a service's output (at most 5000). */
	serviceLog: (id: string, name: string, tail = 200): Promise<string> =>
		unwrap(gen.getRunServiceLogs({ path: { id, name }, query: { tail } })) as Promise<string>,

	/** A stage's captured log (latest attempt). */
	stageLog: (id: string, stage: string, n = 65536): Promise<string> =>
		unwrap(gen.getStageLog({ path: { id, stage }, query: { n } })) as Promise<string>,

	/** A range of the workspace's live exec output: the last `n` bytes, or
	 *  up to `n` from byte `from`. An optional provisioner capability, so
	 *  null means "not available here", not "failed". */
	workspaceLog: async (
		id: string,
		range: { from?: number; n?: number } = {}
	): Promise<LogRange | null> => {
		const w = await unwrapOptional(gen.getWorkspaceLog({ path: { id }, query: range }));
		return w ? map.toLogRange(w) : null;
	},

	/** SSE stream of a run's events: replays from afterSeq, then follows live.
	 *  Returns the unsubscribe. EventSource is the browser's, not the SDK's —
	 *  OpenAPI describes the endpoint but cannot hand us a stream.
	 *
	 *  onDelta receives the agent's live progress (text being written, tool
	 *  calls in flight). Deltas are not in the log: they carry no id, so a
	 *  reconnect never resumes from one, and the persisted event that follows
	 *  supersedes each.
	 *
	 *  The browser retries a dropped connection by itself, but an error status
	 *  (a proxy's 502 while the engine restarts) ends the stream for good, so
	 *  that one is re-opened here, from the last event seen. */
	events(
		id: string,
		afterSeq: number,
		onEvent: (e: RunEvent) => void,
		onDelta?: (d: LiveDelta) => void
	): () => void {
		let after = afterSeq;
		let es: EventSource;
		let retry: ReturnType<typeof setTimeout> | undefined;
		const open = () => {
			const source = new EventSource(`/api/runs/${id}/events?after_seq=${after}`);
			es = source;
			source.onmessage = (m) => {
				const ev = map.toRunEvent(JSON.parse(m.data) as Wire.Event);
				after = ev.seq;
				onEvent(ev);
			};
			if (onDelta) {
				source.addEventListener('delta', (m) => {
					const d = map.toLiveDelta(JSON.parse((m as MessageEvent).data));
					if (d) onDelta(d);
				});
			}
			source.onerror = () => {
				if (source.readyState === EventSource.CLOSED) retry = setTimeout(open, STREAM_RETRY_MS);
			};
		};
		open();
		return () => {
			clearTimeout(retry);
			es.close();
		};
	},

	/** Send a message to the agent you took over. Delivered into its live
	 *  session when the current turn ends. Rejects (409) without a take-over. */
	sendMessage: async (id: string, text: string): Promise<string> =>
		(await unwrap(gen.sendRunMessage({ path: { id }, body: { text } }))).message_id,

	/** Take over the running agent's session: its turns no longer end the
	 *  stage, they wait for your next message — until you release. Rejects
	 *  (409) when no agent is running, or someone already holds it. */
	takeOver: (id: string) => unwrapVoid(gen.takeOverRun({ path: { id } })),
	/** Hand the task back: the hold closes on the record and the agent gets
	 *  the handback prompt (after its turn in flight, if one). */
	release: (id: string) => unwrapVoid(gen.releaseRun({ path: { id } }))
};

// --- ask (read-only sessions beside the run) ----------------------------------

export const ask = {
	/** The pane: on or off (and why), its sessions, their conversations and
	 *  models, and the models it could answer with. */
	state: async (runId: string): Promise<AskState> =>
		map.toAskState(await unwrap(gen.getAsk({ path: { id: runId } }))),
	/** Ask a question in a session (none: the run's latest, opened when it
	 *  has none); the answer arrives on the stream. Rejects (409) when the
	 *  pane is off or the session is writing an answer, (404) for a session
	 *  the run does not have. */
	send: async (runId: string, text: string, sessionId?: string): Promise<AskMessage> =>
		map.toAskMessage(
			await unwrap(
				gen.askRun({ path: { id: runId }, body: { text, thread_id: sessionId || undefined } })
			)
		),
	/** Pick the model a session (none: the run's latest) answers with. */
	setModel: (runId: string, model: string, sessionId?: string) =>
		unwrapVoid(
			gen.setAskModel({ path: { id: runId }, body: { model, thread_id: sessionId || undefined } })
		),
	/** Open a new session: no messages, the model of the run's latest. */
	newSession: async (runId: string): Promise<AskSession> =>
		map.toAskSession(await unwrap(gen.newAskThread({ path: { id: runId } }))),
	/** Follow the pane: every session's messages as they land (replayed from
	 *  afterSeq), an answer's text as it is written, a session's state as it
	 *  changes, sessions as they open. onReconnect fires when the browser
	 *  re-opens the stream, so the caller can refetch what a dropped
	 *  connection may have missed. */
	stream(
		runId: string,
		onFrame: (f: AskFrame) => void,
		onReconnect?: () => void,
		afterSeq = 0
	): () => void {
		let after = afterSeq;
		let opened = false;
		let es: EventSource;
		let retry: ReturnType<typeof setTimeout> | undefined;
		const open = () => {
			const source = new EventSource(`/api/runs/${runId}/ask/stream?after_seq=${after}`);
			es = source;
			source.onopen = () => {
				if (opened) onReconnect?.();
				opened = true;
			};
			source.onmessage = (m) => {
				const w = JSON.parse(m.data) as { message?: Wire.Message };
				if (!w.message) return;
				const message = map.toAskMessage(w.message);
				after = message.seq;
				onFrame({ kind: 'message', sessionId: message.sessionId, message });
			};
			source.addEventListener('delta', (m) => {
				const w = JSON.parse((m as MessageEvent).data) as {
					thread_id?: string;
					text?: string;
					tool?: string;
				};
				onFrame({
					kind: 'delta',
					sessionId: w.thread_id ?? '',
					text: w.text ?? '',
					tool: w.tool || undefined
				});
			});
			source.addEventListener('state', (m) => {
				const w = JSON.parse((m as MessageEvent).data) as {
					thread_id?: string;
					busy?: boolean;
					model?: string;
				};
				onFrame({
					kind: 'state',
					sessionId: w.thread_id ?? '',
					busy: !!w.busy,
					model: w.model || undefined
				});
			});
			source.addEventListener('thread', (m) => {
				const w = JSON.parse((m as MessageEvent).data) as { thread?: Wire.ThreadState };
				if (!w.thread) return;
				onFrame({ kind: 'session', session: map.toAskSession(w.thread) });
			});
			// As runs.events: an error status ends a stream for good.
			source.onerror = () => {
				if (source.readyState === EventSource.CLOSED) retry = setTimeout(open, STREAM_RETRY_MS);
			};
		};
		open();
		return () => {
			clearTimeout(retry);
			es.close();
		};
	}
};

// --- workspace (files, changes, terminal) -------------------------------------

// Every workspace call takes repo: the checkout of a multi-repo run ('' =
// the primary).
export const workspace = {
	/** The run's checkouts, the primary first. */
	repos: async (runId: string): Promise<RunCheckout[]> =>
		many(await unwrap(gen.getWorkspaceRepos({ path: { id: runId } })), (c) => ({
			name: c.name,
			dir: c.dir,
			url: c.url,
			branch: c.branch,
			source: c.source,
			primary: c.primary ?? false
		})),

	/** One directory level of the checkout; '' is the root. */
	tree: async (runId: string, path = '', repo = ''): Promise<WorkspaceEntry[]> =>
		many(
			await unwrap(gen.getWorkspaceTree({ path: { id: runId }, query: { path, repo } })),
			map.toWorkspaceEntry
		),

	file: async (runId: string, path: string, repo = ''): Promise<WorkspaceFile> =>
		map.toWorkspaceFile(
			await unwrap(gen.getWorkspaceFile({ path: { id: runId }, query: { path, repo } }))
		),

	/** A checkout's changes against the branch it forked from. */
	changes: async (runId: string, repo = ''): Promise<WorkspaceChanges> =>
		map.toWorkspaceChanges(
			await unwrap(gen.getWorkspaceChanges({ path: { id: runId }, query: { repo } }))
		),

	diff: async (runId: string, path: string, repo = ''): Promise<FileDiff> =>
		map.toFileDiff(
			await unwrap(gen.getWorkspaceDiff({ path: { id: runId }, query: { path, repo } }))
		),

	/** WebSocket URL of a terminal in the workspace (binary frames are
	 *  keystrokes/output; a JSON text frame {type:'resize',cols,rows} resizes).
	 *  Not in the generated client: a protocol switch is not an OpenAPI call. */
	terminalUrl: (
		runId: string,
		cols: number,
		rows: number,
		repo = '',
		page: Pick<Location, 'protocol' | 'host'> = location
	): string => {
		const proto = page.protocol === 'https:' ? 'wss:' : 'ws:';
		const q = new URLSearchParams({ cols: String(cols), rows: String(rows), repo });
		return `${proto}//${page.host}/api/runs/${runId}/terminal?${q}`;
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

// --- notices -----------------------------------------------------------------

export const notices = {
	list: async (): Promise<Notice[]> => many(await unwrap(gen.listNotices()), map.toNotice),
	dismiss: (id: string) => unwrapVoid(gen.dismissNotice({ path: { id } })),
	dismissAll: () => unwrapVoid(gen.dismissAllNotices())
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

	/** An answer: approve|reject, or a form's accept (with its `content`)
	 *  or decline (the reason in `feedback`). With `review`, the person's
	 *  comments on the files shown (feedback is then the review's summary);
	 *  a comment's attachments are among `attachmentIds`. With `curated`,
	 *  what they made of each review document the gate showed. */
	answer: (pingId: string, input: PingAnswerInput) =>
		unwrapVoid(
			gen.answerPing({
				path: { pid: pingId },
				body: {
					answer: input.answer,
					...(input.feedback ? { feedback: input.feedback } : {}),
					...(input.content ? { content: input.content } : {}),
					...(input.attachmentIds?.length ? { attachment_ids: input.attachmentIds } : {}),
					...(input.review?.length ? { review: input.review.map(map.fromReviewComment) } : {}),
					...(input.curated && Object.keys(input.curated).length
						? {
								curated: Object.fromEntries(
									Object.entries(input.curated).map(([p, c]) => [p, map.fromCuration(c)])
								)
							}
						: {})
				}
			})
		),

	/** A question's review documents, decoded as the gate showed them: what
	 *  the curation page lists item by item. */
	curation: async (pingId: string): Promise<CurationView> =>
		map.toCurationView(await unwrap(gen.getPingCuration({ path: { pid: pingId } })))
};

// --- tickets -----------------------------------------------------------------

export const tickets = {
	/** Every ticket, done ones included, with its tracker status (the fleet
	 *  poll; lists filter it). */
	list: async (): Promise<Ticket[]> => many(await unwrap(gen.listTickets()), map.toTicket),

	get: async (id: string): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.getTicket({ path: { id } }))),

	create: async (draft: TicketDraft): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.createTicket({ body: ticketBody(draft) }))),

	/** Refused (409) on a sourced ticket when title, goal or change type
	 *  differ: the tracker owns those. */
	update: async (id: string, draft: TicketDraft): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.updateTicket({ path: { id }, body: ticketBody(draft) }))),

	remove: (id: string) => unwrapVoid(gen.deleteTicket({ path: { id } })),

	/** Re-read the ticket from its tracker now. A failed fetch is not thrown:
	 *  it lands on the ticket's syncState / syncError. */
	sync: async (id: string): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.syncTicket({ path: { id } }))),

	/** Move the ticket in its tracker (the remote issue, or the internal one). */
	setStatus: async (id: string, statusId: string): Promise<Ticket> =>
		map.toTicket(
			await unwrap(gen.setTicketStatus({ path: { id }, body: { status_id: statusId } }))
		),

	setLabels: async (id: string, labels: string[]): Promise<Ticket> =>
		map.toTicket(await unwrap(gen.setTicketLabels({ path: { id }, body: { labels } }))),

	/** Oldest first. */
	comments: async (id: string): Promise<TicketComment[]> =>
		many(await unwrap(gen.listTicketComments({ path: { id } })), map.toTicketComment),

	/** Posts to the ticket's tracker; resolves to the whole updated thread. */
	addComment: async (id: string, body: string): Promise<TicketComment[]> =>
		many(await unwrap(gen.addTicketComment({ path: { id }, body: { body } })), map.toTicketComment),

	/** A ticket's attachments — shared by every run of it, so they hang off the
	 *  ticket rather than a run. */
	attachments: async (id: string): Promise<Attachment[]> =>
		many((await unwrap(gen.getTicketAttachments({ path: { id } }))).attachments, map.toAttachment),

	/** Start one attempt. The input is per-attempt and never modifies the
	 *  ticket. Returns the new run's id. A ticket has one active run at a
	 *  time: see busyRunOf for the refusal. */
	startRun: async (id: string, opts?: RunInput): Promise<string> =>
		(await unwrap(gen.startTicketRun({ path: { id }, body: runInputBody(opts) }))).run_id
};

/** The 409 body of a refusal, when the error is one. */
function conflictBody(e: unknown): Record<string, unknown> | undefined {
	if (!(e instanceof ApiError) || e.status !== 409) return undefined;
	return e.body && typeof e.body === 'object' ? (e.body as Record<string, unknown>) : {};
}

/** Whether a call was refused because what it names is not there (404): an
 *  expired terminal sign-in code, say. */
export function isNotFound(e: unknown): boolean {
	return e instanceof ApiError && e.status === 404;
}

/** The run in the way, when starting a run was refused because the ticket
 *  already has an active one. */
export function busyRunOf(e: unknown): string | undefined {
	const runId = conflictBody(e)?.run_id;
	return typeof runId === 'string' && runId ? runId : undefined;
}

/** What deleting a ticket source would unlink, when the delete was refused
 *  because tickets still mirror its issues. */
export function linkedTicketsOf(e: unknown): { tickets: number; activeRuns: number } | undefined {
	const body = conflictBody(e);
	if (!body || typeof body.tickets !== 'number') return undefined;
	return {
		tickets: body.tickets,
		activeRuns: typeof body.active_runs === 'number' ? body.active_runs : 0
	};
}

/** A tracker list save refused because a deleted row is still in use: it
 *  needs a replacement. */
export const isInUse = (e: unknown): boolean => conflictBody(e) !== undefined;

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
	/** The repository's open merge requests, newest first: what a run of a
	 *  flow with operates_on: merge_request may be started on. */
	mergeRequests: async (id: string, repo?: string): Promise<MergeRequestRow[]> =>
		many(
			await unwrap(gen.listProjectMrs({ path: { id }, query: repo ? { repo } : undefined })),
			map.toMergeRequestRow
		),
	/** One merge request, with its description and the runs on it. */
	mergeRequest: async (id: string, iid: number, repo?: string): Promise<MergeRequestRow> =>
		map.toMergeRequestRow(
			await unwrap(
				gen.getProjectMr({ path: { id, iid: String(iid) }, query: repo ? { repo } : undefined })
			)
		),
	/** Start a run of a flow on a merge request (the flow's operates_on must
	 *  be merge_request), given its title and description: no ticket. */
	startRunOnMR: async (
		id: string,
		iid: number,
		opts?: {
			flow?: string;
			repo?: string;
			params?: Record<string, string>;
			skillsets?: SkillsetRef[];
		}
	): Promise<{ runId: string }> => {
		const r = await unwrap(
			gen.startMrRun({
				path: { id, iid: String(iid) },
				body: {
					...(opts?.flow ? { flow: opts.flow } : {}),
					...(opts?.repo ? { repo: opts.repo } : {}),
					...(opts?.params && Object.keys(opts.params).length ? { params: opts.params } : {}),
					...(opts?.skillsets?.length
						? {
								skillsets: opts.skillsets.map((s) => ({
									id: s.id,
									...(s.seq ? { seq: s.seq } : {})
								}))
							}
						: {})
				}
			})
		);
		return { runId: r.run_id };
	},

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
	clearBuildCache: (id: string) => unwrapVoid(gen.clearBuildCache({ path: { id } })),

	/** The workshop's secrets: names and when each was last set, never a value. */
	secrets: async (id: string): Promise<ProjectSecret[]> =>
		many(await unwrap(gen.listProjectSecrets({ path: { id } })), map.toProjectSecret),

	/** Set (or replace) a secret's value. */
	setSecret: async (id: string, name: string, value: string): Promise<ProjectSecret> =>
		map.toProjectSecret(
			await unwrap(gen.setProjectSecret({ path: { id, name }, body: { value } }))
		),

	deleteSecret: (id: string, name: string) =>
		unwrapVoid(gen.deleteProjectSecret({ path: { id, name } })),

	/** The workshop's browser: its toggle, and whether its container runs. */
	browser: async (id: string): Promise<BrowserStatus> =>
		map.toBrowserStatus(await unwrap(gen.getProjectBrowser({ path: { id } })))
};

// --- model provider credentials ---------------------------------------------

export const credentials = {
	/** The providers apothecary manages, narrowed to one harness kind. */
	providers: async (harness?: string): Promise<ProviderInfo[]> =>
		many(
			await unwrap(gen.listProviders({ query: harness ? { harness } : {} })),
			map.toProviderInfo
		),

	list: async (): Promise<Credential[]> =>
		many(await unwrap(gen.listCredentials()), map.toCredential),

	/** Add an API key. */
	addKey: async (provider: string, key: string, name?: string): Promise<Credential> =>
		map.toCredential(await unwrap(gen.createCredential({ body: { provider, key, name } }))),

	/** Import a credential file a harness CLI wrote. */
	importFile: async (text: string): Promise<CredentialImport> => {
		const r = await unwrap(gen.importCredentials({ body: { text } }));
		return { created: many(r.created, map.toCredential), rejected: r.rejected ?? [] };
	},

	rename: async (id: string, name: string): Promise<Credential> =>
		map.toCredential(await unwrap(gen.renameCredential({ path: { id }, body: { name } }))),

	remove: (id: string) => unwrapVoid(gen.deleteCredential({ path: { id } })),

	test: async (id: string): Promise<{ ok: boolean; error?: string; warnings: string[] }> => {
		const r = await unwrap(gen.testCredential({ path: { id } }));
		return { ok: r.ok, error: r.error, warnings: r.warnings ?? [] };
	},

	/** Start signing in to a provider from the engine. */
	signIn: async (provider: string): Promise<SignInPending> =>
		map.toSignInPending(await unwrap(gen.startSignIn({ body: { provider } }))),

	/** Finish a sign-in: null while a device sign-in waits for approval. */
	completeSignIn: async (
		id: string,
		pasted?: string,
		name?: string
	): Promise<Credential | null> => {
		const r = await unwrap(gen.completeSignIn({ path: { sid: id }, body: { pasted, name } }));
		return r.pending || !r.credential ? null : map.toCredential(r.credential);
	}
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

	/** A ticket source whose tickets still mirror it is refused (see
	 *  linkedTicketsOf) unless unlinkTickets moves them onto the internal
	 *  tracker in the same step. */
	remove: (id: string, opts?: { unlinkTickets?: boolean }) =>
		unwrapVoid(
			gen.deleteIntegration({
				path: { id },
				query: opts?.unlinkTickets ? { unlink_tickets: '1' } : undefined
			})
		),

	/** Re-read every ticket of a tracker now, done ones included. */
	sync: async (id: string): Promise<TrackerSyncCounts> => {
		const r = await unwrap(gen.syncIntegration({ path: { id } }));
		return {
			tickets: r.tickets,
			synced: r.synced,
			gone: r.gone,
			failed: r.failed,
			busy: r.busy,
			rateLimited: r.rate_limited
		};
	},

	/** A tracker's statuses; refresh refetches them from the remote first. */
	statuses: async (
		id: string,
		refresh = false
	): Promise<{ statuses: TrackerStatus[]; warnings: string[] }> =>
		trackerStatuses(
			await unwrap(
				gen.listTrackerStatuses({ path: { id }, query: refresh ? { refresh: '1' } : undefined })
			)
		),

	/** Remote tracker: only isDone/flowStep are taken. Internal: the list as
	 *  given (no id = new, missing = deleted). replaceWith maps a deleted
	 *  status in use to the one its tickets move to (see isInUse). */
	saveStatuses: async (
		id: string,
		statuses: TrackerStatus[],
		replaceWith?: Record<string, string>
	): Promise<{ statuses: TrackerStatus[]; warnings: string[] }> =>
		trackerStatuses(
			await unwrap(
				gen.putTrackerStatuses({
					path: { id },
					body: {
						statuses: statuses.map(map.fromTrackerStatus),
						...(replaceWith ? { replace_with: replaceWith } : {})
					}
				})
			)
		),

	types: async (id: string, refresh = false): Promise<TrackerType[]> =>
		many(
			(
				await unwrap(
					gen.listTrackerTypes({ path: { id }, query: refresh ? { refresh: '1' } : undefined })
				)
			).types,
			map.toTrackerType
		),

	/** Like saveStatuses; a remote tracker's rows only take changeType. */
	saveTypes: async (
		id: string,
		types: TrackerType[],
		replaceWith?: Record<string, string>
	): Promise<TrackerType[]> =>
		many(
			(
				await unwrap(
					gen.putTrackerTypes({
						path: { id },
						body: {
							types: types.map(map.fromTrackerType),
							...(replaceWith ? { replace_with: replaceWith } : {})
						}
					})
				)
			).types,
			map.toTrackerType
		),

	/** Check the credentials against the real service. */
	test: async (id: string): Promise<{ ok: boolean; error?: string; warnings: string[] }> => {
		const r = await unwrap(gen.testIntegration({ path: { id } }));
		return { ok: r.ok, error: r.error, warnings: r.warnings ?? [] };
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

	/** The project's parameter bindings, one per configured flow. */
	bindings: async (projectId: string): Promise<FlowBinding[]> =>
		many(await unwrap(gen.listFlowBindings({ path: { id: projectId } })), map.toFlowBinding),

	/** The project's values for a flow's parameters; null when it bound
	 *  nothing (the flow runs on its declared defaults). */
	binding: async (projectId: string, name: string): Promise<FlowBinding | null> => {
		const w = await unwrapOptional(gen.getFlowBinding({ path: { id: projectId, name } }));
		return w ? map.toFlowBinding(w) : null;
	},

	/** Replace the project's binding: every key a declared parameter, every
	 *  value one it accepts; an empty value unbinds. A binding left with no
	 *  values is removed. The document itself is untouched. */
	bind: async (
		projectId: string,
		name: string,
		params: Record<string, string>
	): Promise<FlowBinding> =>
		map.toFlowBinding(
			await unwrap(gen.putFlowBinding({ path: { id: projectId, name }, body: { params } }))
		),

	/** Back to the flow's declared defaults. */
	unbind: (projectId: string, name: string) =>
		unwrapVoid(gen.deleteFlowBinding({ path: { id: projectId, name } })),

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
	/** The JSON Schema of a flow document as written, for the YAML editor. */
	schema: async (): Promise<Record<string, unknown>> =>
		(await unwrap(gen.getFlowSchema())) as Record<string, unknown>,

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

	harnessOptions: async (): Promise<HarnessSpec[]> =>
		many(await unwrap(gen.listHarnessOptions()), map.toHarnessSpec),

	signals: async (): Promise<SignalSpec[]> =>
		many(await unwrap(gen.listSignals()), map.toSignalSpec),

	settings: async (): Promise<Settings> => map.toSettings(await unwrap(gen.getSettings())),

	saveSettings: async (s: Settings): Promise<Settings> =>
		map.toSettings(await unwrap(gen.updateSettings({ body: map.fromSettings(s) }))),

	/** The instance's capacity caps (null = no cap). */
	limits: async (): Promise<Limits> => map.toLimits(await unwrap(gen.getLimits())),

	/** Set them (admin); they apply to the next run admission. */
	setLimits: async (l: Limits): Promise<Limits> =>
		map.toLimits(await unwrap(gen.setLimits({ body: map.fromLimits(l) }))),

	/** The operator's Grafana URL (admin); '' when not configured. */
	observability: async (): Promise<string> => (await unwrap(gen.getObservability())).grafana_url,

	channels: async (): Promise<ChannelEntry[]> =>
		many(await unwrap(gen.listChannels()), map.toChannelEntry),

	addChannel: async (kind: string, config: Record<string, string>): Promise<ChannelEntry> =>
		map.toChannelEntry(await unwrap(gen.addChannel({ body: { kind, ...config } }))),

	removeChannel: (id: string) => unwrapVoid(gen.deleteChannel({ path: { id } }))
};

/** Everything, grouped. Import the group you need — `api.runs.list()` reads
 *  better at a call site than a flat namespace of sixty functions. */
// Automations: how runs begin without a person. Validated by the server
// the way a firing would be, so a refusal names what the watcher could not
// run.
function automationBody(d: AutomationDraft): Wire.AutomationRequest {
	return {
		name: d.name,
		kind: d.kind,
		flow: d.flow,
		enabled: d.enabled,
		...(d.sourceId ? { source_id: d.sourceId } : {}),
		...(d.query ? { query: d.query } : {}),
		...(d.schedule ? { schedule: d.schedule } : {}),
		...(d.ticketId ? { ticket_id: d.ticketId } : {}),
		...(d.repo ? { repo: d.repo } : {}),
		...(d.params && Object.keys(d.params).length ? { params: d.params } : {})
	};
}

export const automations = {
	list: async (projectId: string): Promise<Automation[]> =>
		many(await unwrap(gen.listAutomations({ path: { id: projectId } })), map.toAutomation),

	create: async (projectId: string, d: AutomationDraft): Promise<Automation> =>
		map.toAutomation(
			await unwrap(gen.createAutomation({ path: { id: projectId }, body: automationBody(d) }))
		),

	/** Replaces the rule; the watcher's last poll, firing and error are kept. */
	update: async (projectId: string, id: string, d: AutomationDraft): Promise<Automation> =>
		map.toAutomation(
			await unwrap(
				gen.updateAutomation({ path: { id: projectId, aid: id }, body: automationBody(d) })
			)
		),

	/** The tickets and runs it created stay. */
	remove: (projectId: string, id: string) =>
		unwrapVoid(gen.deleteAutomation({ path: { id: projectId, aid: id } }))
};

// --- accounts ----------------------------------------------------------------
//
// Who is signed in (TODO 25). The browser's proof is a session cookie the
// engine sets on setup and login; the SDK sends it with every call.

export const auth = {
	/** Whether the instance still needs its first account, and how one may
	 *  sign in. The one call that needs no session. */
	status: async (): Promise<AuthStatus> => map.toAuthStatus(await unwrap(gen.getAuthStatus())),

	/** Claim a fresh instance: the first account, an admin, signed in at once. */
	setup: async (d: {
		handle: string;
		email: string;
		name: string;
		password: string;
	}): Promise<Me> =>
		map.toMe(
			await unwrap(
				gen.setupInstance({
					body: { handle: d.handle, email: d.email, name: d.name, password: d.password }
				})
			)
		),

	/** Sign in with a handle or an email and a password. */
	login: async (login: string, password: string): Promise<Me> =>
		map.toMe(await unwrap(gen.login({ body: { method: 'password', login, proof: password } }))),

	logout: () => unwrapVoid(gen.logout()),

	me: async (): Promise<Me> => map.toMe(await unwrap(gen.getMe())),

	/** Change one's own profile (absent fields keep their value). */
	updateMe: async (d: { handle?: string; email?: string; name?: string }): Promise<Me> =>
		map.toMe(await unwrap(gen.updateMe({ body: d }))),

	changePassword: (current: string, password: string) =>
		unwrapVoid(gen.changeMyPassword({ body: { current, password } })),

	tokens: async (): Promise<Token[]> => many(await unwrap(gen.listMyTokens()), map.toToken),

	/** Mint a token; the secret is in the answer and nowhere else. */
	createToken: async (name: string): Promise<{ token: Token; secret: string }> => {
		const w = await unwrap(gen.createMyToken({ body: { name } }));
		return { token: map.toToken(w.token), secret: w.secret };
	},

	revokeToken: (id: string) => unwrapVoid(gen.deleteMyToken({ path: { id } })),

	// SSH keys (TODO 46): what the engine's SSH door lets one in with.
	sshKeys: async (): Promise<SshKey[]> => many(await unwrap(gen.listMySshKeys()), map.toSshKey),

	/** Register a public key from its .pub line. The name defaults to the
	 *  line's comment, then to the key's type. */
	addSshKey: async (publicKey: string, name?: string): Promise<SshKey> =>
		map.toSshKey(
			await unwrap(gen.addMySshKey({ body: { public_key: publicKey, name: name || undefined } }))
		),

	removeSshKey: (id: string) => unwrapVoid(gen.deleteMySshKey({ path: { id } })),

	// The terminal client's sign-in (`apothecary login`): the browser sees
	// the waiting sign-in by the code the terminal printed, and answers it.
	// An unknown or expired code is a 404 (ApiError.status).

	/** A sign-in waiting for approval; the code in any case, dash or not. */
	cliLogin: async (code: string): Promise<CliLogin> =>
		map.toCliLogin(await unwrap(gen.getCliLogin({ path: { code } }))),

	/** Sign the terminal in as oneself: it collects a token on its next poll. */
	approveCliLogin: (code: string) => unwrapVoid(gen.approveCliLogin({ path: { code } })),

	/** Refuse it: the terminal stays signed out. */
	denyCliLogin: (code: string) => unwrapVoid(gen.denyCliLogin({ path: { code } }))
};

/** Accounts, for the instance admin. */
export const users = {
	list: async (): Promise<User[]> => many(await unwrap(gen.listUsers()), map.toUser),

	create: async (d: {
		handle: string;
		email: string;
		name: string;
		password: string;
		admin: boolean;
	}): Promise<User> =>
		map.toUser(
			await unwrap(
				gen.createUser({
					body: {
						handle: d.handle,
						email: d.email,
						name: d.name,
						password: d.password,
						admin: d.admin
					}
				})
			)
		),

	/** Change an account: profile fields, the admin flag, whether it is disabled. */
	update: async (
		id: string,
		d: { handle?: string; email?: string; name?: string; admin?: boolean; disabled?: boolean }
	): Promise<User> => map.toUser(await unwrap(gen.updateUser({ path: { id }, body: d }))),

	resetPassword: (id: string, password: string) =>
		unwrapVoid(gen.resetUserPassword({ path: { id }, body: { password } }))
};

// --- skills -------------------------------------------------------------------

export const skills = {
	list: async (): Promise<Skill[]> => many(await unwrap(gen.listSkills()), map.toSkill),

	/** One skill with its file contents. */
	get: async (id: string): Promise<SkillDetail> =>
		map.toSkillDetail(await unwrap(gen.getSkill({ path: { id } }))),

	create: async (draft: SkillDraft): Promise<Skill> =>
		map.toSkill(await unwrap(gen.createSkill({ body: skillBody(draft) }))),

	/** Replace the folder. Every skillset listing the skill gets a new
	 *  version if its content changed. */
	update: async (id: string, draft: SkillDraft): Promise<Skill> =>
		map.toSkill(await unwrap(gen.updateSkill({ path: { id }, body: skillBody(draft) }))),

	remove: (id: string) => unwrapVoid(gen.deleteSkill({ path: { id } })),

	/** Re-download an imported skill. */
	refresh: async (id: string): Promise<Skill> =>
		map.toSkill(await unwrap(gen.refreshSkill({ path: { id } }))),

	/** Import a skill or a folder of skills from a public URL. */
	import: async (url: string, path?: string): Promise<SkillImportResult> =>
		map.toSkillImportResult(
			await unwrap(gen.importSkills({ body: { url, ...(path ? { path } : {}) } }))
		)
};

export const skillsets = {
	list: async (): Promise<Skillset[]> => many(await unwrap(gen.listSkillsets()), map.toSkillset),

	get: async (id: string): Promise<Skillset> =>
		map.toSkillset(await unwrap(gen.getSkillset({ path: { id } }))),

	create: async (draft: SkillsetDraft): Promise<Skillset> =>
		map.toSkillset(await unwrap(gen.createSkillset({ body: skillsetBody(draft) }))),

	update: async (id: string, draft: SkillsetDraft): Promise<Skillset> =>
		map.toSkillset(await unwrap(gen.updateSkillset({ path: { id }, body: skillsetBody(draft) }))),

	remove: (id: string) => unwrapVoid(gen.deleteSkillset({ path: { id } })),

	/** Newest first. */
	versions: async (id: string): Promise<SkillsetVersion[]> =>
		many(await unwrap(gen.listSkillsetVersions({ path: { id } })), map.toSkillsetVersion),

	/** One version; seq 0 = the latest. */
	version: async (id: string, seq: number): Promise<SkillsetVersion> =>
		map.toSkillsetVersion(await unwrap(gen.getSkillsetVersion({ path: { id, seq: String(seq) } }))),

	/** Re-download an imported skillset's folder and reconcile its members. */
	refresh: async (id: string): Promise<SkillsetRefresh> =>
		map.toSkillsetRefresh(await unwrap(gen.refreshSkillset({ path: { id } })))
};

const measureBody = (m: MeasureRef): Wire.AnalyticsMeasure =>
	m.agg ? { field: m.field, agg: m.agg } : { field: m.field };

const filterBody = (f: AnalyticsFilter): Wire.AnalyticsFilter => ({
	field: f.field,
	op: f.op,
	values: f.values
});

const timeBody = (t: TimeRange): Wire.AnalyticsTimeRange => ({
	...(t.from ? { from: t.from } : {}),
	...(t.to ? { to: t.to } : {}),
	...(t.bucket ? { bucket: t.bucket } : {})
});

const queryBody = (q: AnalyticsQuery): Wire.AnalyticsQuery => ({
	grain: q.grain,
	measures: q.measures.map(measureBody),
	group_by: q.groupBy,
	filters: q.filters.map(filterBody),
	time: timeBody(q.time),
	...(q.order ? { order: { by: q.order.by, desc: q.order.desc } } : {}),
	...(q.limit ? { limit: q.limit } : {}),
	...(q.score
		? {
				score: {
					scorecard: q.score.scorecard,
					...(q.score.baseline?.length ? { baseline: q.score.baseline } : {})
				}
			}
		: {})
});

const dashboardBody = (d: DashboardDraft): Wire.DashboardInput => ({
	name: d.name,
	filters: d.filters,
	panels: d.panels.map((p) => ({
		id: p.id,
		title: p.title,
		query: queryBody(p.query),
		viz: p.viz,
		width: p.width
	}))
});

const scorecardBody = (s: ScorecardDraft): Wire.ScorecardInput => ({
	name: s.name,
	terms: s.terms.map((t) => ({
		measure: measureBody(t.measure),
		direction: t.direction,
		weight: t.weight
	}))
});

/** The user's own run analytics: the catalog of what can be asked, the
 *  queries, and the dashboards and scorecards built from them. Built-ins come
 *  first in each list and are read-only (409 on update or delete). */
export const analytics = {
	catalog: async (): Promise<Catalog> => map.toCatalog(await unwrap(gen.getAnalyticsCatalog())),

	/** Run one query. 400 names the invalid token; 422 when it took too long. */
	query: async (q: AnalyticsQuery): Promise<AnalyticsResult> =>
		map.toAnalyticsResult(await unwrap(gen.queryAnalytics({ body: queryBody(q) }))),

	/** The distinct values of one dimension (up to 200), labelled. */
	values: async (grain: Grain, field: string, time: TimeRange = {}): Promise<AnalyticsValue[]> =>
		many(
			await unwrap(gen.analyticsValues({ body: { grain, field, time: timeBody(time) } })),
			map.toAnalyticsValue
		),

	/** The runs behind an aggregate (newest first, up to 200). Dimension
	 *  filters only: a time bucket is a range, not a filter. */
	drill: async (
		grain: Grain,
		filters: AnalyticsFilter[],
		time: TimeRange = {}
	): Promise<DrillRun[]> =>
		many(
			await unwrap(
				gen.drillAnalytics({
					body: { grain, filters: filters.map(filterBody), time: timeBody(time) }
				})
			),
			map.toDrillRun
		),

	dashboards: {
		list: async (): Promise<Dashboard[]> =>
			many(await unwrap(gen.listDashboards()), map.toDashboard),

		get: async (id: string): Promise<Dashboard> =>
			map.toDashboard(await unwrap(gen.getDashboard({ path: { id } }))),

		create: async (d: DashboardDraft): Promise<Dashboard> =>
			map.toDashboard(await unwrap(gen.createDashboard({ body: dashboardBody(d) }))),

		update: async (id: string, d: DashboardDraft): Promise<Dashboard> =>
			map.toDashboard(await unwrap(gen.updateDashboard({ path: { id }, body: dashboardBody(d) }))),

		remove: (id: string) => unwrapVoid(gen.deleteDashboard({ path: { id } }))
	},

	scorecards: {
		list: async (): Promise<Scorecard[]> =>
			many(await unwrap(gen.listScorecards()), map.toScorecard),

		get: async (id: string): Promise<Scorecard> =>
			map.toScorecard(await unwrap(gen.getScorecard({ path: { id } }))),

		create: async (s: ScorecardDraft): Promise<Scorecard> =>
			map.toScorecard(await unwrap(gen.createScorecard({ body: scorecardBody(s) }))),

		update: async (id: string, s: ScorecardDraft): Promise<Scorecard> =>
			map.toScorecard(await unwrap(gen.updateScorecard({ path: { id }, body: scorecardBody(s) }))),

		remove: (id: string) => unwrapVoid(gen.deleteScorecard({ path: { id } }))
	}
};

/** The war room: every sandbox one has in flight, across one's workshops. */
export const swarm = {
	get: async (): Promise<Swarm> => map.toSwarm(await unwrap(gen.getSwarm()))
};

export const api = {
	runs,
	workspace,
	egress,
	pings,
	tickets,
	projects,
	integrations,
	credentials,
	flows,
	automations,
	files,
	system,
	auth,
	users,
	ask,
	skills,
	skillsets,
	analytics,
	notices,
	swarm
};
