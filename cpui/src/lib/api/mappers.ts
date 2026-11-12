// The translating half of the anti-corruption layer.
//
// One function per type, wire → domain. They are dull on purpose: renaming,
// narrowing open strings into the unions this UI actually switches on, and
// turning "absent" into the empty array or undefined so no component has to
// ask twice. When the engine renames a field, exactly one line in here moves.
//
// The other direction (domain → wire) lives with the calls that need it in
// index.ts, because a request body is only ever built at one call site.

import type * as Wire from './generated/types.gen';
import type {
	AskMessage,
	AskState,
	AuthStatus,
	CliLogin,
	Limits,
	Me,
	SshKey,
	Token,
	User
} from '$lib/domain';
import type { Credential, Notice, ProviderInfo, SignInPending } from '$lib/domain';
import { asChangeType, asFlowStep } from '$lib/domain';
import type {
	Skill,
	SkillDetail,
	SkillFileContent,
	SkillFileEntry,
	SkillImportResult,
	SkillSource,
	Skillset,
	SkillsetPin,
	SkillsetRefresh,
	SkillsetVersion
} from '$lib/domain';
import type {
	Agg,
	AnalyticsFieldType,
	AnalyticsQuery,
	AnalyticsResult,
	AnalyticsValue,
	Catalog,
	CatalogField,
	Dashboard,
	DrillRun,
	FilterOp,
	Grain,
	MeasureRef,
	Panel,
	Scorecard
} from '$lib/domain';
import { BUCKETS, DIRECTIONS, PANEL_WIDTHS, VIZES } from '$lib/domain';
import { normalizeDoc, PHASE_KINDS, OPERATES_ON, type OperatesOn } from '$lib/domain/flowdoc';
import {
	answerKindOf,
	toCuratedMap,
	toFormContent,
	toPingForm,
	toReviewComment
} from '$lib/core/ping';
import type {
	Curation,
	CurationDocument,
	CurationView,
	ReviewNote,
	ThreadsDocument
} from '$lib/domain';
import type {
	ActionPort,
	ActionSetting,
	ActionSpec,
	AgentToolCall,
	HarnessSpec,
	Attachment,
	Automation,
	Branch,
	BrowserStatus,
	BuildCache,
	ChangedFile,
	ChangeStatus,
	ChannelEntry,
	EgressAuditRow,
	EgressDenial,
	EgressPolicy,
	LogRange,
	EnvCheck,
	EnvCheckStep,
	FileDiff,
	Flow,
	FlowBinding,
	FlowValidation,
	FlowVersion,
	Integration,
	KindDescriptor,
	KindField,
	LiveDelta,
	MergeRequestLive,
	CheckState,
	OpenMergeRequest,
	MergeRequestBase,
	MergeRequestRow,
	Model,
	ParamDecl,
	PhaseSummary,
	Ping,
	PingFile,
	PingPayload,
	PingStatus,
	PingType,
	ReviewComment,
	Port,
	Preflight,
	PreflightStep,
	Project,
	ProjectSecret,
	Repo,
	ResourceSample,
	Run,
	RunFacts,
	RunDetail,
	RunEvent,
	RunFiles,
	RunPhase,
	RunPorts,
	RunServices,
	RunSsh,
	RunState,
	ServiceConfig,
	ServiceState,
	ServiceStatus,
	Settings,
	Snapshot,
	SnapshotKind,
	SourceTicket,
	SyncState,
	Ticket,
	TicketComment,
	CommentOrigin,
	TrackerStatus,
	TrackerType,
	UploadLimits,
	WorkspaceChanges,
	WorkspaceEntry,
	WorkspaceFile,
	WorkspacePolicy
} from '$lib/domain';

// --- narrowing ---------------------------------------------------------------
//
// The wire types these come from are plain strings: Go has no union type and
// the spec is generated from Go. Narrowing HERE, once, is what lets every
// component switch exhaustively — and an unrecognised value falls back
// visibly rather than crashing a page.

const RUN_STATES: RunState[] = [
	'pending',
	'provisioning',
	'running',
	'waiting_on_human',
	'in_review',
	'paused',
	'completed',
	'abandoned',
	'failed',
	'archived'
];
const PING_TYPES: PingType[] = [
	'gate',
	'debug',
	'manual',
	'phase_confirm',
	'select',
	'clarification',
	'blocker',
	'permission'
];
const PING_STATUSES: PingStatus[] = ['pending', 'answered', 'withdrawn'];
const PORTS: Port[] = ['repository', 'provisioner', 'harness', 'ticketsource'];
const SNAPSHOT_KINDS: SnapshotKind[] = ['output', 'log', 'ping_file'];
const SERVICE_STATES: ServiceState[] = [
	'stopped',
	'starting',
	'healthy',
	'unhealthy',
	'exited',
	'stopping',
	'unknown'
];

const oneOf = <T extends string>(allowed: readonly T[], v: string | undefined, fallback: T): T =>
	allowed.includes(v as T) ? (v as T) : fallback;

const list = <T>(v: T[] | null | undefined): T[] => v ?? [];

// --- runs --------------------------------------------------------------------

export function toAskMessage(w: Wire.Message): AskMessage {
	return {
		seq: w.seq,
		id: w.id,
		role: w.role === 'answer' ? 'answer' : 'question',
		text: w.text,
		userId: w.user_id || undefined,
		model: w.model || undefined,
		costUsd: w.cost_usd ?? 0,
		turns: w.turns ?? 0,
		error: w.error || undefined,
		at: w.at
	};
}

export function toAskState(w: Wire.AskState): AskState {
	return {
		enabled: w.enabled,
		reason: w.reason || undefined,
		harness: w.harness || undefined,
		model: w.model || undefined,
		models: list(w.models),
		busy: w.busy,
		messages: list(w.messages).map(toAskMessage)
	};
}

