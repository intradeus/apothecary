import { describe, expect, it } from 'vitest';
import type * as Wire from './generated/types.gen';
import * as map from './mappers';

/** A wire value with only the fields a test is about: what a partial or older
 *  engine answer looks like to the mapper. */
const wire = <T>(v: object) => v as T;

const AT = '2026-10-31T12:00:00Z';

const aRunView = (over: object = {}) =>
	wire<Wire.RunView>({
		run_id: 'r1',
		state: 'running',
		ticket_title: 'Title',
		flow_name: 'builder',
		flow_hash: 'abc',
		created_at: AT,
		updated_at: AT,
		facts: aFacts(),
		...over
	});

const aFacts = (over: object = {}) =>
	wire<Wire.Facts>({
		last_ts: AT,
		queued_ms: 1,
		provisioning_ms: 2,
		active_ms: 30,
		waiting_human_ms: 400,
		held_ms: 5000,
		paused_ms: 60000,
		cost_usd: 0.5,
		turns: 7,
		attempts: 2,
		...over
	});

const aPingView = (over: object = {}) =>
	wire<Wire.PingView>({
		ping_id: 'p1',
		run_id: 'r1',
		type: 'gate',
		status: 'pending',
		question: 'Sure?',
		raised_at: AT,
		...over
	});

describe('runs', () => {
	it('gives every field the name this app thinks in', () => {
		const run = map.toRun(
			aRunView({
				current_stage: 'build',
				project_id: 'p',
				ticket_id: 't',
				ticket_key: 'K-1',
				ticket_url: 'https://t.test',
				run_goal: 'goal',
				branch: 'b',
				source_branch: 'main',
				mr_url: 'https://mr.test',
				env_id: 'e',
				env_status: 'running',
				env_dirty: true,
				pause_reason: 'budget',
				pause_note: 'budget exceeded: cost 1.20 USD > max 1.00',
				provision_failed: true,
				idle: true,
				phase: 'w',
				phase_subject: 'sub',
				params: { a: '1' },
				started_by: 'me'
			})
		);
		expect(run).toMatchObject({
			id: 'r1',
			state: 'running',
			title: 'Title',
			flowName: 'builder',
			flowHash: 'abc',
			currentStage: 'build',
			projectId: 'p',
			ticketId: 't',
			ticketKey: 'K-1',
			ticketUrl: 'https://t.test',
			goal: 'goal',
			branch: 'b',
			sourceBranch: 'main',
			mrUrl: 'https://mr.test',
			envId: 'e',
			envStatus: 'running',
			envDirty: true,
			pause: { reason: 'budget', note: 'budget exceeded: cost 1.20 USD > max 1.00' },
			provisionFailed: true,
			idle: true,
			phase: 'w',
			phaseSubject: 'sub',
			params: { a: '1' },
			startedBy: 'me',
			createdAt: AT,
			updatedAt: AT
		});
	});

	it('turns absent into the empty value no component has to ask about', () => {
		expect(map.toRun(aRunView())).toMatchObject({
			mrs: [],
			checkouts: [],
			idle: false,
			envDirty: false,
			pause: undefined,
			provisionFailed: false,
			phase: undefined,
			phaseSubject: undefined,
			params: undefined,
			startedBy: undefined,
			skillsets: undefined,
			hold: undefined,
			adopted: undefined
		});
	});

	it.each(['mrs', 'checkouts', 'skillsets'])('reads a null %s as empty', (key) => {
		const run = map.toRun(aRunView({ [key]: null }));
		expect(run[key as 'mrs'] ?? []).toEqual([]);
	});

	it('reads no skillsets for an empty list', () => {
		expect(map.toRun(aRunView({ skillsets: [] })).skillsets).toBeUndefined();
	});

	it.each([
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
	])('keeps the state %s', (state) => {
		expect(map.toRun(aRunView({ state })).state).toBe(state);
	});

	it('sums the six time buckets into wall time and keeps the open attempts', () => {
		const run = map.toRun(
			aRunView({
				facts: aFacts({
					ended_at: AT,
					open: {
						build: { iteration: 3, started_at: AT, outcome: 'paused', stage_id: 'build' }
					}
				})
			})
		);
		expect(run.facts).toEqual({
			costUsd: 0.5,
			turns: 7,
			wallMs: 65433,
			attempts: 2,
			endedAt: AT,
			open: { build: { iteration: 3, startedAt: AT, outcome: 'paused' } }
		});
	});

	it('reads a run with no open attempt and no end', () => {
		expect(map.toRun(aRunView()).facts).toMatchObject({ endedAt: undefined, open: {} });
	});

	it('falls back visibly on a state it does not know', () => {
		expect(map.toRun(aRunView({ state: 'teleporting' })).state).toBe('pending');
	});

	it('reads the nested things a run carries', () => {
		const run = map.toRun(
			aRunView({
				mrs: [{ repo: 'a', branch: 'b', target: 'c', url: 'u', iid: 3 }],
				adopted: { repo: 'r', iid: 1, url: 'u', author: 'a', title: 't' },
				checkouts: [{ name: 'n', dir: 'd', url: 'u', branch: 'b', source: 's' }],
				skillsets: [{ id: 's', name: 'n', seq: 2, hash: 'h' }],
				hold: { stage_id: 'st', user_id: '', since: AT }
			})
		);
		expect(run.mrs).toEqual([{ repo: 'a', branch: 'b', target: 'c', url: 'u', iid: 3 }]);
		expect(run.adopted).toEqual({ repo: 'r', iid: 1, url: 'u', author: 'a', title: 't' });
		expect(run.checkouts[0].primary).toBe(false);
		expect(run.skillsets).toEqual([{ id: 's', name: 'n', seq: 2, hash: 'h' }]);
		expect(run.hold).toEqual({
			stageId: 'st',
			userId: undefined,
			since: AT,
			turns: 0,
			messages: 0,
			costUsd: 0
		});
	});

	it('reads a run’s detail, null events as none', () => {
		const d = map.toRunDetail(wire<Wire.RunDetail>({ run: aRunView(), events: null }));
		expect(d.run.id).toBe('r1');
		expect(d.events).toEqual([]);
	});

	it('reads an event, a null payload as empty', () => {
		const e = map.toRunEvent(
			wire<Wire.Event>({ run_id: 'r', seq: 2, ts: AT, type: 'x', payload: null })
		);
		expect(e).toEqual({ runId: 'r', seq: 2, ts: AT, type: 'x', payload: {} });
	});

	it('reads a phase, narrowing what it does not know', () => {
		const ph = map.toRunPhase(
			wire<Wire.PhaseInfo>({ id: 'w', signal: 's', kind: 'mystery', start: 'whenever' })
		);
		expect(ph).toMatchObject({
			kind: 'watcher',
			start: 'auto',
			runs: {},
			total: 0,
			holds: [],
			live: undefined
		});
	});

	it('reads a phase’s live run and what holds for it', () => {
		const ph = map.toRunPhase(
			wire<Wire.PhaseInfo>({
				id: 'o',
				signal: 's',
				kind: 'outcome',
				start: 'confirm',
				then: 'abort',
				quiet_for_s: 30,
				max: 2,
				runs: { a: 1 },
				total: 1,
				live: { subject: '', parked_at: 'gate' },
				holds: [{ subject: 's', key: 'k', why: 'w', observed_at: AT }]
			})
		);
		expect(ph).toMatchObject({
			kind: 'outcome',
			start: 'confirm',
			then: 'abort',
			quietForS: 30,
			max: 2,
			runs: { a: 1 },
			total: 1,
			live: { subject: undefined, parkedAt: 'gate' },
			holds: [{ subject: 's', key: 'k', why: 'w', seen: false, observedAt: AT }]
		});
	});

	it('reads a resource sample', () => {
		expect(
			map.toResourceSample({
				at: AT,
				cpu_perc: 1,
				cpus: 2,
				mem_used: 3,
				mem_limit: 4,
				net_rx: 5,
				net_tx: 6,
				pids: 7
			})
		).toEqual({
			at: AT,
			cpuPerc: 1,
			cpus: 2,
			memUsed: 3,
			memLimit: 4,
			netRx: 5,
			netTx: 6,
			pids: 7
		});
	});

	it('reads a run’s ports', () => {
		expect(
			map.toRunPorts(
				wire<Wire.RunPorts>({
					ports: [{ port: 80, url: 'u' }],
					observed: [{ port: 81, process: 'p', service: 's' }]
				})
			)
		).toEqual({
			exposed: [{ port: 80, url: 'u' }],
			observed: [{ port: 81, process: 'p', service: 's' }]
		});
		expect(map.toRunPorts(wire<Wire.RunPorts>({}))).toEqual({ exposed: [], observed: [] });
	});

	it('reads what lets one in over SSH, blanks as absent', () => {
		const ssh = map.toRunSsh(
			wire<Wire.RunSsh>({
				enabled: false,
				reason: 'no key',
				host: '',
				extensions: [],
				host_key: 'k',
				vscode_url: 'v',
				keys: 0
			})
		);
		expect(ssh).toMatchObject({
			enabled: false,
			reason: 'no key',
			host: undefined,
			extensions: undefined,
			hostKey: 'k',
			vscodeUrl: 'v',
			keys: 0
		});
		expect(
			map.toRunSsh(wire<Wire.RunSsh>({ enabled: true, extensions: ['a'] })).extensions
		).toEqual(['a']);
	});
});

