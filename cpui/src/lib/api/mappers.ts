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
import { normalizeDoc } from '$lib/flowdoc';
import { answerKindOf, toPingOption } from '$lib/ping';
import type {
	ActionIO,
	MergeRequestLive,
	RunPhase,
	ActionSpec,
	Attachment,
	Branch,
	BuildCache,
	ChannelEntry,
	EgressAuditRow,
	EgressDenial,
	EgressPolicy,
	EnvCheck,
	EnvCheckStep,
	Flow,
	FlowValidation,
	FlowVersion,
	Integration,
	KindDescriptor,
	KindField,
	Model,
	Ping,
	PingFile,
	PingPayload,
	PingStatus,
	PingType,
	Port,
	Preflight,
	PreflightStep,
	Project,
	Repo,
	ResourceSample,
	Run,
	RunDetail,
	RunEvent,
	RunFiles,
	RunPorts,
	RunState,
	Settings,
	Snapshot,
	SnapshotKind,
	SourceTicket,
	Ticket,
	UploadLimits,
	WorkspacePolicy,
	AgentToolCall,
	ChangedFile,
	ChangeStatus,
	FileDiff,
	LiveDelta,
	WorkspaceChanges,
	WorkspaceEntry,
	WorkspaceFile
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
	'phase_confirm',
	'select',
	'clarification',
	'blocker',
	'permission'
];
const PING_STATUSES: PingStatus[] = ['pending', 'answered', 'withdrawn'];
const PORTS: Port[] = ['forge', 'provisioner', 'harness', 'ticketsource'];
const SNAPSHOT_KINDS: SnapshotKind[] = ['output', 'log', 'ping_file'];

const oneOf = <T extends string>(allowed: T[], v: string | undefined, fallback: T): T =>
	allowed.includes(v as T) ? (v as T) : fallback;

const list = <T>(v: T[] | null | undefined): T[] => v ?? [];

// --- runs --------------------------------------------------------------------

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
		beginPhase: w.begin_phase || undefined,
		params: w.params ?? undefined,
		startedBy: w.started_by || undefined,
		idle: w.idle ?? false,
		envId: w.env_id,
		envStatus: w.env_status as Run['envStatus'],
		envDirty: w.env_dirty ?? false,
		createdAt: w.created_at,
		updatedAt: w.updated_at
	};
}

export function toRunPhase(w: Wire.PhaseInfo): RunPhase {
	return {
		id: w.id,
		description: w.description || undefined,
		signal: w.signal,
		start: oneOf(['auto', 'confirm', 'manual'] as const, w.start, 'auto'),
		entry: w.entry ?? false,
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
		error: w.error || undefined
	};
}

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
		link: w.link,
		primaryRef: w.payload_ref,
		answerKind: answerKindOf(w.answer_kind, w.type, w.options),
		options: list(w.options).map(toPingOption),
		answer: w.answer,
		feedback: w.feedback,
		answerAttachmentIds: list(w.answer_attachment_ids),
		raisedAt: w.raised_at,
		answeredAt: w.answered_at
	};
}

export function toPingFile(w: Wire.PingFile): PingFile {
	return {
		path: w.path,
		captured: w.captured,
		snapshotId: w.snapshot_id,
		mime: w.mime,
		size: w.size
	};
}

export function toPingPayload(w: Wire.PingFiles): PingPayload {
	return { files: list(w.files).map(toPingFile), link: w.link };
}

// --- tickets -----------------------------------------------------------------

/** The list endpoint returns the ticket alone; get/create/update return it
 *  with its attachments and any import warnings. One domain type either way —
 *  the extras are simply empty on a summary. */