export function toRun(w: Wire.RunView): Run {
	return {
		id: w.run_id,
		state: oneOf(RUN_STATES, w.state, 'pending'),
		title: w.ticket_title,
		flowName: w.flow_name,
		flowHash: w.flow_hash,
		currentStage: w.current_stage,
		projectId: w.project_id,
		ticketId: w.ticket_id,
		ticketKey: w.ticket_key,
		ticketUrl: w.ticket_url,
		goal: w.run_goal,
		branch: w.branch,
		sourceBranch: w.source_branch,
		mrUrl: w.mr_url,
		mrs: list(w.mrs).map((m) => ({
			repo: m.repo,
			branch: m.branch,
			target: m.target,
			url: m.url,
			iid: m.iid
		})),
		adopted: w.adopted
			? {
					repo: w.adopted.repo,
					iid: w.adopted.iid,
					url: w.adopted.url,
					author: w.adopted.author,
					title: w.adopted.title
				}
			: undefined,
		checkouts: list(w.checkouts).map((c) => ({
			name: c.name,
			dir: c.dir,
			url: c.url,
			branch: c.branch,
			source: c.source,
			primary: c.primary ?? false
		})),
		phase: w.phase || undefined,
		phaseSubject: w.phase_subject || undefined,
		params: w.params ?? undefined,
		startedBy: w.started_by || undefined,
		skillsets: w.skillsets?.length ? w.skillsets.map(toSkillsetPin) : undefined,
		idle: w.idle ?? false,
		hold: w.hold
			? {
					stageId: w.hold.stage_id,
					userId: w.hold.user_id || undefined,
					since: w.hold.since,
					turns: w.hold.turns ?? 0,
					messages: w.hold.messages ?? 0,
					costUsd: w.hold.cost_usd ?? 0
				}
			: undefined,
		envId: w.env_id,
		envStatus: w.env_status as Run['envStatus'],
		envDirty: w.env_dirty ?? false,
		pause: w.pause_reason ? { reason: w.pause_reason, note: w.pause_note || undefined } : undefined,
		provisionFailed: w.provision_failed ?? false,
		createdAt: w.created_at,
		updatedAt: w.updated_at,
		facts: toRunFacts(w.facts)
	};
}

export function toRunFacts(w: Wire.Facts): RunFacts {
	return {
		costUsd: w.cost_usd,
		turns: w.turns,
		wallMs:
			w.queued_ms + w.provisioning_ms + w.active_ms + w.waiting_human_ms + w.held_ms + w.paused_ms,
		attempts: w.attempts,
		endedAt: w.ended_at || undefined,
		open: Object.fromEntries(
			Object.entries(w.open ?? {}).map(([id, a]) => [
				id,
				{ iteration: a.iteration, startedAt: a.started_at, outcome: a.outcome }
			])
		)
	};
}

export function toRunPhase(w: Wire.PhaseInfo): RunPhase {
	return {
		id: w.id,
		description: w.description || undefined,
		signal: w.signal,
		kind: oneOf(PHASE_KINDS, w.kind, 'watcher'),
		start: oneOf(['auto', 'confirm', 'manual'] as const, w.start, 'auto'),
		then: w.then || undefined,
		quietForS: w.quiet_for_s || undefined,
		max: w.max,
		runs: w.runs ?? {},
		total: w.total ?? 0,
		live: w.live
			? { subject: w.live.subject || undefined, parkedAt: w.live.parked_at || undefined }
			: undefined,
		holds: list(w.holds).map((h) => ({
			subject: h.subject,
			key: h.key,
			why: h.why,
			seen: h.seen ?? false,
			observedAt: h.observed_at
		}))
	};
}

export function toMergeRequestRow(w: Wire.MrRow): MergeRequestRow {
	return {
		mr: toOpenMergeRequest(w.mr),
		stackedOn: w.stacked_on ? toMergeRequestBase(w.stacked_on) : undefined,
		run: w.run
			? {
					id: w.run.id,
					state: w.run.state as RunState,
					flow: w.run.flow,
					createdAt: w.run.created_at
				}
			: undefined,
		runs: w.runs ?? 0
	};
}

export function toMergeRequestBase(w: Wire.MrBase): MergeRequestBase {
	return { iid: w.iid, url: w.url, title: w.title, branch: w.branch };
}

export function toOpenMergeRequest(w: Wire.MrInfo): OpenMergeRequest {
	return {
		iid: w.ref.IID,
		url: w.ref.URL,
		title: w.title,
		author: w.author,
		source: w.source,
		target: w.target,
		draft: w.draft ?? false,
		fork: w.fork ?? false,
		body: w.body ?? '',
		state: w.state === 'merged' || w.state === 'closed' ? w.state : 'open'
	};
}

export function toMergeRequestLive(w: Wire.MrLive): MergeRequestLive {
	return {
		repo: w.repo,
		branch: w.branch,
		url: w.url,
		state: w.state as MergeRequestLive['state'],
		mergeable: w.mergeable as MergeRequestLive['mergeable'],
		target: w.target || undefined,
		headSha: w.head_sha || undefined,
		pendingThreads: w.pending_threads,
		lastCommentAt: w.last_comment_at ? new Date(w.last_comment_at) : undefined,
		mergeableNow: w.mergeable_now ?? false,
		draft: w.draft ?? false,
		checks: w.checks
			? {
					head: w.checks.head,
					state: checkState(w.checks.state),
					items: list(w.checks.items).map((c) => ({
						name: c.name,
						state: checkState(c.state),
						url: c.url || undefined
					}))
				}
			: undefined,
		approval: w.approval ? { approved: w.approval.approved, by: w.approval.by ?? [] } : undefined,
		stackedOn: w.stacked_on ? toMergeRequestBase(w.stacked_on) : undefined,
		error: w.error || undefined
	};
}

const checkState = (s: string): CheckState =>
	oneOf(['pending', 'success', 'failure', 'neutral'] as const, s, '' as CheckState);

export function toRunEvent(w: Wire.Event): RunEvent {
	return {
		runId: w.run_id,
		seq: w.seq,
		ts: w.ts,
		type: w.type,
		payload: (w.payload ?? {}) as Record<string, unknown>
	};
}

export function toRunDetail(w: Wire.RunDetail): RunDetail {
	return { run: toRun(w.run), events: list(w.events).map(toRunEvent) };
}

export function toResourceSample(w: Wire.ResourceSample): ResourceSample {
	return {
		at: w.at,
		cpuPerc: w.cpu_perc,
		cpus: w.cpus,
		memUsed: w.mem_used,
		memLimit: w.mem_limit,
		netRx: w.net_rx,
		netTx: w.net_tx,
		pids: w.pids
	};
}

export function toRunPorts(w: Wire.RunPorts): RunPorts {
	return {
		exposed: list(w.ports).map((p) => ({ port: p.port, url: p.url })),
		observed: list(w.observed).map((p) => ({
			port: p.port,
			process: p.process,
			service: p.service
		})),
		links: w.links === true
	};
}

export function toRunSsh(w: Wire.RunSsh): RunSsh {
	return {
		enabled: w.enabled,
		reason: w.reason || undefined,
		host: w.host || undefined,
		port: w.port || undefined,
		user: w.user || undefined,
		path: w.path || undefined,
		fingerprint: w.fingerprint || undefined,
		hostKey: w.host_key || undefined,
		command: w.command || undefined,
		setup: w.setup || undefined,
		vscodeUrl: w.vscode_url || undefined,
		extensions: w.extensions?.length ? w.extensions : undefined,
		keys: w.keys
	};
}