describe('the Ask pane', () => {
	it.each([
		['answer', 'answer'],
		['question', 'question'],
		['anything', 'question']
	])('reads the role %s as %s', (role, out) => {
		expect(map.toAskMessage(wire<Wire.Message>({ role })).role).toBe(out);
	});

	it('reads a message, blanks as absent and costs as zero', () => {
		expect(
			map.toAskMessage(
				wire<Wire.Message>({ seq: 1, id: 'm', role: 'answer', text: 't', at: AT, user_id: '' })
			)
		).toEqual({
			seq: 1,
			id: 'm',
			role: 'answer',
			text: 't',
			userId: undefined,
			model: undefined,
			costUsd: 0,
			turns: 0,
			error: undefined,
			at: AT
		});
	});

	it('reads the pane, null lists as none', () => {
		expect(
			map.toAskState(
				wire<Wire.AskState>({ enabled: true, busy: false, models: null, messages: null })
			)
		).toEqual({
			enabled: true,
			reason: undefined,
			harness: undefined,
			model: undefined,
			models: [],
			busy: false,
			messages: []
		});
	});
});

describe('merge requests', () => {
	const info = {
		ref: { IID: 5, URL: 'u' },
		title: 't',
		author: 'a',
		source: 's',
		target: 'main'
	};

	it('reads a merge request, its ref unwrapped', () => {
		expect(map.toOpenMergeRequest(wire<Wire.MrInfo>(info))).toEqual({
			iid: 5,
			url: 'u',
			title: 't',
			author: 'a',
			source: 's',
			target: 'main',
			draft: false,
			fork: false
		});
	});

	it('reads a row with nothing else known', () => {
		expect(map.toMergeRequestRow(wire<Wire.MrRow>({ mr: info }))).toMatchObject({
			mine: false,
			stackedOn: undefined,
			ticketId: undefined,
			run: undefined,
			runs: 0
		});
	});

	it('reads a row’s run and what it stacks on', () => {
		const row = map.toMergeRequestRow(
			wire<Wire.MrRow>({
				mr: info,
				mine: true,
				ticket_id: 't',
				runs: 2,
				stacked_on: { iid: 1, url: 'u', title: 't', branch: 'b' },
				run: { id: 'r', state: 'failed', flow: 'f', created_at: AT }
			})
		);
		expect(row).toMatchObject({
			mine: true,
			ticketId: 't',
			runs: 2,
			stackedOn: { iid: 1, branch: 'b' },
			run: { id: 'r', state: 'failed', flow: 'f', createdAt: AT }
		});
	});

	it('reads a live status: the comment time as a date, checks narrowed', () => {
		const live = map.toMergeRequestLive(
			wire<Wire.MrLive>({
				repo: 'r',
				branch: 'b',
				url: 'u',
				state: 'open',
				mergeable: 'yes',
				pending_threads: 1,
				last_comment_at: AT,
				head_sha: 'sha',
				checks: {
					head: 'h',
					state: 'success',
					items: [
						{ name: 'a', state: 'failure', url: '' },
						{ name: 'b', state: 'weird', url: 'u' }
					]
				},
				approval: { approved: true }
			})
		);
		expect(live.lastCommentAt).toEqual(new Date(AT));
		expect(live.headSha).toBe('sha');
		expect(live.checks).toEqual({
			head: 'h',
			state: 'success',
			items: [
				{ name: 'a', state: 'failure', url: undefined },
				{ name: 'b', state: '', url: 'u' }
			]
		});
		expect(live.approval).toEqual({ approved: true, by: [] });
	});

	it('reads a bare live status', () => {
		expect(map.toMergeRequestLive(wire<Wire.MrLive>({ repo: 'r' }))).toMatchObject({
			target: undefined,
			lastCommentAt: undefined,
			mergeableNow: false,
			draft: false,
			checks: undefined,
			approval: undefined,
			stackedOn: undefined,
			error: undefined
		});
	});
});

describe('services', () => {
	it('reads a service, narrowing its state and who started it', () => {
		const s = map.toServiceStatus(
			wire<Wire.ServiceInfo>({
				name: 'web',
				state: 'melting',
				healthy: false,
				by: 'robot',
				exit_code: 0,
				ports: [{ port: 80, label: '', path: '/x', address: 'a', url: '' }]
			})
		);
		expect(s).toMatchObject({
			name: 'web',
			state: 'unknown',
			by: undefined,
			exitCode: 0,
			ports: [{ port: 80, label: undefined, path: '/x', address: 'a', url: undefined }]
		});
	});

	it.each(['stage', 'human'])('keeps who started it: %s', (by) => {
		expect(map.toServiceStatus(wire<Wire.ServiceInfo>({ by })).by).toBe(by);
	});

	it('reads the list, null as none', () => {
		expect(
			map.toRunServices(wire<Wire.RunServices>({ workspace: true, error: '', services: null }))
		).toEqual({ workspace: true, error: undefined, services: [] });
	});
});