export function toTicket(w: Wire.Ticket | Wire.TicketDetail): Ticket {
	const detail = w as Wire.TicketDetail;
	return {
		id: w.id ?? '',
		projectId: w.project_id ?? '',
		title: w.title ?? '',
		goal: w.goal ?? '',
		baseBranch: w.base_branch ?? '',
		attachmentIds: list(detail.attachment_ids),
		source: w.source_id
			? {
					integrationId: w.source_id,
					kind: w.source_kind ?? '',
					key: w.source_key ?? '',
					url: w.source_url
				}
			: undefined,
		warnings: list(detail.warnings),
		createdAt: w.created_at ?? '',
		updatedAt: w.updated_at ?? ''
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
		}))
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
		forgeId: w.forge_id ?? '',
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
		shadowsBuiltin: w.shadows_builtin ?? false
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
		stages: w.stages,
		agents: w.agents,
		doc: w.doc ? normalizeDoc(w.doc) : undefined,
		parameters: list(w.parameters).map((p) => ({
			name: p.name,
			type: oneOf(['bool', 'int', 'float', 'string'] as const, p.type, 'string'),
			default: p.default,
			description: p.description || undefined,
			enum: p.enum ?? undefined,
			known: p.known ?? false
		})),
		refs: list(w.refs).map((r) => ({
			param: r.param,
			phase: r.phase || undefined,
			stage: r.stage || undefined,
			field: r.field,
			input: r.input || undefined
		})),
		phases: list(w.phases).map((p) => ({
			id: p.id,
			description: p.description || undefined,
			signal: p.signal,
			start: oneOf(['auto', 'confirm', 'manual'] as const, p.start, 'auto'),
			quietFor: p.quiet_for || undefined,
			max: p.max,
			entry: p.entry ?? false,
			subject: p.subject ?? false,
			stages: p.stages
		}))
	};
}

// An action's inputs and outputs are two wire types with one meaning: what a
// stage may bind. The UI treats them alike, so they map to one domain type.
function toActionInput(w: Wire.InputSig): ActionIO {
	return {
		name: w.name,
		description: w.description,
		kind:
			w.kind === 'value'
				? 'value'
				: w.kind === 'enum'
					? 'enum'
					: w.kind === 'text'
						? 'text'
						: 'file',
		required: w.required ?? false,
		defaultPath: w.default_path,
		values: w.values ?? undefined,
		defaultValue: w.default || undefined
	};
}

function toActionOutput(w: Wire.OutputSig): ActionIO {
	return {
		name: w.name,
		description: w.description,
		kind: w.kind === 'value' ? 'value' : 'file',
		required: false,
		sentinel: w.sentinel,
		path: w.path || undefined
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
		inputs: list(w.inputs).map(toActionInput),
		outputs: list(w.outputs).map(toActionOutput)
	};
}

export function toSignalSpec(w: Wire.Signal) {
	return {
		slug: w.slug ?? '',
		title: w.title ?? '',
		description: w.description,
		requires: w.requires,
		needs: w.needs,
		trigger: w.trigger ?? false,
		entry: w.entry ?? false,
		debounced: w.debounced ?? false
	};
}

// --- integrations ------------------------------------------------------------

export function toIntegration(w: Wire.Integration): Integration {
	return {
		id: w.id ?? '',
		port: oneOf(PORTS, w.port, 'forge'),
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
		help: w.help
	};
}

export function toKindDescriptor(w: Wire.KindDescriptor): KindDescriptor {
	return {
		port: oneOf(PORTS, w.port, 'forge'),
		kind: w.kind ?? '',
		label: w.label ?? '',
		help: w.help,
		fields: list(w.fields).map(toKindField)
	};
}

export function toChannelEntry(w: Wire.ChannelEntry): ChannelEntry {
	return { id: w.id ?? '', kind: w.kind ?? '', config: w.config ?? {} };
}

// --- egress ------------------------------------------------------------------

export function toEgressDenial(w: Wire.EgressDenial): EgressDenial {
	return { host: w.host ?? '', count: w.count ?? 0, last: w.last ?? '' };
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
		maxConcurrentRuns: w.max_concurrent_runs ?? 0,
		maxEnvironments: w.max_environments ?? 0,
		destroyCompletedAfter: w.destroy_completed_after ?? '',
		destroyAbandoned: w.destroy_abandoned ?? '',
		orphanUploadsAfter: w.orphan_uploads_after ?? '',
		snapshotsAfter: w.snapshots_after ?? ''
	};
}

export function fromSettings(s: Settings): Wire.Settings {
	return {
		max_concurrent_runs: s.maxConcurrentRuns,
		max_environments: s.maxEnvironments,
		destroy_completed_after: s.destroyCompletedAfter,
		destroy_abandoned: s.destroyAbandoned,
		orphan_uploads_after: s.orphanUploadsAfter,
		snapshots_after: s.snapshotsAfter
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