// --- services ----------------------------------------------------------------

export function toServiceStatus(w: Wire.ServiceInfo): ServiceStatus {
	return {
		name: w.name,
		description: w.description || undefined,
		state: oneOf(SERVICE_STATES, w.state, 'unknown'),
		healthy: w.healthy,
		by: w.by === 'stage' || w.by === 'human' ? w.by : undefined,
		stageId: w.stage_id || undefined,
		phase: w.flow_phase || undefined,
		startedAt: w.started_at || undefined,
		healthyAt: w.healthy_at || undefined,
		endedAt: w.ended_at || undefined,
		exitCode: w.exit_code ?? undefined,
		probe: w.probe || undefined,
		lastProbeAt: w.last_probe_at || undefined,
		lastProbeOk: w.last_probe_ok,
		lastError: w.last_error || undefined,
		ports: list(w.ports).map((p) => ({
			port: p.port,
			label: p.label || undefined,
			path: p.path || undefined,
			address: p.address,
			url: p.url || undefined
		}))
	};
}

export function toRunServices(w: Wire.RunServices): RunServices {
	return {
		workspace: w.workspace,
		error: w.error || undefined,
		services: list(w.services).map(toServiceStatus)
	};
}

function toServiceConfig(w: Wire.ServiceConfig): ServiceConfig {
	const h = w.health;
	return {
		name: w.name,
		description: w.description || undefined,
		start: w.start,
		stop: w.stop || undefined,
		health:
			h && (h.http || h.tcp || h.command || h.timeout || h.interval)
				? {
						http: h.http || undefined,
						tcp: h.tcp || undefined,
						command: h.command || undefined,
						timeout: h.timeout || undefined,
						interval: h.interval || undefined
					}
				: undefined,
		ports: list(w.ports).map((p) => ({
			port: p.port,
			label: p.label || undefined,
			path: p.path || undefined
		}))
	};
}

// --- pings -------------------------------------------------------------------

export function toPing(w: Wire.PingView): Ping {
	return {
		id: w.ping_id,
		runId: w.run_id,
		type: oneOf(PING_TYPES, w.type, 'clarification'),
		status: oneOf(PING_STATUSES, w.status, 'pending'),
		stageId: w.stage_id,
		question: w.question,
		context: w.context,
		files: list(w.files),
		fileTypes: w.file_types ?? {},
		link: w.link,
		primaryRef: w.payload_ref,
		answerKind: answerKindOf(w.answer_kind, w.type),
		form: toPingForm(w.form),
		phase: w.phase || undefined,
		endsPhase: w.ends_phase ?? false,
		answer: w.answer,
		feedback: w.feedback,
		content: toFormContent(w.content),
		answerAttachmentIds: list(w.answer_attachment_ids),
		review: list(w.review).map(toReviewComment),
		curated: toCuratedMap(w.curated),
		raisedAt: w.raised_at,
		answeredAt: w.answered_at
	};
}

/** A curation as the answer sends it: only what the person decided. */
export function fromCuration(c: Curation): Wire.Curation {
	return {
		...(c.summary !== undefined ? { summary: c.summary } : {}),
		items: c.items.map((it) => ({
			item: it.item,
			keep: it.keep,
			...(it.body ? { body: it.body } : {}),
			...(it.resolve !== undefined ? { resolve: it.resolve } : {})
		}))
	};
}

function toReviewNote(w: Wire.ReviewNote): ReviewNote {
	return {
		path: w.path,
		line: w.line,
		kind: w.kind,
		body: w.body,
		rationale: w.rationale || undefined,
		evidence: w.evidence || undefined,
		example: w.example || undefined
	};
}

function toThreadsDocument(w: Wire.ThreadsDocument): ThreadsDocument {
	return {
		mergeRequest: w.merge_request || undefined,
		threads: list(w.threads).map((th) => ({
			id: th.id,
			path: th.path || undefined,
			line: th.line || undefined,
			resolved: th.resolved,
			ours: th.ours,
			kind: th.kind || undefined,
			hunk: th.hunk || undefined,
			comments: list(th.comments).map((c) => ({
				id: c.id,
				author: c.author,
				at: c.at || undefined,
				ours: c.ours === true,
				new: c.new === true,
				did: c.did || undefined,
				body: c.body
			}))
		})),
		dropped: list(w.dropped).map(toReviewNote)
	};
}

/** A question's review documents, decoded by the engine from what the gate
 *  showed: the curation page lists them item by item. */
export function toCurationView(w: Wire.CurationView): CurationView {
	return {
		documents: list(w.documents).map((d): CurationDocument => ({
			path: d.path,
			type: d.type,
			captured: d.captured,
			error: d.error || undefined,
			review: d.review
				? {
						summary: d.review.summary ?? '',
						notes: list(d.review.notes).map(toReviewNote),
						replies: list(d.review.replies).map((r) => ({
							thread: r.thread,
							body: r.body,
							resolve: r.resolve === true,
							rationale: r.rationale || undefined
						}))
					}
				: undefined,
			replies: d.replies
				? d.replies.map((r) => ({
						thread: r.thread,
						did: r.did,
						reply: r.reply,
						resolve: r.resolve === true,
						rationale: r.rationale || undefined,
						evidence: r.evidence || undefined
					}))
				: undefined
		})),
		threads: w.threads ? toThreadsDocument(w.threads) : undefined
	};
}

export function fromReviewComment(c: ReviewComment): Wire.ReviewComment {
	return {
		path: c.path,
		body: c.body,
		...(c.startLine ? { start_line: c.startLine, end_line: c.endLine ?? c.startLine } : {}),
		...(c.heading ? { heading: c.heading } : {}),
		...(c.quote ? { quote: c.quote } : {}),
		...(c.attachmentIds.length ? { attachment_ids: c.attachmentIds } : {})
	};
}

export function toPingFile(w: Wire.PingFile): PingFile {
	return {
		path: w.path,
		captured: w.captured,
		snapshotId: w.snapshot_id,
		mime: w.mime,
		size: w.size,
		type: w.type || undefined
	};
}

export function toPingPayload(w: Wire.PingFiles): PingPayload {
	return { files: list(w.files).map(toPingFile), link: w.link };
}

// --- tickets -----------------------------------------------------------------

/** The list endpoint returns the ticket alone; get/create/update return it
 *  with its attachments and any import warnings. One domain type either way —
 *  the extras are simply empty on a summary. */