describe('pings', () => {
	it('reads a ping, absent lists empty', () => {
		expect(map.toPing(aPingView())).toMatchObject({
			id: 'p1',
			runId: 'r1',
			type: 'gate',
			status: 'pending',
			question: 'Sure?',
			files: [],
			fileTypes: {},
			answerKind: 'yes_no',
			endsPhase: false,
			answerAttachmentIds: [],
			review: [],
			form: undefined,
			content: undefined,
			curated: undefined,
			raisedAt: AT
		});
	});

	it.each<[object, object]>([
		[{ type: 'mystery' }, { type: 'clarification', answerKind: 'form' }],
		[{ status: 'mystery' }, { status: 'pending' }],
		[{ type: 'clarification', answer_kind: 'yes_no' }, { answerKind: 'yes_no' }],
		[{ payload_ref: 'a.md' }, { primaryRef: 'a.md' }],
		[{ phase: '' }, { phase: undefined }],
		[{ ends_phase: true }, { endsPhase: true }]
	])('narrows %j', (over, out) => {
		expect(map.toPing(aPingView(over))).toMatchObject(out);
	});

	it('reads what an answer carried', () => {
		const p = map.toPing(
			aPingView({
				answer: 'accept',
				feedback: 'f',
				content: { a: 1 },
				answer_attachment_ids: ['x'],
				review: [{ path: 'a.md', body: 'b', start_line: 2 }],
				curated: { 'r.yaml': { items: [{ item: 'notes/0', keep: false }] } },
				answered_at: AT
			})
		);
		expect(p).toMatchObject({
			answer: 'accept',
			feedback: 'f',
			content: { a: 1 },
			answerAttachmentIds: ['x'],
			review: [{ path: 'a.md', body: 'b', startLine: 2 }],
			answeredAt: AT
		});
		expect(p.curated?.['r.yaml'].items[0].keep).toBe(false);
	});

	it('sends a curation with only what the person decided', () => {
		expect(
			map.fromCuration({
				summary: 'S',
				items: [
					{ item: 'a', keep: true },
					{ item: 'b', keep: false, body: 'new', resolve: false }
				]
			})
		).toEqual({
			summary: 'S',
			items: [
				{ item: 'a', keep: true },
				{ item: 'b', keep: false, body: 'new', resolve: false }
			]
		});
		expect(map.fromCuration({ items: [] })).toEqual({ items: [] });
	});

	it('sends an emptied summary: the person cleared it', () => {
		expect(map.fromCuration({ summary: '', items: [] })).toHaveProperty('summary', '');
	});

	it.each<[string, Parameters<typeof map.fromReviewComment>[0], object]>([
		['a whole file', { path: 'a', body: 'b', attachmentIds: [] }, { path: 'a', body: 'b' }],
		[
			'one line',
			{ path: 'a', body: 'b', startLine: 3, attachmentIds: [] },
			{ path: 'a', body: 'b', start_line: 3, end_line: 3 }
		],
		[
			'a stretch, with all it can say',
			{
				path: 'a',
				body: 'b',
				startLine: 3,
				endLine: 7,
				heading: 'H',
				quote: 'q',
				attachmentIds: ['x']
			},
			{
				path: 'a',
				body: 'b',
				start_line: 3,
				end_line: 7,
				heading: 'H',
				quote: 'q',
				attachment_ids: ['x']
			}
		]
	])('sends a comment on %s', (_, comment, out) => {
		expect(map.fromReviewComment(comment)).toEqual(out);
	});

	it('reads a ping’s files', () => {
		expect(
			map.toPingPayload(
				wire<Wire.PingFiles>({
					link: 'l',
					files: [
						{ path: 'a', captured: true, snapshot_id: 's', mime: 'm', size: 1, type: '' },
						{ path: 'b', captured: false, type: 'review_threads' }
					]
				})
			)
		).toEqual({
			link: 'l',
			files: [
				{ path: 'a', captured: true, snapshotId: 's', mime: 'm', size: 1, type: undefined },
				{
					path: 'b',
					captured: false,
					snapshotId: undefined,
					mime: undefined,
					size: undefined,
					type: 'review_threads'
				}
			]
		});
		expect(map.toPingPayload(wire<Wire.PingFiles>({ files: null })).files).toEqual([]);
	});

	it('reads a review document', () => {
		const view = map.toCurationView(
			wire<Wire.CurationView>({
				documents: [
					{
						path: 'r.yaml',
						type: 'review_threads',
						captured: true,
						error: '',
						review: {
							notes: [{ path: 'a', line: 1, kind: 'nit', body: 'b', rationale: '', evidence: 'e' }],
							replies: [{ thread: 't', body: 'b' }]
						}
					}
				]
			})
		);
		expect(view.documents[0]).toEqual({
			path: 'r.yaml',
			type: 'review_threads',
			captured: true,
			error: undefined,
			replies: undefined,
			review: {
				summary: '',
				notes: [
					{
						path: 'a',
						line: 1,
						kind: 'nit',
						body: 'b',
						rationale: undefined,
						evidence: 'e',
						example: undefined
					}
				],
				replies: [{ thread: 't', body: 'b', resolve: false, rationale: undefined }]
			}
		});
		expect(view.threads).toBeUndefined();
	});

	it('reads a round’s replies and the threads beside them', () => {
		const view = map.toCurationView(
			wire<Wire.CurationView>({
				documents: [
					{
						path: 'r.yaml',
						type: 'review_replies',
						captured: false,
						error: 'gone',
						replies: [{ thread: 't', did: 'fixed', reply: 'r', resolve: true, evidence: 'e' }]
					}
				],
				threads: {
					merge_request: 'mr',
					threads: [
						{
							id: 't',
							path: 'a',
							line: 2,
							resolved: false,
							ours: true,
							comments: [
								{ id: 'c', author: 'a', ours: true, new: false, body: 'b', did: 'fixed' },
								{ id: 'd', author: 'b', body: 'c' }
							]
						}
					],
					dropped: [{ path: 'a', line: 1, kind: 'nit', body: 'b' }]
				}
			})
		);
		expect(view.documents[0].error).toBe('gone');
		expect(view.documents[0].replies).toEqual([
			{
				thread: 't',
				did: 'fixed',
				reply: 'r',
				resolve: true,
				rationale: undefined,
				evidence: 'e'
			}
		]);
		expect(view.threads?.mergeRequest).toBe('mr');
		expect(view.threads?.threads[0].comments.map((c) => [c.ours, c.new])).toEqual([
			[true, false],
			[false, false]
		]);
		expect(view.threads?.dropped).toHaveLength(1);
	});

	it('reads no documents as none', () => {
		expect(map.toCurationView(wire<Wire.CurationView>({ documents: null })).documents).toEqual([]);
	});
});

describe('tickets', () => {
	it('reads a summary with empty extras', () => {
		expect(map.toTicket(wire<Wire.TicketView>({ id: 't', title: 'x' }))).toEqual({
			id: 't',
			projectId: '',
			title: 'x',
			goal: '',
			baseBranch: '',
			changeType: 'feat',
			attachmentIds: [],
			source: undefined,
			status: undefined,
			type: undefined,
			labels: [],
			links: [],
			syncState: '',
			syncError: undefined,
			syncedAt: undefined,
			statusChangedAt: undefined,
			statusChangedBy: undefined,
			warnings: [],
			createdAt: '',
			updatedAt: ''
		});
	});

	it('reads a detail: attachments, warnings and where it came from', () => {
		const t = map.toTicket(
			wire<Wire.TicketDetail>({
				id: 't',
				base_branch: 'fix/x',
				change_type: 'fix',
				attachment_ids: ['a'],
				warnings: ['w'],
				source_id: 'jira',
				source_kind: 'jira',
				source_key: 'K-1',
				source_url: 'u',
				source_type: ''
			})
		);
		expect(t).toMatchObject({
			baseBranch: 'fix/x',
			changeType: 'fix',
			attachmentIds: ['a'],
			warnings: ['w'],
			source: { integrationId: 'jira', kind: 'jira', key: 'K-1', url: 'u', type: undefined }
		});
	});

	it('reads the tracker state: status, type, labels, links and sync', () => {
		const t = map.toTicket(
			wire<Wire.TicketDetail>({
				id: 't',
				status: { id: 's', name: 'In Review', is_done: false, flow_step: 'in_review' },
				type: { id: 'y', name: 'Bug' },
				labels: ['ui'],
				links: [{ ticket_id: 't', url: 'https://x', title: 'Spec' }],
				sync_state: 'error',
				sync_error: 'timeout',
				synced_at: '2026-01-01T00:00:00Z'
			})
		);
		expect(t).toMatchObject({
			status: { id: 's', name: 'In Review', isDone: false, flowStep: 'in_review' },
			type: { id: 'y', name: 'Bug' },
			labels: ['ui'],
			links: [{ url: 'https://x', title: 'Spec' }],
			syncState: 'error',
			syncError: 'timeout',
			syncedAt: '2026-01-01T00:00:00Z'
		});
	});

	it('reads an unknown sync state and flow step as none', () => {
		const t = map.toTicket(
			wire<Wire.TicketView>({
				sync_state: 'weird',
				status: { id: 's', name: 'Odd', is_done: true, flow_step: 'none' }
			})
		);
		expect(t.syncState).toBe('');
		expect(t.status).toEqual({ id: 's', name: 'Odd', isDone: true, flowStep: undefined });
	});

	it('reads a comment, an unknown origin as remote', () => {
		const c = wire<Wire.TicketComment>({
			id: 'c',
			author: 'ann',
			body_md: 'hi',
			origin: 'apothecary',
			run_id: 'r1',
			stage_id: '',
			created_at: 'a',
			updated_at: 'b'
		});
		expect(map.toTicketComment(c)).toEqual({
			id: 'c',
			author: 'ann',
			bodyMd: 'hi',
			origin: 'apothecary',
			runId: 'r1',
			stageId: undefined,
			createdAt: 'a',
			updatedAt: 'b'
		});
		expect(map.toTicketComment({ ...c, origin: 'bot' }).origin).toBe('remote');
	});

	it('round-trips a tracker status, leaving no step off the wire', () => {
		const w: Wire.TrackerStatus = {
			id: 's',
			integration_id: 'i',
			remote_id: '3',
			name: 'Done',
			remote_category: 'done',
			position: 2,
			is_done: true,
			flow_step: 'done'
		};
		expect(map.fromTrackerStatus(map.toTrackerStatus(w))).toEqual(w);
		const { flow_step: _, ...noStep } = w;
		expect(map.fromTrackerStatus(map.toTrackerStatus(noStep))).toEqual(noStep);
	});

	it('round-trips a tracker type, an unknown change type as feat', () => {
		const w: Wire.TrackerType = {
			id: 'y',
			integration_id: 'i',
			remote_id: '1',
			name: 'Bug',
			change_type: 'fix',
			position: 0
		};
		expect(map.fromTrackerType(map.toTrackerType(w))).toEqual(w);
		expect(map.toTrackerType({ ...w, change_type: '' }).changeType).toBe('feat');
	});

	it('reads a tracker’s ticket', () => {
		expect(
			map.toSourceTicket(
				wire<Wire.SourceTicket>({
					key: 'K-1',
					change_type: 'chore',
					attachments: [{ filename: 'a.txt' }, {}]
				})
			)
		).toMatchObject({
			key: 'K-1',
			title: '',
			changeType: 'chore',
			branch: '',
			attachments: [{ filename: 'a.txt' }, { filename: '' }]
		});
	});
});

describe('projects', () => {
	it('reads a bare project without tripping on what is absent', () => {
		expect(map.toProject(wire<Wire.Project>({}))).toEqual({
			id: '',
			name: '',
			repoAccountId: '',
			provisionerId: '',
			harnessIds: [],
			repo: { url: '', locator: '', defaultBranch: '', agentsMdPath: '' },
			repos: [],
			commands: {},
			workspace: {
				extraDomains: [],
				egressMode: undefined,
				deniedDomains: [],
				blockedDomains: [],
				allowedCidrs: [],
				allowedPorts: [],
				codeServerExtensions: [],
				services: [],
				browserEnabled: false,
				docker: false,
				editor: undefined
			},
			defaultFlow: '',
			createdAt: '',
			updatedAt: ''
		});
	});

	it('reads a workspace’s policy, services and editor', () => {
		const p = map.toProject(
			wire<Wire.Project>({
				workspace: {
					extra_domains: ['a'],
					egress_mode: 'deny',
					denied_domains: ['b'],
					blocked_domains: ['c'],
					egress_allowed_cidrs: ['10.0.0.0/8'],
					egress_allowed_ports: [22],
					code_server_extensions: ['e'],
					browser: { enabled: true },
					docker: true,
					editor: {
						theme: 't',
						font_size: 12,
						tab_size: 2,
						format_on_save: true,
						word_wrap: false,
						trust_workspace: true
					},
					services: [
						{
							name: 's',
							start: 'run',
							stop: '',
							description: 'd',
							health: { http: '/h', timeout: '5s' },
							ports: [{ port: 80, label: 'l', path: '' }]
						},
						{ name: 'quiet', start: 'run', health: {} }
					]
				}
			})
		);
		expect(p.workspace).toMatchObject({
			extraDomains: ['a'],
			egressMode: 'deny',
			deniedDomains: ['b'],
			blockedDomains: ['c'],
			allowedCidrs: ['10.0.0.0/8'],
			allowedPorts: [22],
			codeServerExtensions: ['e'],
			browserEnabled: true,
			docker: true,
			editor: {
				theme: 't',
				fontSize: 12,
				tabSize: 2,
				formatOnSave: true,
				wordWrap: false,
				trustWorkspace: true
			}
		});
		expect(p.workspace.services[0]).toEqual({
			name: 's',
			description: 'd',
			start: 'run',
			stop: undefined,
			health: {
				http: '/h',
				tcp: undefined,
				command: undefined,
				timeout: '5s',
				interval: undefined
			},
			ports: [{ port: 80, label: 'l', path: undefined }]
		});
		expect(p.workspace.services[1].health).toBeUndefined();
	});

	it('reads the extra repositories', () => {
		const p = map.toProject(
			wire<Wire.Project>({
				repos: [{ name: 'n', url: 'u', locator: '', default_branch: 'main' }]
			})
		);
		expect(p.repos).toEqual([{ name: 'n', url: 'u', locator: undefined, defaultBranch: 'main' }]);
	});

	it('reads the small things around a project', () => {
		expect(map.toBranch({ name: 'main', default: true })).toEqual({
			name: 'main',
			isDefault: true
		});
		expect(map.toBranch(wire<Wire.BranchInfo>({ name: 'x' })).isDefault).toBe(false);
		expect(map.toRepo(wire<Wire.RepoInfo>({ default_branch: 'm' })).defaultBranch).toBe('m');
		expect(map.toModel(wire<Wire.ModelInfo>({}))).toEqual({ id: '', name: '' });
		expect(map.toPreflight(wire<Wire.PreflightResult>({ ok: true, steps: null }))).toEqual({
			ok: true,
			steps: []
		});
		expect(
			map.toPreflight(
				wire<Wire.PreflightResult>({
					ok: false,
					steps: [{ id: 's', ok: false, error: 'e' }]
				})
			).steps
		).toEqual([{ id: 's', ok: false, error: 'e' }]);
		expect(map.toProjectSecret({ name: 'n', updated_at: AT })).toEqual({
			name: 'n',
			updatedAt: AT
		});
		expect(map.toBuildCache({ available: true, bytes: 3 })).toEqual({ available: true, bytes: 3 });
	});

	it.each<[string | undefined, string]>([
		['running', 'running'],
		['stopped', 'stopped'],
		['', 'absent'],
		['paused', 'absent'],
		[undefined, 'absent']
	])('reads the browser’s state %j as %s', (state, out) => {
		const b = map.toBrowserStatus(
			wire<Wire.BrowserInfo>({ enabled: true, available: true, sessions: 1, state })
		);
		expect(b).toMatchObject({ enabled: true, available: true, sessions: 1, state: out });
	});

	it('reads an environment check, starting as running and steps as skipped', () => {
		expect(map.toEnvCheck(wire<Wire.EnvCheck>({ steps: [{}] }))).toEqual({
			id: '',
			projectId: '',
			status: 'running',
			keep: false,
			tearingDown: false,
			steps: [{ id: '', status: 'skip', detail: undefined }],
			startedAt: '',
			finishedAt: undefined
		});
	});
});