export function toTicket(w: Wire.TicketView | Wire.TicketDetail): Ticket {
	const detail = w as Wire.TicketDetail;
	return {
		id: w.id ?? '',
		projectId: w.project_id ?? '',
		title: w.title ?? '',
		goal: w.goal ?? '',
		baseBranch: w.base_branch ?? '',
		changeType: asChangeType(w.change_type),
		attachmentIds: list(detail.attachment_ids),
		source: w.source_id
			? {
					integrationId: w.source_id,
					kind: w.source_kind ?? '',
					key: w.source_key ?? '',
					url: w.source_url,
					type: w.source_type || undefined
				}
			: undefined,
		status: w.status
			? {
					id: w.status.id,
					name: w.status.name,
					isDone: w.status.is_done,
					flowStep: asFlowStep(w.status.flow_step)
				}
			: undefined,
		type: w.type ? { id: w.type.id, name: w.type.name } : undefined,
		labels: list(w.labels),
		links: list(detail.links).map((l) => ({ url: l.url, title: l.title })),
		syncState: oneOf(SYNC_STATES, w.sync_state, ''),
		syncError: w.sync_error || undefined,
		syncedAt: w.synced_at || undefined,
		statusChangedAt: w.status_changed_at || undefined,
		statusChangedBy: w.status_changed_by || undefined,
		warnings: list(detail.warnings),
		createdAt: w.created_at ?? '',
		updatedAt: w.updated_at ?? ''
	};
}

const SYNC_STATES: readonly SyncState[] = ['', 'ok', 'error', 'gone'];
const COMMENT_ORIGINS: readonly CommentOrigin[] = ['remote', 'user', 'apothecary'];