describe('files', () => {
	it('reads an attachment and a snapshot, defaults filled', () => {
		expect(map.toAttachment(wire<Wire.Attachment>({ id: 'a', size: 2 }))).toMatchObject({
			id: 'a',
			filename: '',
			mime: '',
			size: 2,
			sha256: '',
			createdAt: ''
		});
		expect(map.toSnapshot(wire<Wire.Snapshot>({ kind: 'mystery' }))).toMatchObject({
			kind: 'output',
			truncated: false,
			size: 0
		});
	});

	it.each(['output', 'log', 'ping_file'])('keeps the snapshot kind %s', (kind) => {
		expect(map.toSnapshot(wire<Wire.Snapshot>({ kind })).kind).toBe(kind);
	});

	it('reads a run’s files, null lists as none', () => {
		expect(map.toRunFiles(wire<Wire.RunFiles>({ attachments: null, snapshots: null }))).toEqual({
			attachments: [],
			snapshots: []
		});
	});

	it('reads the upload limits', () => {
		expect(
			map.toUploadLimits(wire<Wire.UploadLimits>({ max_bytes: 1, max_files: 2, mime_allow: ['x'] }))
		).toEqual({ maxBytes: 1, maxFiles: 2, mimeAllow: ['x'] });
		expect(map.toUploadLimits(wire<Wire.UploadLimits>({}))).toEqual({
			maxBytes: 0,
			maxFiles: 0,
			mimeAllow: []
		});
	});
});

describe('flows', () => {
	it('reads a flow with nothing known about it', () => {
		expect(map.toFlow(wire<Wire.Flow>({}))).toEqual({
			projectId: '',
			name: '',
			yaml: '',
			hash: '',
			layout: undefined,
			updatedAt: '',
			builtin: false,
			shadowsBuiltin: false,
			operatesOn: undefined,
			parameters: [],
			phases: []
		});
	});

	it.each<[string | undefined, string | undefined]>([
		[undefined, undefined],
		['', undefined],
		['ticket', 'ticket'],
		['merge_request', 'merge_request'],
		['mystery', 'ticket']
	])('reads operates_on %j as %j', (operates_on, out) => {
		expect(map.toFlow(wire<Wire.Flow>({ operates_on })).operatesOn).toBe(out);
		expect(
			map.toFlowValidation(wire<Wire.FlowValidation>({ ok: true, operates_on })).operatesOn
		).toBe(out);
	});

	it('reads a declared parameter', () => {
		const [p] = map.toFlow(
			wire<Wire.Flow>({
				parameters: [
					{
						name: 'm',
						type: 'mystery',
						list: false,
						default: 'x',
						description: '',
						enum: null,
						required: true,
						default_ref: 'other',
						role: 'model',
						harness: 'h'
					}
				]
			})
		).parameters;
		expect(p).toEqual({
			name: 'm',
			type: 'string',
			list: undefined,
			default: 'x',
			description: undefined,
			enum: undefined,
			known: false,
			required: true,
			defaultRef: 'other',
			role: 'model',
			harness: 'h'
		});
	});

	it.each<[string | undefined, string | undefined]>([
		['model', 'model'],
		['harness', 'harness'],
		['other', undefined],
		[undefined, undefined]
	])('reads the parameter role %j', (role, out) => {
		const flow = map.toFlow(wire<Wire.Flow>({ parameters: [{ name: 'p', role }] }));
		expect(flow.parameters[0].role).toBe(out);
	});

	it('reads a phase summary, narrowing what it does not know', () => {
		const [a, b] = map.toFlow(
			wire<Wire.Flow>({
				phases: [
					{ id: 'a', kind: 'mystery', signal: 's', start: 'never', then: 'later', stages: 2 },
					{
						id: 'b',
						kind: 'outcome',
						signal: 's',
						start: 'confirm',
						then: 'abort',
						quiet_for: '5m',
						subject: true
					}
				]
			})
		).phases;
		expect(a).toMatchObject({ kind: 'watcher', start: 'auto', then: undefined, subject: false });
		expect(b).toMatchObject({
			kind: 'outcome',
			start: 'confirm',
			then: 'abort',
			quietFor: '5m',
			subject: true
		});
	});

	it('hands a validation’s document to the editor normalised', () => {
		const v = map.toFlowValidation(
			wire<Wire.FlowValidation>({
				ok: true,
				doc: {
					name: 'f',
					version: 5,
					parameters: { harness: 'h' },
					main: [{ id: 'a', type: 'command', run: 'r' }]
				}
			})
		);
		expect(v.doc?.stages).toMatchObject([{ id: 'a', phase: 'main', on_success: 'done' }]);
		expect(v.doc?.parameters.harness).toEqual({ default: 'h' });
	});

	it('reads a refused validation', () => {
		const v = map.toFlowValidation(
			wire<Wire.FlowValidation>({ ok: false, errors: ['e'], warnings: null })
		);
		expect(v).toMatchObject({
			ok: false,
			errors: ['e'],
			warnings: [],
			doc: undefined,
			parameters: [],
			refs: [],
			phases: []
		});
	});

	it('reads where parameters are read', () => {
		const v = map.toFlowValidation(
			wire<Wire.FlowValidation>({
				ok: true,
				refs: [{ param: 'p', field: 'with', phase: '', stage: 's', input: 'i' }]
			})
		);
		expect(v.refs).toEqual([
			{ param: 'p', phase: undefined, stage: 's', field: 'with', input: 'i' }
		]);
	});

	it('reads a version and a binding', () => {
		expect(map.toFlowVersion(wire<Wire.FlowVersion>({ hash: 'h' }))).toEqual({
			hash: 'h',
			name: '',
			yaml: '',
			createdAt: ''
		});
		const params = { a: '1' };
		const b = map.toFlowBinding(wire<Wire.FlowBinding>({ flow_name: 'f', params }));
		expect(b).toMatchObject({ flowName: 'f', params });
		expect(b.params).not.toBe(params);
		expect(map.toFlowBinding(wire<Wire.FlowBinding>({})).params).toEqual({});
	});

	it('reads an automation', () => {
		expect(
			map.toAutomation(
				wire<Wire.Automation>({
					kind: 'cron',
					schedule: '@daily',
					enabled: true,
					params: { a: '1' }
				})
			)
		).toMatchObject({ kind: 'cron', schedule: '@daily', enabled: true, params: { a: '1' } });
		expect(map.toAutomation(wire<Wire.Automation>({ kind: 'mystery' }))).toMatchObject({
			kind: 'tracker_query',
			sourceId: undefined,
			enabled: false,
			params: {}
		});
	});

	it('reads an action, its ports and settings', () => {
		const a = map.toActionSpec(
			wire<Wire.Action>({
				slug: 'push',
				title: 'Push',
				exactly_one: ['a', 'b'],
				inputs: [{ name: 'i', default_path: 'p' }],
				outputs: null,
				settings: [
					{ name: 's', kind: 'mystery', default: '' },
					{ name: 't', kind: 'enum', values: ['a'], default: 'a', required: true }
				]
			})
		);
		expect(a).toMatchObject({
			slug: 'push',
			single: false,
			remote: false,
			exactlyOne: ['a', 'b'],
			inputs: [{ name: 'i', required: false, defaultPath: 'p' }],
			outputs: [],
			settings: [
				{ name: 's', kind: 'text', required: false, defaultValue: undefined },
				{ name: 't', kind: 'enum', required: true, values: ['a'], defaultValue: 'a' }
			]
		});
		expect(map.toActionSpec(wire<Wire.Action>({ exactly_one: [] })).exactlyOne).toBeUndefined();
	});

	it.each<[string | undefined, string]>([
		['hand', 'hand'],
		['observed', 'observed'],
		['mystery', 'observed'],
		[undefined, 'observed']
	])('reads the signal kind %j', (kind, out) => {
		expect(map.toSignalSpec(wire<Wire.Signal>({ kind }))).toMatchObject({
			kind: out,
			debounced: false,
			all: false
		});
	});
});

describe('integrations', () => {
	it('reads an integration', () => {
		expect(map.toIntegration(wire<Wire.Integration>({ port: 'mystery' }))).toEqual({
			id: '',
			port: 'repository',
			kind: '',
			name: '',
			config: {},
			secrets: {},
			createdAt: '',
			updatedAt: ''
		});
	});

	it.each(['repository', 'provisioner', 'harness', 'ticketsource'])('keeps the port %s', (port) => {
		expect(map.toIntegration(wire<Wire.Integration>({ port })).port).toBe(port);
	});

	it('reads what a kind asks for', () => {
		const k = map.toKindDescriptor(
			wire<Wire.KindDescriptor>({
				port: 'harness',
				kind: 'k',
				label: 'L',
				fields: [{ name: 'a', type: 'credentials', secret: true }, { type: 'text' }]
			})
		);
		expect(k).toMatchObject({
			port: 'harness',
			kind: 'k',
			label: 'L',
			fields: [
				{ name: 'a', type: 'credentials', secret: true, required: false },
				{ name: '', type: undefined, secret: false }
			]
		});
	});

	it('reads a channel', () => {
		expect(map.toChannelEntry(wire<Wire.ChannelEntry>({}))).toEqual({
			id: '',
			kind: '',
			config: {}
		});
	});
});

describe('notices', () => {
	it('reads a notice, defaulting what the engine left out', () => {
		expect(
			map.toNotice(
				wire<Wire.Notice>({ id: 'n', kind: 'k', title: 'T', created_at: 'c', last_seen_at: 'l' })
			)
		).toEqual({
			id: 'n',
			kind: 'k',
			title: 'T',
			body: '',
			link: '',
			count: 1,
			createdAt: 'c',
			lastSeenAt: 'l'
		});
	});

	it('keeps its body, link and count', () => {
		expect(
			map.toNotice(wire<Wire.Notice>({ body: 'b', link: '/system/channels', count: 3 }))
		).toMatchObject({ body: 'b', link: '/system/channels', count: 3 });
	});
});

describe('egress', () => {
	it.each<[string | undefined, string]>([
		['allowed', 'allowed'],
		['blocked', 'blocked'],
		['held', 'held'],
		['mystery', 'held'],
		[undefined, 'held']
	])('reads a denial of status %j as %s: unknown stays held', (status, out) => {
		expect(map.toEgressDenial(wire<Wire.EgressDenial>({ status })).status).toBe(out);
	});

	it('reads an audit row', () => {
		expect(
			map.toEgressAuditRow(wire<Wire.EgressAuditRow>({ verdict: 'deny', dst_ip: '1.1.1.1' }))
		).toMatchObject({ verdict: 'deny', dstIp: '1.1.1.1', port: 0, bytesOut: 0 });
		expect(map.toEgressAuditRow(wire<Wire.EgressAuditRow>({})).verdict).toBe('allow');
	});

	it('reads the rule in force', () => {
		expect(
			map.toEgressPolicy(
				wire<Wire.EgressPolicy>({ mode: 'deny', domains: ['a'], blocked_domains: null })
			)
		).toEqual({ mode: 'deny', domains: ['a'], blockedDomains: [] });
		expect(map.toEgressPolicy(wire<Wire.EgressPolicy>({})).mode).toBe('allow');
	});
});

describe('settings and limits', () => {
	it('reads settings and sends them back unchanged', () => {
		const w = {
			destroy_completed_after: '48h',
			destroy_abandoned: 'never',
			orphan_uploads_after: '1h',
			snapshots_after: '30d'
		};
		expect(map.fromSettings(map.toSettings(w))).toEqual(w);
		expect(map.toSettings(wire<Wire.Settings>({})).destroyAbandoned).toBe('');
	});

	it.each<[Wire.LimitsInfo, object]>([
		[
			{ max_concurrent_runs: 3, max_environments: 0 },
			{ maxConcurrentRuns: 3, maxEnvironments: 0 }
		],
		[
			{ max_concurrent_runs: -1, max_environments: -1 },
			{ maxConcurrentRuns: null, maxEnvironments: null }
		]
	])('turns %j into %j and back', (w, domain) => {
		expect(map.toLimits(w)).toEqual(domain);
		expect(map.fromLimits(map.toLimits(w))).toEqual(w);
	});
});

describe('accounts', () => {
	const user = {
		id: 'u',
		handle: 'h',
		email: 'e',
		name: 'n',
		admin: true,
		created_at: AT
	};

	it.each<[object, boolean]>([
		[{}, false],
		[{ disabled_at: AT }, true],
		[{ disabled_at: '' }, false]
	])('reads a user whose account is %j', (over, disabled) => {
		expect(map.toUser(wire<Wire.User>({ ...user, ...over })).disabled).toBe(disabled);
	});

	it('reads who is signed in, and how one may', () => {
		expect(map.toMe(wire<Wire.Me>({ user, methods: null }))).toMatchObject({
			user: { id: 'u', handle: 'h', admin: true, createdAt: AT },
			methods: []
		});
		expect(
			map.toAuthStatus(wire<Wire.AuthStatus>({ setup_required: true, methods: ['password'] }))
		).toEqual({
			setupRequired: true,
			methods: ['password']
		});
	});

	it('reads a token and an SSH key, blanks as absent', () => {
		expect(
			map.toToken(
				wire<Wire.Token>({ id: 't', name: 'n', prefix: 'p', created_at: AT, last_used_at: '' })
			)
		).toEqual({
			id: 't',
			name: 'n',
			prefix: 'p',
			createdAt: AT,
			lastUsedAt: undefined,
			expiresAt: undefined
		});
		expect(
			map.toSshKey(
				wire<Wire.SshKey>({
					id: 'k',
					name: 'n',
					type: 'ed25519',
					public_key: 'pk',
					fingerprint: 'f',
					created_at: AT
				})
			)
		).toMatchObject({ publicKey: 'pk', lastUsedAt: undefined });
	});
});