export function toTicketComment(w: Wire.TicketComment): TicketComment {
	return {
		id: w.id,
		author: w.author,
		bodyMd: w.body_md,
		// Unknown reads as remote: it came from somewhere other than here.
		origin: oneOf(COMMENT_ORIGINS, w.origin, 'remote'),
		runId: w.run_id || undefined,
		stageId: w.stage_id || undefined,
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toTrackerStatus(w: Wire.TrackerStatus): TrackerStatus {
	return {
		id: w.id,
		integrationId: w.integration_id,
		remoteId: w.remote_id,
		name: w.name,
		remoteCategory: w.remote_category,
		position: w.position,
		isDone: w.is_done,
		flowStep: asFlowStep(w.flow_step)
	};
}

export function fromTrackerStatus(s: TrackerStatus): Wire.TrackerStatus {
	return {
		id: s.id,
		integration_id: s.integrationId,
		remote_id: s.remoteId,
		name: s.name,
		remote_category: s.remoteCategory,
		position: s.position,
		is_done: s.isDone,
		...(s.flowStep ? { flow_step: s.flowStep } : {})
	};
}

export function toTrackerType(w: Wire.TrackerType): TrackerType {
	return {
		id: w.id,
		integrationId: w.integration_id,
		remoteId: w.remote_id,
		name: w.name,
		changeType: asChangeType(w.change_type),
		position: w.position
	};
}

export function fromTrackerType(t: TrackerType): Wire.TrackerType {
	return {
		id: t.id,
		integration_id: t.integrationId,
		remote_id: t.remoteId,
		name: t.name,
		change_type: t.changeType,
		position: t.position
	};
}

export function toSourceTicket(w: Wire.SourceTicket): SourceTicket {
	return {
		key: w.key ?? '',
		title: w.title ?? '',
		body: w.body,
		url: w.url,
		status: w.status,
		type: w.type,
		assignee: w.assignee,
		updated: w.updated,
		attachments: list(w.attachments).map((a) => ({
			filename: a.filename ?? '',
			mime: a.mime,
			size: a.size
		})),
		changeType: asChangeType(w.change_type),
		branch: w.branch ?? ''
	};
}

// --- projects ----------------------------------------------------------------

function toWorkspacePolicy(w: Wire.Project['workspace']): WorkspacePolicy {
	return {
		extraDomains: list(w?.extra_domains),
		egressMode: w?.egress_mode,
		deniedDomains: list(w?.denied_domains),
		blockedDomains: list(w?.blocked_domains),
		allowedCidrs: list(w?.egress_allowed_cidrs),
		allowedPorts: list(w?.egress_allowed_ports),
		codeServerExtensions: list(w?.code_server_extensions),
		services: list(w?.services).map(toServiceConfig),
		browserEnabled: w?.browser?.enabled ?? false,
		docker: w?.docker ?? false,
		editor: w?.editor
			? {
					theme: w.editor.theme,
					fontSize: w.editor.font_size,
					tabSize: w.editor.tab_size,
					formatOnSave: w.editor.format_on_save,
					wordWrap: w.editor.word_wrap,
					trustWorkspace: w.editor.trust_workspace
				}
			: undefined
	};
}

export function toProject(w: Wire.Project): Project {
	return {
		id: w.id ?? '',
		name: w.name ?? '',
		repoAccountId: w.repo_account_id ?? '',
		provisionerId: w.provisioner_id ?? '',
		harnessIds: list(w.harness_ids),
		repo: {
			url: w.repo?.url ?? '',
			locator: w.repo?.locator ?? '',
			defaultBranch: w.repo?.default_branch ?? '',
			agentsMdPath: w.repo?.agents_md_path ?? ''
		},
		repos: list(w.repos).map((r) => ({
			name: r.name,
			url: r.url,
			locator: r.locator || undefined,
			defaultBranch: r.default_branch
		})),
		commands: w.commands ?? {},
		workspace: toWorkspacePolicy(w.workspace),
		defaultFlow: w.default_flow ?? '',
		createdAt: w.created_at ?? '',
		updatedAt: w.updated_at ?? ''
	};
}

export function toBranch(w: Wire.BranchInfo): Branch {
	return { name: w.name, isDefault: w.default ?? false };
}

export function toRepo(w: Wire.RepoInfo): Repo {
	return {
		name: w.name ?? '',
		url: w.url ?? '',
		locator: w.locator ?? '',
		defaultBranch: w.default_branch ?? ''
	};
}

export function toModel(w: Wire.ModelInfo): Model {
	return { id: w.id ?? '', name: w.name ?? '' };
}

export function toPreflightStep(w: Wire.PreflightStep): PreflightStep {
	return { id: w.id, ok: w.ok, error: w.error };
}

export function toPreflight(w: Wire.PreflightResult): Preflight {
	return { ok: w.ok, steps: list(w.steps).map(toPreflightStep) };
}

export function toProjectSecret(w: Wire.ProjectSecret): ProjectSecret {
	return { name: w.name, updatedAt: w.updated_at };
}

export function toBrowserStatus(w: Wire.BrowserInfo): BrowserStatus {
	return {
		enabled: w.enabled,
		available: w.available,
		container: w.container || undefined,
		state: w.state === 'running' || w.state === 'stopped' ? w.state : 'absent',
		image: w.image || undefined,
		current: w.current || undefined,
		sessions: w.sessions,
		error: w.error || undefined
	};
}

export function toBuildCache(w: Wire.BuildCacheInfo): BuildCache {
	return { available: w.available, bytes: w.bytes };
}

export function toEnvCheck(w: Wire.EnvCheck): EnvCheck {
	return {
		id: w.id ?? '',
		projectId: w.project_id ?? '',
		status: (w.status as EnvCheck['status']) ?? 'running',
		keep: w.keep ?? false,
		tearingDown: w.tearing_down ?? false,
		steps: list(w.steps).map((s): EnvCheckStep => ({
			id: s.id ?? '',
			status: (s.status as EnvCheckStep['status']) ?? 'skip',
			detail: s.detail
		})),
		startedAt: w.started_at ?? '',
		finishedAt: w.finished_at
	};
}

// --- files -------------------------------------------------------------------

export function toAttachment(w: Wire.Attachment): Attachment {
	return {
		id: w.id ?? '',
		ticketId: w.ticket_id,
		runId: w.run_id,
		pingId: w.ping_id,
		filename: w.filename ?? '',
		mime: w.mime ?? '',
		size: w.size ?? 0,
		sha256: w.sha256 ?? '',
		createdAt: w.created_at ?? ''
	};
}

export function toSnapshot(w: Wire.Snapshot): Snapshot {
	return {
		id: w.id ?? '',
		runId: w.run_id ?? '',
		kind: oneOf(SNAPSHOT_KINDS, w.kind, 'output'),
		stageId: w.stage_id ?? '',
		iteration: w.iteration,
		pingId: w.ping_id,
		name: w.name,
		path: w.path,
		filename: w.filename ?? '',
		mime: w.mime ?? '',
		size: w.size ?? 0,
		sha256: w.sha256 ?? '',
		truncated: w.truncated ?? false,
		createdAt: w.created_at ?? ''
	};
}

export function toRunFiles(w: Wire.RunFiles): RunFiles {
	return {
		attachments: list(w.attachments).map(toAttachment),
		snapshots: list(w.snapshots).map(toSnapshot)
	};
}

export function toUploadLimits(w: Wire.UploadLimits): UploadLimits {
	return {
		maxBytes: w.max_bytes ?? 0,
		maxFiles: w.max_files ?? 0,
		mimeAllow: list(w.mime_allow)
	};
}

// --- flows -------------------------------------------------------------------

export function toFlow(w: Wire.Flow): Flow {
	return {
		projectId: w.project_id ?? '',
		name: w.name ?? '',
		yaml: w.yaml ?? '',
		hash: w.hash ?? '',
		layout: (w.layout as Flow['layout']) ?? undefined,
		updatedAt: w.updated_at ?? '',
		builtin: w.builtin ?? false,
		shadowsBuiltin: w.shadows_builtin ?? false,
		operatesOn: toOperatesOn(w.operates_on),
		parameters: list(w.parameters).map(toParamDecl),
		phases: list(w.phases).map(toPhaseSummary)
	};
}

export function toAutomation(w: Wire.Automation): Automation {
	return {
		id: w.id ?? '',
		projectId: w.project_id ?? '',
		name: w.name ?? '',
		kind: oneOf(['tracker_query', 'cron', 'merge_request'] as const, w.kind, 'tracker_query'),
		sourceId: w.source_id || undefined,
		query: w.query || undefined,
		schedule: w.schedule || undefined,
		ticketId: w.ticket_id || undefined,
		repo: w.repo || undefined,
		flow: w.flow ?? '',
		params: { ...(w.params ?? {}) },
		enabled: w.enabled ?? false,
		lastPolledAt: w.last_polled_at || undefined,
		lastFiredAt: w.last_fired_at || undefined,
		lastError: w.last_error || undefined,
		createdAt: w.created_at ?? '',
		updatedAt: w.updated_at ?? ''
	};
}

export function toFlowBinding(w: Wire.FlowBinding): FlowBinding {
	return {
		projectId: w.project_id ?? '',
		flowName: w.flow_name ?? '',
		params: { ...(w.params ?? {}) },
		updatedAt: w.updated_at ?? ''
	};
}

// A flow's parameters and phases are described the same way on a stored flow
// and on a validation verdict: what its runs are started on, what a project
// may bind, its main chain, watchers and outcomes.
const toOperatesOn = (v?: string): OperatesOn | undefined =>
	v ? oneOf(OPERATES_ON, v, 'ticket') : undefined;

function toParamDecl(p: Wire.Param): ParamDecl {
	return {
		name: p.name,
		type: oneOf(['bool', 'int', 'float', 'string', 'duration'] as const, p.type, 'string'),
		list: p.list || undefined,
		default: p.default,
		description: p.description || undefined,
		enum: p.enum ?? undefined,
		known: p.known ?? false,
		required: p.required || undefined,
		defaultRef: p.default_ref || undefined,
		role: p.role === 'model' || p.role === 'harness' ? p.role : undefined,
		harness: p.harness || undefined
	};
}

function toPhaseSummary(p: Wire.PhaseSummary): PhaseSummary {
	return {
		id: p.id,
		description: p.description || undefined,
		kind: oneOf(PHASE_KINDS, p.kind, 'watcher'),
		signal: p.signal,
		start: oneOf(['auto', 'confirm', 'manual'] as const, p.start, 'auto'),
		quietFor: p.quiet_for || undefined,
		max: p.max,
		then: p.then === 'done' || p.then === 'abort' ? p.then : undefined,
		subject: p.subject ?? false,
		stages: p.stages
	};
}

export function toFlowVersion(w: Wire.FlowVersion): FlowVersion {
	return {
		hash: w.hash ?? '',
		name: w.name ?? '',
		yaml: w.yaml ?? '',
		createdAt: w.created_at ?? ''
	};
}

export function toFlowValidation(w: Wire.FlowValidation): FlowValidation {
	return {
		ok: w.ok,
		errors: list(w.errors),
		warnings: list(w.warnings),
		name: w.name,
		hash: w.hash,
		operatesOn: toOperatesOn(w.operates_on),
		stages: w.stages,
		agents: w.agents,
		doc: w.doc ? normalizeDoc(w.doc) : undefined,
		parameters: list(w.parameters).map(toParamDecl),
		refs: list(w.refs).map((r) => ({
			param: r.param,
			phase: r.phase || undefined,
			stage: r.stage || undefined,
			field: r.field,
			input: r.input || undefined
		})),
		phases: list(w.phases).map(toPhaseSummary)
	};
}

function toActionPort(w: Wire.PortSig): ActionPort {
	return {
		name: w.name,
		description: w.description,
		type: w.type,
		required: w.required ?? false,
		defaultPath: w.default_path
	};
}

function toActionSetting(w: Wire.SettingSig): ActionSetting {
	return {
		name: w.name,
		description: w.description,
		kind: oneOf(['enum', 'text', 'list'] as const, w.kind, 'text'),
		required: w.required ?? false,
		values: w.values ?? undefined,
		defaultValue: w.default || undefined
	};
}

export function toActionSpec(w: Wire.Action): ActionSpec {
	return {
		slug: w.slug ?? '',
		title: w.title ?? '',
		description: w.description,
		requires: w.requires,
		single: w.single ?? false,
		exactlyOne: w.exactly_one?.length ? list(w.exactly_one) : undefined,
		remote: w.remote ?? false,
		needs: w.needs,
		inputs: list(w.inputs).map(toActionPort),
		outputs: list(w.outputs).map(toActionPort),
		settings: list(w.settings).map(toActionSetting)
	};
}

export function toHarnessSpec(w: Wire.Harness): HarnessSpec {
	return {
		kind: w.kind,
		options: list(w.options).map((o) => ({
			name: o.name,
			description: o.description || undefined,
			kind: oneOf(['model', 'enum'] as const, o.kind, 'model'),
			values: o.values?.length ? list(o.values) : undefined,
			needs: o.needs || undefined
		}))
	};
}

export function toSignalSpec(w: Wire.Signal) {
	return {
		slug: w.slug ?? '',
		title: w.title ?? '',
		description: w.description,
		requires: w.requires,
		needs: w.needs,
		kind: oneOf(['hand', 'observed'] as const, w.kind, 'observed'),
		debounced: w.debounced ?? false,
		all: w.all ?? false
	};
}

// --- integrations ------------------------------------------------------------

export function toIntegration(w: Wire.Integration): Integration {
	return {
		id: w.id ?? '',
		port: oneOf(PORTS, w.port, 'repository'),
		kind: w.kind ?? '',
		name: w.name ?? '',
		config: w.config ?? {},
		secrets: (w.secrets ?? {}) as Record<string, string>,
		createdAt: w.created_at ?? '',
		updatedAt: w.updated_at ?? ''
	};
}

function toKindField(w: Wire.Field): KindField {
	return {
		name: w.name ?? '',
		label: w.label ?? '',
		placeholder: w.placeholder,
		default: w.default,
		secret: w.secret ?? false,
		required: w.required ?? false,
		multiline: w.multiline,
		help: w.help,
		type: w.type === 'credentials' ? 'credentials' : undefined
	};
}

export function toKindDescriptor(w: Wire.KindDescriptor): KindDescriptor {
	return {
		port: oneOf(PORTS, w.port, 'repository'),
		kind: w.kind ?? '',
		label: w.label ?? '',
		help: w.help,
		fields: list(w.fields).map(toKindField)
	};
}

export function toChannelEntry(w: Wire.ChannelEntry): ChannelEntry {
	return { id: w.id ?? '', kind: w.kind ?? '', config: w.config ?? {} };
}

// --- notices -----------------------------------------------------------------

export function toNotice(w: Wire.Notice): Notice {
	return {
		id: w.id ?? '',
		kind: w.kind ?? '',
		title: w.title ?? '',
		body: w.body ?? '',
		link: w.link ?? '',
		count: w.count ?? 1,
		createdAt: w.created_at ?? '',
		lastSeenAt: w.last_seen_at ?? ''
	};
}

export function toLogRange(w: Wire.WorkspaceLog): LogRange {
	const start = w.start ?? 0;
	const end = w.end ?? start;
	return { text: w.data ?? '', start, end, size: w.size ?? end };
}

// --- egress ------------------------------------------------------------------

export function toEgressDenial(w: Wire.EgressDenial): EgressDenial {
	return {
		host: w.host ?? '',
		count: w.count ?? 0,
		last: w.last ?? '',
		// Unknown reads as held: asking twice beats never asking.
		status: w.status === 'allowed' || w.status === 'blocked' ? w.status : 'held'
	};
}

export function toEgressAuditRow(w: Wire.EgressAuditRow): EgressAuditRow {
	return {
		project: w.project ?? '',
		run: w.run ?? '',
		host: w.host ?? '',
		dstIp: w.dst_ip ?? '',
		port: w.port ?? 0,
		proto: w.proto ?? '',
		verdict: w.verdict === 'deny' ? 'deny' : 'allow',
		count: w.count ?? 0,
		bytesOut: w.bytes_out ?? 0,
		bytesIn: w.bytes_in ?? 0,
		firstSeen: w.first_seen ?? '',
		lastSeen: w.last_seen ?? ''
	};
}

export function toEgressPolicy(w: Wire.EgressPolicy): EgressPolicy {
	return {
		mode: w.mode === 'deny' ? 'deny' : 'allow',
		domains: list(w.domains),
		blockedDomains: list(w.blocked_domains)
	};
}

// --- settings ----------------------------------------------------------------

export function toSettings(w: Wire.Settings): Settings {
	return {
		destroyCompletedAfter: w.destroy_completed_after ?? '',
		destroyAbandoned: w.destroy_abandoned ?? '',
		orphanUploadsAfter: w.orphan_uploads_after ?? '',
		snapshotsAfter: w.snapshots_after ?? ''
	};
}

export function fromSettings(s: Settings): Wire.Settings {
	return {
		destroy_completed_after: s.destroyCompletedAfter,
		destroy_abandoned: s.destroyAbandoned,
		orphan_uploads_after: s.orphanUploadsAfter,
		snapshots_after: s.snapshotsAfter
	};
}

/** -1 on the wire is "no cap"; the UI reads that as null. */
export function fromLimits(l: Limits): Wire.LimitsInfo {
	return {
		max_concurrent_runs: l.maxConcurrentRuns ?? -1,
		max_environments: l.maxEnvironments ?? -1
	};
}

export function toLimits(w: Wire.LimitsInfo): Limits {
	return {
		maxConcurrentRuns: w.max_concurrent_runs < 0 ? null : w.max_concurrent_runs,
		maxEnvironments: w.max_environments < 0 ? null : w.max_environments
	};
}

// --- accounts ----------------------------------------------------------------

export function toUser(w: Wire.User): User {
	return {
		id: w.id,
		handle: w.handle,
		email: w.email,
		name: w.name,
		admin: w.admin,
		disabled: !!w.disabled_at,
		lastSeenAt: w.last_seen_at || undefined,
		createdAt: w.created_at
	};
}

export function toMe(w: Wire.Me): Me {
	return { user: toUser(w.user), methods: w.methods ?? [] };
}

export function toAuthStatus(w: Wire.AuthStatus): AuthStatus {
	return { setupRequired: w.setup_required, methods: w.methods ?? [], version: w.version ?? '' };
}

export function toToken(w: Wire.Token): Token {
	return {
		id: w.id,
		name: w.name,
		prefix: w.prefix,
		createdAt: w.created_at,
		lastUsedAt: w.last_used_at || undefined,
		expiresAt: w.expires_at || undefined,
		idleSeconds: w.idle_seconds || undefined
	};
}

export function toCliLogin(w: Wire.CliLoginInfo): CliLogin {
	return { userCode: w.user_code, name: w.name, expiresAt: w.expires_at };
}

export function toSshKey(w: Wire.SshKey): SshKey {
	return {
		id: w.id,
		name: w.name,
		type: w.type,
		publicKey: w.public_key,
		fingerprint: w.fingerprint,
		createdAt: w.created_at,
		lastUsedAt: w.last_used_at || undefined
	};
}

// --- workspace ---------------------------------------------------------------

export function toWorkspaceEntry(w: Wire.WorkspaceEntry): WorkspaceEntry {
	return { name: w.name, path: w.path, dir: w.dir, size: w.size, ignored: w.ignored ?? false };
}

export function toWorkspaceFile(w: Wire.WorkspaceFile): WorkspaceFile {
	return {
		path: w.path,
		size: w.size,
		binary: w.binary,
		truncated: w.truncated,
		content: w.content
	};
}

const changeStatuses: readonly ChangeStatus[] = ['A', 'M', 'D', 'R', 'C', '?'];

export function toChangedFile(w: Wire.WorkspaceChangedFile): ChangedFile {
	return {
		path: w.path,
		status: (changeStatuses as readonly string[]).includes(w.status)
			? (w.status as ChangeStatus)
			: 'M',
		oldPath: w.old_path || undefined,
		additions: w.additions,
		deletions: w.deletions,
		binary: w.binary ?? false,
		committed: w.committed
	};
}

export function toWorkspaceChanges(w: Wire.WorkspaceChanges): WorkspaceChanges {
	return {
		branch: w.branch,
		head: w.head,
		base: w.base,
		mergeBase: w.merge_base,
		commits: list(w.commits).map((c) => ({ sha: c.sha, subject: c.subject })),
		files: list(w.files).map(toChangedFile)
	};
}

export function toFileDiff(w: Wire.WorkspaceDiff): FileDiff {
	return { path: w.path, diff: w.diff, truncated: w.truncated };
}

/** The agent_tool event payload (and a live delta's tool) — open JSON from the
 *  event stream, so read defensively. */
export function toAgentToolCall(p: Record<string, unknown>): AgentToolCall {
	const diffs = Array.isArray(p.diffs) ? (p.diffs as Record<string, unknown>[]) : [];
	return {
		toolCallId: String(p.tool_call_id ?? ''),
		title: String(p.title ?? ''),
		kind: String(p.kind ?? 'other'),
		status: String(p.status ?? ''),
		input: (p.input as string | undefined) || undefined,
		output: (p.output as string | undefined) || undefined,
		locations: Array.isArray(p.locations) ? (p.locations as string[]) : [],
		diffs: diffs.map((d) => ({
			path: String(d.path ?? ''),
			oldText: (d.old_text as string | undefined) || undefined,
			newText: String(d.new_text ?? '')
		})),
		turn: (p.turn as string | undefined) || undefined
	};
}

const deltaKinds = ['message', 'thought', 'tool', 'turn_start', 'turn_end'] as const;

export function toLiveDelta(raw: Record<string, unknown>): LiveDelta | null {
	const kind = raw.kind as LiveDelta['kind'];
	if (!deltaKinds.includes(kind)) return null;
	return {
		stageId: String(raw.stage_id ?? ''),
		iteration: Number(raw.iteration ?? 0),
		turn: (raw.turn as string | undefined) || undefined,
		kind,
		segment: (raw.segment as string | undefined) || undefined,
		text: (raw.text as string | undefined) ?? undefined,
		tool: raw.tool ? toAgentToolCall(raw.tool as Record<string, unknown>) : undefined
	};
}

// --- model provider credentials ----------------------------------------------

export function toProviderInfo(w: Wire.ProviderInfo): ProviderInfo {
	return {
		id: w.id,
		name: w.name,
		key: w.key,
		login: w.login,
		signIn: w.sign_in,
		harnesses: list(w.harnesses)
	};
}

export function toCredential(w: Wire.ProviderCredential): Credential {
	return {
		id: w.id,
		provider: w.provider,
		kind: w.kind === 'oauth' ? 'oauth' : 'api_key',
		name: w.name,
		account: w.meta?.account || undefined,
		expiresAt: w.meta?.expires_at || undefined,
		lastRefresh: w.meta?.last_refresh || undefined,
		status: w.meta?.status === 'disabled' ? 'disabled' : 'ok',
		error: w.meta?.error || undefined,
		createdAt: w.created_at
	};
}

export function toSignInPending(w: Wire.Pending): SignInPending {
	return {
		id: w.id,
		provider: w.provider,
		url: w.url,
		userCode: w.user_code || undefined,
		paste: w.paste,
		intervalSeconds: w.interval_seconds || 5,
		expiresAt: w.expires_at
	};
}

// --- skills -------------------------------------------------------------------

/** Base64 (the wire's bytes) → bytes. */
export function bytesFromBase64(b64: string): Uint8Array {
	const bin = atob(b64);
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}

/** Bytes → base64, for a skill write. */
export function base64FromBytes(bytes: Uint8Array): string {
	let bin = '';
	const chunk = 0x8000;
	for (let i = 0; i < bytes.length; i += chunk)
		bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
	return btoa(bin);
}

export function toSkillSource(w: Wire.Source): SkillSource {
	return {
		kind: w.kind,
		url: w.url,
		host: w.host || undefined,
		owner: w.owner || undefined,
		repo: w.repo || undefined,
		ref: w.ref || undefined,
		path: w.path || undefined,
		commit: w.commit || undefined,
		fetchedAt: w.fetched_at
	};
}

export function toSkillFileEntry(w: Wire.FileEntry): SkillFileEntry {
	return { path: w.path, sha256: w.sha256, size: w.size, mode: w.mode };
}

export function toSkillFileContent(w: Wire.SkillFileRequest): SkillFileContent {
	return { path: w.path, bytes: bytesFromBase64(w.content_b64), mode: w.mode };
}

export function toSkill(w: Wire.Skill): Skill {
	return {
		id: w.id,
		name: w.name,
		description: w.description,
		files: list(w.files).map(toSkillFileEntry),
		hash: w.hash,
		source: w.source ? toSkillSource(w.source) : undefined,
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toSkillDetail(w: Wire.SkillDetail): SkillDetail {
	return { skill: toSkill(w.skill), contents: list(w.contents).map(toSkillFileContent) };
}

export function toSkillset(w: Wire.Skillset): Skillset {
	return {
		id: w.id,
		name: w.name,
		description: w.description,
		source: w.source ? toSkillSource(w.source) : undefined,
		headSeq: w.head_seq,
		skillIds: list(w.skill_ids),
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toSkillsetVersion(w: Wire.Version): SkillsetVersion {
	return {
		skillsetId: w.skillset_id,
		seq: w.seq,
		hash: w.hash,
		members: list(w.members).map((m) => ({
			skillId: m.skill_id,
			name: m.name,
			description: m.description,
			hash: m.hash,
			files: list(m.files).map(toSkillFileEntry)
		})),
		createdAt: w.created_at
	};
}

export function toSkillsetPin(w: Wire.SkillsetPin): SkillsetPin {
	return { id: w.id, name: w.name, seq: w.seq, hash: w.hash };
}

export function toSkillImportResult(w: Wire.ImportResult): SkillImportResult {
	return {
		skill: w.skill ? toSkill(w.skill) : undefined,
		skillset: w.skillset ? toSkillset(w.skillset) : undefined,
		skills: list(w.skills).map(toSkill),
		warnings: w.warnings ?? []
	};
}

export function toSkillsetRefresh(w: Wire.SkillsetRefreshResult): SkillsetRefresh {
	return { skillset: toSkillset(w.skillset), warnings: w.warnings ?? [] };
}

// --- analytics ---------------------------------------------------------------

const GRAINS: Grain[] = ['runs', 'attempts', 'skillsets', 'pings', 'tickets'];
const FIELD_TYPES: AnalyticsFieldType[] = [
	'text',
	'bool',
	'time',
	'count',
	'usd',
	'ms',
	'tokens',
	'ratio'
];
const AGGS: Agg[] = ['sum', 'avg', 'min', 'max', 'median', 'p25', 'p75', 'p90'];
const FILTER_OPS: FilterOp[] = ['eq', 'neq', 'in', 'not_in'];

/** An agg the engine names that this UI does not know reads as none: the
 *  measure still shows, by its catalog label. */
const asAgg = (v: string | undefined): Agg | undefined =>
	AGGS.includes(v as Agg) ? (v as Agg) : undefined;

export function toCatalogField(w: Wire.CatalogField): CatalogField {
	return {
		id: w.id,
		label: w.label,
		type: oneOf(FIELD_TYPES, w.type, 'text'),
		aggs: list(w.aggs).flatMap((a) => asAgg(a) ?? [])
	};
}

export function toCatalog(w: Wire.AnalyticsCatalog): Catalog {
	return {
		grains: list(w.grains).map((g) => ({
			id: oneOf(GRAINS, g.id, 'runs'),
			label: g.label,
			timeField: g.time_field,
			dimensions: list(g.dimensions).map(toCatalogField),
			measures: list(g.measures).map(toCatalogField),
			derived: list(g.derived).map(toCatalogField)
		}))
	};
}

export function toMeasureRef(w: Wire.AnalyticsMeasure): MeasureRef {
	const agg = asAgg(w.agg);
	return agg ? { field: w.field, agg } : { field: w.field };
}

export function toAnalyticsQuery(w: Wire.AnalyticsQuery): AnalyticsQuery {
	const bucket = BUCKETS.find((b) => b === w.time?.bucket);
	return {
		grain: oneOf(GRAINS, w.grain, 'runs'),
		measures: list(w.measures).map(toMeasureRef),
		groupBy: list(w.group_by),
		filters: list(w.filters).map((f) => ({
			field: f.field,
			op: oneOf(FILTER_OPS, f.op, 'eq'),
			values: list(f.values)
		})),
		time: {
			...(w.time?.from ? { from: w.time.from } : {}),
			...(w.time?.to ? { to: w.time.to } : {}),
			...(bucket ? { bucket } : {})
		},
		...(w.order ? { order: { by: w.order.by, desc: w.order.desc ?? false } } : {}),
		...(w.limit ? { limit: w.limit } : {}),
		...(w.score
			? {
					score: {
						scorecard: w.score.scorecard,
						...(w.score.baseline?.length ? { baseline: w.score.baseline } : {})
					}
				}
			: {})
	};
}

export function toAnalyticsResult(w: Wire.AnalyticsResult): AnalyticsResult {
	return {
		dims: list(w.dims).map(toCatalogField),
		measures: list(w.measures).map(toCatalogField),
		rows: list(w.rows).map((r) => ({
			dims: list(r.dims),
			values: list(r.values),
			n: r.n,
			small: r.small,
			...(r.score != null ? { score: r.score } : {})
		})),
		labels: Object.fromEntries(Object.entries(w.labels ?? {}).map(([k, v]) => [k, v ?? {}])),
		truncated: w.truncated
	};
}

export function toPanel(w: Wire.Panel): Panel {
	return {
		id: w.id,
		title: w.title,
		query: toAnalyticsQuery(w.query),
		viz: oneOf(VIZES, w.viz, 'table'),
		width: PANEL_WIDTHS.find((n) => n === w.width) ?? 12
	};
}

export function toDashboard(w: Wire.Dashboard): Dashboard {
	return {
		id: w.id,
		userId: w.user_id || undefined,
		name: w.name,
		builtin: w.builtin,
		filters: list(w.filters),
		panels: list(w.panels).map(toPanel),
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toScorecard(w: Wire.Scorecard): Scorecard {
	return {
		id: w.id,
		userId: w.user_id || undefined,
		name: w.name,
		builtin: w.builtin,
		terms: list(w.terms).map((t) => ({
			measure: toMeasureRef(t.measure),
			direction: oneOf(DIRECTIONS, t.direction, 'lower'),
			weight: t.weight
		})),
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toDrillRun(w: Wire.DrillRun): DrillRun {
	return {
		runId: w.run_id,
		title: w.title,
		flowName: w.flow_name,
		flowHash: w.flow_hash,
		state: oneOf(RUN_STATES, w.state, 'pending'),
		createdAt: w.created_at,
		wallMs: w.wall_ms,
		costUsd: w.cost_usd
	};
}

export function toAnalyticsValue(w: Wire.AnalyticsValue): AnalyticsValue {
	return { value: w.value, label: w.label || w.value };
}