describe('the workspace', () => {
	it('reads an entry and a file', () => {
		expect(
			map.toWorkspaceEntry(wire<Wire.WorkspaceEntry>({ name: 'a', path: 'a', dir: true }))
		).toEqual({
			name: 'a',
			path: 'a',
			dir: true,
			size: undefined,
			ignored: false
		});
		const file = { path: 'a', size: 1, binary: false, truncated: true, content: 'c' };
		expect(map.toWorkspaceFile(file)).toEqual(file);
	});

	it.each([
		['A', 'A'],
		['M', 'M'],
		['D', 'D'],
		['R', 'R'],
		['C', 'C'],
		['?', '?'],
		['X', 'M']
	])('reads the change status %s as %s', (status, out) => {
		expect(map.toChangedFile(wire<Wire.WorkspaceChangedFile>({ status })).status).toBe(out);
	});

	it('reads the changes, null lists as none', () => {
		expect(
			map.toWorkspaceChanges(
				wire<Wire.WorkspaceChanges>({
					branch: 'b',
					head: 'h',
					base: 'b0',
					merge_base: 'm',
					commits: null,
					files: null
				})
			)
		).toEqual({ branch: 'b', head: 'h', base: 'b0', mergeBase: 'm', commits: [], files: [] });
	});

	it('reads a renamed file and a commit', () => {
		const c = map.toWorkspaceChanges(
			wire<Wire.WorkspaceChanges>({
				commits: [{ sha: 's', subject: 'x', extra: 1 }],
				files: [
					{ path: 'b', status: 'R', old_path: 'a', additions: 1, deletions: 2, committed: true }
				]
			})
		);
		expect(c.commits).toEqual([{ sha: 's', subject: 'x' }]);
		expect(c.files[0]).toEqual({
			path: 'b',
			status: 'R',
			oldPath: 'a',
			additions: 1,
			deletions: 2,
			binary: false,
			committed: true
		});
	});

	it('reads a diff', () => {
		expect(map.toFileDiff({ path: 'a', diff: 'd', truncated: false })).toEqual({
			path: 'a',
			diff: 'd',
			truncated: false
		});
	});
});

describe('the live agent stream', () => {
	it('reads a tool call defensively: it is open JSON', () => {
		expect(map.toAgentToolCall({})).toEqual({
			toolCallId: '',
			title: '',
			kind: 'other',
			status: '',
			input: undefined,
			output: undefined,
			locations: [],
			diffs: [],
			turn: undefined
		});
		expect(map.toAgentToolCall({ locations: 'x', diffs: 'y' })).toMatchObject({
			locations: [],
			diffs: []
		});
	});

	it('reads a tool call in full', () => {
		expect(
			map.toAgentToolCall({
				tool_call_id: 't',
				title: 'edit',
				kind: 'edit',
				status: 'done',
				input: 'i',
				output: 'o',
				locations: ['a'],
				diffs: [{ path: 'a', old_text: 'x', new_text: 'y' }, {}],
				turn: 'u'
			})
		).toEqual({
			toolCallId: 't',
			title: 'edit',
			kind: 'edit',
			status: 'done',
			input: 'i',
			output: 'o',
			locations: ['a'],
			diffs: [
				{ path: 'a', oldText: 'x', newText: 'y' },
				{ path: '', oldText: undefined, newText: '' }
			],
			turn: 'u'
		});
	});

	it.each(['message', 'thought', 'tool', 'turn_start', 'turn_end'])('keeps a %s delta', (kind) => {
		expect(map.toLiveDelta({ kind })?.kind).toBe(kind);
	});

	it.each([[{}], [{ kind: 'mystery' }]])('drops the delta %j', (raw) => {
		expect(map.toLiveDelta(raw)).toBeNull();
	});

	it('reads a delta, keeping an empty text: the agent wrote nothing yet', () => {
		expect(
			map.toLiveDelta({
				kind: 'tool',
				stage_id: 's',
				iteration: '2',
				text: '',
				tool: { title: 't' }
			})
		).toMatchObject({
			stageId: 's',
			iteration: 2,
			text: '',
			tool: { title: 't' },
			turn: undefined,
			segment: undefined
		});
		expect(map.toLiveDelta({ kind: 'message' })).toMatchObject({
			stageId: '',
			iteration: 0,
			text: undefined,
			tool: undefined
		});
	});
});

describe('model credentials', () => {
	it('reads a provider', () => {
		expect(
			map.toProviderInfo(
				wire<Wire.ProviderInfo>({
					id: 'p',
					name: 'n',
					key: true,
					login: false,
					sign_in: true,
					harnesses: null
				})
			)
		).toEqual({ id: 'p', name: 'n', key: true, login: false, signIn: true, harnesses: [] });
	});

	it('reads a credential, narrowing its kind and status', () => {
		expect(
			map.toCredential(wire<Wire.ProviderCredential>({ id: 'c', kind: 'mystery', created_at: AT }))
		).toMatchObject({
			kind: 'api_key',
			status: 'ok',
			account: undefined,
			error: undefined,
			createdAt: AT
		});
		expect(
			map.toCredential(
				wire<Wire.ProviderCredential>({
					kind: 'oauth',
					meta: {
						account: 'a',
						expires_at: 'e',
						last_refresh: 'r',
						status: 'disabled',
						error: 'x'
					}
				})
			)
		).toMatchObject({
			kind: 'oauth',
			account: 'a',
			expiresAt: 'e',
			lastRefresh: 'r',
			status: 'disabled',
			error: 'x'
		});
	});

	it('polls a pending sign-in every five seconds unless told otherwise', () => {
		expect(map.toSignInPending(wire<Wire.Pending>({ id: 'p' })).intervalSeconds).toBe(5);
		expect(
			map.toSignInPending(wire<Wire.Pending>({ interval_seconds: 2, user_code: 'c' }))
		).toMatchObject({ intervalSeconds: 2, userCode: 'c' });
	});
});

describe('skills', () => {
	const MAN = Uint8Array.of(77, 97, 110);

	it.each<[string, Uint8Array]>([
		['nothing', new Uint8Array()],
		['text', Uint8Array.of(104, 195, 169, 108)],
		['every byte', Uint8Array.from({ length: 256 }, (_, i) => i)],
		['more than one chunk', new Uint8Array(0x8000 * 2 + 3).fill(0xab)]
	])('carries %s through base64 unchanged', (_, bytes) => {
		expect(map.bytesFromBase64(map.base64FromBytes(bytes))).toEqual(bytes);
	});

	it('speaks standard base64', () => {
		expect(map.base64FromBytes(MAN)).toBe('TWFu');
		expect(map.bytesFromBase64('TWFu')).toEqual(MAN);
	});

	const skill = {
		id: 's',
		name: 'n',
		description: 'd',
		files: [{ path: 'a', sha256: 'h', size: 1, mode: 420 }],
		hash: 'h',
		created_at: AT,
		updated_at: AT
	};
	const source = {
		kind: 'github',
		url: 'u',
		host: '',
		owner: 'o',
		repo: 'r',
		ref: 'main',
		path: '',
		commit: 'c',
		fetched_at: AT
	};

	it('reads a skill and where it came from', () => {
		expect(map.toSkill(wire<Wire.Skill>({ ...skill, source }))).toEqual({
			id: 's',
			name: 'n',
			description: 'd',
			files: [{ path: 'a', sha256: 'h', size: 1, mode: 420 }],
			hash: 'h',
			source: {
				kind: 'github',
				url: 'u',
				host: undefined,
				owner: 'o',
				repo: 'r',
				ref: 'main',
				path: undefined,
				commit: 'c',
				fetchedAt: AT
			},
			createdAt: AT,
			updatedAt: AT
		});
		expect(map.toSkill(wire<Wire.Skill>({ files: null })).files).toEqual([]);
	});

	it('decodes a skill’s contents into bytes', () => {
		const d = map.toSkillDetail(
			wire<Wire.SkillDetail>({
				skill,
				contents: [{ path: 'a', content_b64: 'TWFu', mode: 420 }]
			})
		);
		expect(d.contents).toEqual([{ path: 'a', bytes: MAN, mode: 420 }]);
		expect(map.toSkillDetail(wire<Wire.SkillDetail>({ skill, contents: null })).contents).toEqual(
			[]
		);
	});

	it('reads a skillset, a version and a pin', () => {
		expect(
			map.toSkillset(wire<Wire.Skillset>({ id: 'x', head_seq: 3, skill_ids: null }))
		).toMatchObject({ id: 'x', headSeq: 3, skillIds: [], source: undefined });
		expect(
			map.toSkillsetVersion(
				wire<Wire.Version>({
					skillset_id: 'x',
					seq: 2,
					members: [{ skill_id: 's', name: 'n', files: null }]
				})
			)
		).toMatchObject({ skillsetId: 'x', seq: 2, members: [{ skillId: 's', files: [] }] });
		expect(map.toSkillsetPin({ id: 'x', name: 'n', seq: 1, hash: 'h' })).toEqual({
			id: 'x',
			name: 'n',
			seq: 1,
			hash: 'h'
		});
	});

	it('reads an import’s result and a refresh', () => {
		expect(map.toSkillImportResult(wire<Wire.ImportResult>({ skills: null }))).toEqual({
			skill: undefined,
			skillset: undefined,
			skills: [],
			warnings: []
		});
		expect(
			map.toSkillImportResult(
				wire<Wire.ImportResult>({ skill, skillset: { id: 'x' }, skills: [skill], warnings: ['w'] })
			)
		).toMatchObject({ skill: { id: 's' }, skillset: { id: 'x' }, warnings: ['w'] });
		expect(
			map.toSkillsetRefresh(wire<Wire.SkillsetRefreshResult>({ skillset: { id: 'x' } }))
		).toMatchObject({ skillset: { id: 'x' }, warnings: [] });
	});
});

describe('analytics', () => {
	it('reads the catalog, dropping aggs it does not know and narrowing types', () => {
		const c = map.toCatalog(
			wire<Wire.AnalyticsCatalog>({
				grains: [
					{
						id: 'attempts',
						label: 'Stage attempts',
						time_field: 'started_at',
						dimensions: [{ id: 'model', label: 'Model', type: 'text' }],
						measures: [
							{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: ['sum', 'p99', 'median'] }
						],
						derived: null
					},
					{ id: 'galaxies', label: '?', time_field: 'x', dimensions: null, measures: null }
				]
			})
		);
		expect(c.grains[0]).toEqual({
			id: 'attempts',
			label: 'Stage attempts',
			timeField: 'started_at',
			dimensions: [{ id: 'model', label: 'Model', type: 'text', aggs: [] }],
			measures: [{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: ['sum', 'median'] }],
			derived: []
		});
		expect(c.grains[1]).toMatchObject({ id: 'runs', dimensions: [], measures: [], derived: [] });
		expect(
			map.toCatalogField(wire<Wire.CatalogField>({ id: 'x', label: 'X', type: 'furlongs' })).type
		).toBe('text');
	});

	it('reads a result: null values stay null, null label maps become empty', () => {
		const r = map.toAnalyticsResult(
			wire<Wire.AnalyticsResult>({
				dims: [{ id: 'bucket', label: 'Week', type: 'time' }],
				measures: [{ id: 'cost_usd', label: 'Cost (p90)', type: 'usd' }],
				rows: [
					{ dims: ['2026-10-26'], values: [1.5], n: 7, small: false, score: 80 },
					{ dims: null, values: [null], n: 2, small: true, score: null }
				],
				labels: { project_id: { p1: 'Shop' }, flow_hash: null },
				truncated: true
			})
		);
		expect(r.rows).toEqual([
			{ dims: ['2026-10-26'], values: [1.5], n: 7, small: false, score: 80 },
			{ dims: [], values: [null], n: 2, small: true }
		]);
		expect(r.labels).toEqual({ project_id: { p1: 'Shop' }, flow_hash: {} });
		expect(r.truncated).toBe(true);
		expect(
			map.toAnalyticsResult(
				wire<Wire.AnalyticsResult>({ dims: null, measures: null, rows: null, labels: null })
			)
		).toMatchObject({ dims: [], measures: [], rows: [], labels: {} });
	});

	it('reads a dashboard’s panels, falling back visibly on an unknown viz, width or grain', () => {
		const d = map.toDashboard(
			wire<Wire.Dashboard>({
				id: 'builtin-overview',
				user_id: '',
				name: 'Overview',
				builtin: true,
				filters: ['flow_name'],
				created_at: AT,
				updated_at: AT,
				panels: [
					{
						id: 'p1',
						title: 'Weekly cost',
						viz: 'line',
						width: 6,
						query: {
							grain: 'runs',
							measures: [{ field: 'cost_usd', agg: 'sum' }],
							group_by: ['flow_name'],
							filters: [{ field: 'state', op: 'in', values: ['completed'] }],
							time: { bucket: 'week', from: AT, to: AT },
							order: { by: 'm0' },
							limit: 20,
							score: { scorecard: 'builtin-cheapest', baseline: ['a'] }
						}
					},
					{
						id: 'p2',
						title: 'Odd',
						viz: 'pie',
						width: 5,
						query: {
							grain: 'galaxies',
							measures: null,
							filters: [{ field: 'f', op: 'like', values: null }],
							time: { bucket: 'fortnight' },
							score: { scorecard: 's', baseline: [] }
						}
					}
				]
			})
		);
		expect(d).toMatchObject({ userId: undefined, builtin: true, filters: ['flow_name'] });
		expect(d.panels[0]).toEqual({
			id: 'p1',
			title: 'Weekly cost',
			viz: 'line',
			width: 6,
			query: {
				grain: 'runs',
				measures: [{ field: 'cost_usd', agg: 'sum' }],
				groupBy: ['flow_name'],
				filters: [{ field: 'state', op: 'in', values: ['completed'] }],
				time: { bucket: 'week', from: AT, to: AT },
				order: { by: 'm0', desc: false },
				limit: 20,
				score: { scorecard: 'builtin-cheapest', baseline: ['a'] }
			}
		});
		expect(d.panels[1]).toEqual({
			id: 'p2',
			title: 'Odd',
			viz: 'table',
			width: 12,
			query: {
				grain: 'runs',
				measures: [],
				groupBy: [],
				filters: [{ field: 'f', op: 'eq', values: [] }],
				time: {},
				score: { scorecard: 's' }
			}
		});
		expect(
			map.toDashboard(wire<Wire.Dashboard>({ id: 'd', user_id: 'u', filters: null, panels: null }))
		).toMatchObject({ userId: 'u', filters: [], panels: [] });
	});

	it('reads a scorecard, an unknown direction as lower', () => {
		expect(
			map.toScorecard(
				wire<Wire.Scorecard>({
					id: 's',
					user_id: 'u',
					name: 'Cheap',
					builtin: false,
					created_at: AT,
					updated_at: AT,
					terms: [
						{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'higher', weight: 1 },
						{ measure: { field: 'wall_ms', agg: 'fastest' }, direction: 'sideways', weight: 2 }
					]
				})
			)
		).toEqual({
			id: 's',
			userId: 'u',
			name: 'Cheap',
			builtin: false,
			createdAt: AT,
			updatedAt: AT,
			terms: [
				{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'higher', weight: 1 },
				{ measure: { field: 'wall_ms' }, direction: 'lower', weight: 2 }
			]
		});
	});

	it('reads a drilled run and a dimension value, a missing label as the value', () => {
		expect(
			map.toDrillRun(
				wire<Wire.DrillRun>({
					run_id: 'r',
					title: 'T',
					flow_name: 'f',
					flow_hash: 'h',
					state: 'completed',
					created_at: AT,
					wall_ms: 60000,
					cost_usd: 0.25
				})
			)
		).toEqual({
			runId: 'r',
			title: 'T',
			flowName: 'f',
			flowHash: 'h',
			state: 'completed',
			createdAt: AT,
			wallMs: 60000,
			costUsd: 0.25
		});
		expect(map.toAnalyticsValue({ value: 'v', label: '' })).toEqual({ value: 'v', label: 'v' });
	});
});
