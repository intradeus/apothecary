import { describe, expect, it } from 'vitest';
import type { RunState, ServiceStatus } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { anAnswer, anEvent, aRaise, NOW } from '$test/fixtures/run-views';
import {
	accentSplit,
	CODE_SERVER_PORT,
	currentStage,
	describe as describeEvent,
	envLinks,
	hasLog,
	pingsOf,
	PROVISION_STAGE,
	shownStages,
	stageRows,
	totals,
	vscodeRemoteSettings,
	workingSince,
	type StageDef
} from './derive';

const started = (seq: number, id: string, payload = {}) =>
	anEvent(seq, 'stage_started', { stage_id: id, iteration: 1, ...payload });
const completed = (seq: number, id: string, payload = {}) =>
	anEvent(seq, 'stage_completed', { stage_id: id, duration_ms: 2000, ...payload });
const stage = (id: string, type = 'agent', phase?: string): StageDef => ({ id, type, phase });

describe('totals', () => {
	it('banks what every finished and paused leg spent', () => {
		expect(
			totals([
				completed(1, 'a', { usage: { cost_usd: 0.5 }, turns_used: 3 }),
				anEvent(2, 'stage_paused', { stage_id: 'b', usage: { cost_usd: 0.25 }, turns_used: 2 }),
				completed(3, 'b', { usage: { cost_usd: 0.25 }, turns_used: 1 })
			])
		).toEqual({ cost: 1, turns: 6 });
	});

	it('ignores a leg that says nothing and any other event', () => {
		expect(
			totals([completed(1, 'a'), anEvent(2, 'stage_failed', { usage: { cost_usd: 9 } })])
		).toEqual({ cost: 0, turns: 0 });
	});
});

describe('pingsOf', () => {
	it('lists the raised pings oldest first, each pending until answered', () => {
		const pings = pingsOf([aRaise(1, 'p1'), aRaise(2, 'p2'), anAnswer(3, 'p1')], aRun());
		expect(pings.map((p) => [p.id, p.status])).toEqual([
			['p1', 'answered'],
			['p2', 'pending']
		]);
	});

	it.each<[RunState, string]>([
		['completed', 'withdrawn'],
		['failed', 'withdrawn'],
		['abandoned', 'withdrawn'],
		['archived', 'withdrawn'],
		['running', 'pending'],
		['paused', 'pending'],
		['waiting_on_human', 'pending']
	])('reads an unanswered ping on a %s run as %s', (state, status) => {
		expect(pingsOf([aRaise(1, 'p1')], aRun({ state }))[0].status).toBe(status);
	});

	it('keeps an answer on a finished run', () => {
		const [p] = pingsOf([aRaise(1, 'p1'), anAnswer(2, 'p1')], aRun({ state: 'completed' }));
		expect(p.status).toBe('answered');
	});

	it('reads a ping as pending before the run has loaded', () => {
		expect(pingsOf([aRaise(1, 'p1')], null)[0].status).toBe('pending');
	});

	it('carries what the answer said', () => {
		const answer = anAnswer(2, 'p1', {
			answer: 'reject',
			feedback: 'Too broad',
			content: { env: 'prod' },
			attachment_ids: ['a1'],
			review: [{ path: 'plan.md', start_line: 3, body: 'Why?' }],
			curated: { 'plan.md': { summary: 'Short', items: [{ item: 'i1', keep: false }] } }
		});
		const [p] = pingsOf([aRaise(1, 'p1'), answer], aRun());
		expect(p).toMatchObject({
			answer: 'reject',
			feedback: 'Too broad',
			content: { env: 'prod' },
			attachmentIds: ['a1'],
			answeredAt: answer.ts
		});
		expect(p.review).toEqual([
			expect.objectContaining({ path: 'plan.md', startLine: 3, body: 'Why?' })
		]);
		expect(p.curated?.['plan.md'].items[0]).toMatchObject({ item: 'i1', keep: false });
	});

	it('carries no answer fields on a pending ping', () => {
		const [p] = pingsOf([aRaise(1, 'p1')], aRun());
		expect(p.answer).toBeUndefined();
		expect(p.answeredAt).toBeUndefined();
		expect(p.curated).toBeUndefined();
		expect(p.review).toEqual([]);
	});

	it.each([
		['the link when it has one', { link: 'https://x.test', files: ['a.md'] }, 'https://x.test'],
		['the first file otherwise', { files: ['a.md', 'b.md'] }, 'a.md'],
		['nothing without either', {}, undefined]
	])('takes as primary ref %s', (_, payload, ref) => {
		expect(pingsOf([aRaise(1, 'p1', payload)], aRun())[0].primaryRef).toBe(ref);
	});

	it.each([
		['gate', undefined, 'yes_no'],
		['phase_confirm', undefined, 'yes_no'],
		['debug', undefined, 'yes_no'],
		['clarification', undefined, 'form'],
		['clarification', 'yes_no', 'yes_no']
	])('reads a %s ping declared %s as %s', (type, answer_kind, kind) => {
		expect(pingsOf([aRaise(1, 'p1', { type, answer_kind })], aRun())[0].answerKind).toBe(kind);
	});

	it('reads the raise payload into the ping', () => {
		const raise = aRaise(4, 'p1', {
			type: 'clarification',
			question: 'Which env?',
			context: 'Two exist',
			stage_id: 'build',
			phase: 'review',
			ends_phase: true,
			file_types: { 'a.md': 'review_threads' },
			form: { fields: [{ id: 'env', type: 'string', options: ['dev', 'prod'] }] }
		});
		const [p] = pingsOf([raise], aRun());
		expect(p).toMatchObject({
			question: 'Which env?',
			context: 'Two exist',
			stageId: 'build',
			phase: 'review',
			endsPhase: true,
			raisedAt: raise.ts,
			seq: 4,
			fileTypes: { 'a.md': 'review_threads' }
		});
		expect(p.form?.fields[0].options.map((o) => o.label)).toEqual(['dev', 'prod']);
	});

	it('takes only a real true as ending the phase, and no empty phase', () => {
		const [p] = pingsOf([aRaise(1, 'p1', { ends_phase: 'yes', phase: '' })], aRun());
		expect(p.endsPhase).toBe(false);
		expect(p.phase).toBeUndefined();
	});
});

describe('currentStage', () => {
	it('is nothing before a stage starts', () => {
		expect(currentStage([anEvent(1, 'run_created')])).toBeNull();
	});

	it('is the latest start while nothing ended it', () => {
		const start = started(2, 'b');
		expect(currentStage([started(1, 'a'), completed(1, 'a'), start])).toBe(start);
	});

	it.each(['stage_completed', 'stage_failed', 'stage_paused'])('is nothing after %s', (type) => {
		expect(currentStage([started(1, 'a'), anEvent(2, type, { stage_id: 'a' })])).toBeNull();
	});

	it('is back in flight after a resume', () => {
		const start = started(1, 'a');
		const events = [
			start,
			anEvent(2, 'stage_paused', { stage_id: 'a' }),
			anEvent(3, 'stage_resumed', { stage_id: 'a' })
		];
		expect(currentStage(events)).toBe(start);
	});

	it('stays parked when the resume is the older word', () => {
		const events = [
			started(1, 'a'),
			anEvent(2, 'stage_resumed', { stage_id: 'a' }),
			anEvent(3, 'stage_paused', { stage_id: 'a' })
		];
		expect(currentStage(events)).toBeNull();
	});

	it('ignores another stage ending', () => {
		const start = started(2, 'b');
		expect(currentStage([started(1, 'a'), start, completed(3, 'a')])).toBe(start);
	});
});

describe('workingSince', () => {
	it('is nothing without a stage in flight', () => {
		expect(workingSince([], null)).toBeNull();
	});

	it('counts from the start', () => {
		const start = started(1, 'a');
		expect(workingSince([start], start)).toBe(start.ts);
	});

	it('counts from the latest resume: the human thinking is not the agent working', () => {
		const start = started(1, 'a');
		const resumed = anEvent(5, 'stage_resumed', { stage_id: 'a' });
		const events = [start, anEvent(3, 'stage_resumed', { stage_id: 'a' }), resumed];
		expect(workingSince(events, start)).toBe(resumed.ts);
	});

	it("ignores another stage's resume, and a resume before this start", () => {
		const start = started(4, 'a');
		const events = [
			anEvent(2, 'stage_resumed', { stage_id: 'a' }),
			start,
			anEvent(5, 'stage_resumed', { stage_id: 'b' })
		];
		expect(workingSince(events, start)).toBe(start.ts);
	});
});

describe('shownStages', () => {
	const defs = [
		stage('a', 'agent', 'main'),
		stage('w1', 'agent', 'watch'),
		stage('o1', 'agent', 'outcome')
	];
	const phase = (seq: number, id: string) => anEvent(seq, 'phase_started', { phase: id });

	it('shows a flow without phases whole', () => {
		const flat = [stage('a'), stage('b')];
		expect(shownStages(flat, [])).toBe(flat);
	});

	it('starts with the main chain alone', () => {
		expect(shownStages(defs, []).map((d) => d.id)).toEqual(['a']);
	});

	it('adds other chains in the order they first started', () => {
		const events = [phase(1, 'outcome'), phase(2, 'watch'), phase(3, 'outcome')];
		expect(shownStages(defs, events).map((d) => d.id)).toEqual(['a', 'o1', 'w1']);
	});

	it('shows the first chain of a flow that only watches', () => {
		const watching = [stage('w1', 'agent', 'watch'), stage('w2', 'agent', 'other')];
		expect(shownStages(watching, []).map((d) => d.id)).toEqual(['w1']);
	});

	it('skips a phase the document does not have', () => {
		expect(shownStages(defs, [phase(1, 'gone')]).map((d) => d.id)).toEqual(['a']);
	});
});

describe('stageRows', () => {
	const row = (events: Parameters<typeof stageRows>[1], state: RunState = 'running', now = NOW) =>
		stageRows([stage('a'), stage('b')], events, aRun({ state }), now)[0];

	it('shows a stage with no start as pending, with its type for a line', () => {
		expect(stageRows([stage('a', 'command')], [], aRun(), NOW)[0]).toMatchObject({
			status: 'pending',
			line: 'command',
			attempt: 0,
			looped: false
		});
	});

	it('calls a pending gate a gate', () => {
		expect(stageRows([stage('g', 'human_gate')], [], aRun(), NOW)[0].line).toBe('gate');
	});

	it('appends a stage the log has and the recipe does not', () => {
		const rows = stageRows([stage('a')], [started(1, 'old', { agent: 'dev' })], aRun(), NOW);
		expect(rows.map((r) => [r.id, r.type, r.agent])).toEqual([
			['a', 'agent', undefined],
			['old', 'agent', 'dev']
		]);
	});

	it('leaves the type of an appended non-agent stage empty', () => {
		const rows = stageRows([], [started(1, 'old')], aRun(), NOW);
		expect(rows[0].type).toBe('');
	});

	it('shows a running stage with its attempt and clock', () => {
		const r = row([started(1, 'a', { iteration: 2 })], 'running', NOW + 61_000);
		expect(r).toMatchObject({ status: 'running', attempt: 2, line: 'attempt 2 · 1:00' });
	});

	it('counts the clock from the latest resume', () => {
		const events = [
			started(1, 'a'),
			anEvent(2, 'stage_paused', { stage_id: 'a' }),
			anEvent(10, 'stage_resumed', { stage_id: 'a' })
		];
		expect(row(events, 'running', NOW + 15_000).line).toBe('attempt 1 · 0:05');
	});

	it.each([
		['with cost and time', { usage: { cost_usd: 0.5 }, duration_ms: 3000 }, 'done · $0.500 · 3s'],
		['with nothing to say', { duration_ms: 0 }, 'done'],
		['skipped', { skipped: true, duration_ms: 0 }, 'skipped — nothing to act on']
	])('shows a completed stage %s', (_, payload, line) => {
		expect(row([started(1, 'a'), completed(2, 'a', payload)])).toMatchObject({
			status: 'done',
			line
		});
	});

	it('shows a failure with its reason in words', () => {
		const events = [
			started(1, 'a'),
			anEvent(2, 'stage_failed', { stage_id: 'a', reason: 'budget_exceeded' })
		];
		expect(row(events, 'failed')).toMatchObject({ status: 'failed', line: 'budget exceeded' });
	});

	it('says failed when the failure names no reason', () => {
		const events = [started(1, 'a'), anEvent(2, 'stage_failed', { stage_id: 'a' })];
		expect(row(events, 'failed').line).toBe('failed');
	});

	describe('a failure parked on the debug leaf', () => {
		const parked = [
			started(1, 'a'),
			anEvent(2, 'stage_failed', { stage_id: 'a', reason: 'exit' }),
			aRaise(3, 'd1', { type: 'debug', stage_id: 'a' })
		];

		it('waits on a person while the debug ping is open', () => {
			expect(row(parked, 'waiting_on_human')).toMatchObject({
				status: 'waiting',
				line: 'failed — parked for you to check why'
			});
		});

		it('is a plain failure once answered', () => {
			const r = row([...parked, anAnswer(4, 'd1', { answer: 'reject' })], 'running');
			expect(r.status).toBe('failed');
		});

		it('is a plain failure once the run is over', () => {
			expect(row(parked, 'abandoned').status).toBe('failed');
		});
	});

	it('waits after a pause', () => {
		const events = [started(1, 'a'), anEvent(2, 'stage_paused', { stage_id: 'a' })];
		expect(row(events)).toMatchObject({ status: 'waiting', line: 'waiting on you' });
	});

	it('waits with the run, even with no pause on the log', () => {
		expect(row([started(1, 'a')], 'waiting_on_human').status).toBe('waiting');
	});

	it('shows a paused run re-entering the stage', () => {
		expect(row([started(1, 'a')], 'paused')).toMatchObject({
			status: 'paused',
			line: 'paused — will re-enter'
		});
	});

	it.each<[RunState, string, string]>([
		['failed', 'failed', 'failed'],
		['abandoned', 'paused', 'stopped'],
		['completed', 'paused', 'stopped']
	])('closes a stage left in flight on a %s run', (state, status, line) => {
		expect(row([started(1, 'a')], state)).toMatchObject({ status, line });
	});

	it('does not read a pause as waiting once the run is over', () => {
		const events = [started(1, 'a'), anEvent(2, 'stage_paused', { stage_id: 'a' })];
		expect(row(events, 'abandoned').status).toBe('paused');
	});

	it('shows a stage started again as running, not done', () => {
		const events = [started(1, 'a'), completed(2, 'a'), started(3, 'a', { iteration: 2 })];
		expect(row(events)).toMatchObject({ status: 'running', attempt: 2 });
	});

	describe('a loop-back', () => {
		const defs = [stage('plan'), stage('build'), stage('review')];
		const edge = (seq: number, from: string, to: string) =>
			anEvent(seq, 'edge_taken', { from, to, edge: 'rejected', count: 1, max: 3 });
		const first = [
			started(1, 'plan'),
			completed(2, 'plan'),
			started(3, 'build'),
			completed(4, 'build'),
			started(5, 'review'),
			completed(6, 'review')
		];
		const rows = (events: Parameters<typeof stageRows>[1]) => stageRows(defs, events, aRun(), NOW);

		it('re-opens everything between its target and its source', () => {
			expect(rows([...first, edge(7, 'review', 'build')]).map((r) => r.status)).toEqual([
				'done',
				'pending',
				'pending'
			]);
		});

		it('says those stages run again', () => {
			const [, build] = rows([...first, edge(7, 'review', 'build')]);
			expect(build).toMatchObject({ line: '↩ runs again', looped: true });
		});

		it('keeps the target running once it restarts, marked as looped', () => {
			const events = [...first, edge(7, 'review', 'build'), started(8, 'build', { iteration: 2 })];
			const [, build, review] = rows(events);
			expect(build).toMatchObject({ status: 'running', attempt: 2, looped: true });
			expect(review.status).toBe('pending');
		});

		it('ignores a forward edge', () => {
			expect(rows([...first, edge(7, 'plan', 'review')]).map((r) => r.status)).toEqual([
				'done',
				'done',
				'done'
			]);
		});

		it('does not call a stage looped for being reached by a forward edge', () => {
			const events = [started(1, 'plan'), edge(2, 'plan', 'review'), started(3, 'review')];
			expect(rows(events)[2]).toMatchObject({ status: 'running', looped: false });
		});

		it('ignores an edge naming a stage the recipe does not have', () => {
			expect(rows([...first, edge(7, 'review', 'gone')]).map((r) => r.status)).toEqual([
				'done',
				'done',
				'done'
			]);
		});

		it('leaves alone a stage in the span that never ran', () => {
			const events = [started(1, 'plan'), completed(2, 'plan'), edge(3, 'review', 'plan')];
			expect(rows(events)[1]).toMatchObject({ status: 'pending', line: 'agent', looped: false });
		});
	});
});

describe('describe', () => {
	type Row = [string, string, Record<string, unknown>, string, string];
	const rows: Row[] = [
		[
			'run_created',
			'',
			{ ticket_title: 'T', flow_name: 'f' },
			'run created — “T” (flow f)',
			'info'
		],
		['image_built', 'cached', { cached: true }, 'image built (from cache)', ''],
		['image_built', 'fresh', {}, 'image built', ''],
		['env_rebuild_started', '', {}, 'workspace rebuild started', 'info'],
		['env_rebuild_completed', '', {}, 'workspace rebuilt', 'ok'],
		['env_rebuild_failed', 'with error', { error: 'x' }, 'workspace rebuild failed: x', 'bad'],
		['env_rebuild_failed', 'without', {}, 'workspace rebuild failed', 'bad'],
		['action_started', '', { stage_id: 's', action: 'commit' }, 's: action commit started', 'info'],
		[
			'edge_taken',
			'bounded',
			{ edge: 'rejected', from: 'r', to: 'b', count: 1, max: 3 },
			'↩ rejected r → b (1/3)',
			'warn'
		],
		[
			'edge_taken',
			'unbounded, deep',
			{ edge: 'e', from: 'r', to: 'b', count: 2, max: -1, depth: 2 },
			'↩ e (then #2) r → b (2/∞)',
			'warn'
		],
		[
			'ping_raised',
			'debug',
			{ type: 'debug', stage_id: 's' },
			's failed — parked for debugging',
			'warn'
		],
		['ping_raised', 'question', { type: 'gate', question: 'Q?' }, 'ping [gate]: Q?', 'warn'],
		['ping_answered', 'plain', { answer: 'approve' }, 'answered: approve', 'ok'],
		[
			'ping_answered',
			'feedback',
			{ answer: 'reject', feedback: 'no' },
			'answered: reject — no',
			'ok'
		],
		[
			'ping_answered',
			'a form',
			{ answer: 'accept', content: { env: 'dev', tags: ['a', 'b'], none: [] } },
			'answered: env: dev · tags: a, b · none: —',
			'ok'
		],
		['ping_answered', 'accept, no content', { answer: 'accept' }, 'answered: accept', 'ok'],
		['verdict_received', 'approve', { approve: true }, 'verdict: approve', 'ok'],
		[
			'verdict_received',
			'one comment',
			{ approve: false, n_comments: 1 },
			'verdict: changes requested (1 comment)',
			'warn'
		],
		[
			'verdict_received',
			'comments',
			{ approve: false, n_comments: 3 },
			'verdict: changes requested (3 comments)',
			'warn'
		],
		['phase_started', 'plain', { phase: 'p' }, 'p started', 'info'],
		[
			'phase_started',
			'on the mr',
			{ phase: 'p', subject: 'repo', by: 'human' },
			'p on the merge request (started by hand)',
			'info'
		],
		[
			'phase_started',
			'on a subject',
			{ phase: 'p', subject: 'api', by: 'signal' },
			'p on api (triggered)',
			'info'
		],
		['phase_completed', 'done', { phase: 'p' }, 'p finished', 'ok'],
		[
			'phase_completed',
			'ended',
			{ phase: 'p', reason: 'dead end' },
			'p ended early: dead end',
			'warn'
		],
		['phase_declined', '', { phase: 'p' }, 'p declined', 'warn'],
		['review_comments_loaded', 'pending', { threads: [1] }, 'loaded 1 review thread', 'info'],
		[
			'review_comments_loaded',
			'all',
			{ scope: 'all', threads: [1, 2] },
			'read 2 threads on the merge request before writing',
			'info'
		],
		['review_comments_loaded', 'garbled', { threads: 'x' }, 'loaded 0 review threads', 'info'],
		['review_replies_posted', 'plain', { thread_ids: [1, 2] }, 'replied to 2 review threads', 'ok'],
		[
			'review_replies_posted',
			'declined',
			{ thread_ids: [1], declined: [2] },
			'replied to 1 review thread · 1 declined',
			'ok'
		],
		[
			'review_posted',
			'already',
			{ already: true, inline: 3 },
			'the review was already posted',
			'ok'
		],
		['review_posted', 'nothing', {}, 'posted a review summary, no new note', 'ok'],
		[
			'review_posted',
			'everything',
			{ inline: 2, folded: 1, replied: [1], resolved: [1, 2] },
			'posted a review with 3 notes · 1 outside the change, in its summary · continued 1 thread of its own · resolved 2',
			'ok'
		],
		[
			'review_curated',
			'notes',
			{
				type: 'review_threads',
				approved: true,
				review: { notes: [1], replies: [2] },
				dropped: [3],
				rewritten: [1]
			},
			'you kept 2 of 3 review notes · 1 dropped · 1 reworded',
			'info'
		],
		[
			'review_curated',
			'replies sent back',
			{ type: 'review_replies', replies: [1], dropped_replies: [2], approved: false },
			'you kept 1 of 2 replies · 1 dropped · and sent the draft back',
			'warn'
		],
		[
			'mr_merged',
			'',
			{ method: 'squash', head: '0123456789abcdef' },
			'merged the merge request (squash) at 01234567',
			'ok'
		],
		[
			'branch_pushed',
			'main checkout',
			{ branch: 'feat/x', repo: 'repo', tip: '0123456789' },
			'pushed feat/x at 01234567',
			'ok'
		],
		[
			'branch_pushed',
			'other checkout',
			{ branch: 'feat/x', repo: 'api', tip: '0123456789' },
			'pushed feat/x (api) at 01234567',
			'ok'
		],
		[
			'target_synced',
			'',
			{ target_tip: '0123456789abc', strategy: 'rebase' },
			'took 0123456789 in (rebase)',
			'ok'
		],
		['mr_created', 'plain', { mr_url: 'u' }, 'merge request created: u', 'ok'],
		[
			'mr_created',
			'with target',
			{ mr_url: 'u', branch: 'b', target: 'main' },
			'merge request created: u (b → main)',
			'ok'
		],
		[
			'run_completed',
			'',
			{ total_cost_usd: 1.5, total_turns: 9, unwanted_pings: 0 },
			'run completed — $1.500 · 9 turns · 0 unwanted pings',
			'ok'
		],
		['run_abandoned', 'at a stage', { stalled_at: 's', reason: 'r' }, 'abandoned at s: r', 'bad'],
		['run_abandoned', 'by an outcome', { reason: 'r' }, 'abandoned: r', 'bad'],
		['run_failed', '', { reason: 'r' }, 'run failed: r', 'bad'],
		['run_retried', '', {}, 'retry — re-entering the failed stage in the same workspace', 'info'],
		[
			'run_resumed',
			'old spelling',
			{},
			'retry — re-entering the failed stage in the same workspace',
			'info'
		],
		[
			'run_paused',
			'by the engine',
			{ reason: 'waiting_on_human', stage_id: 's' },
			'parked at s — waiting for you (the run released its slot)',
			''
		],
		['run_paused', 'at a stage', { stage_id: 's' }, 'paused at s', ''],
		['run_paused', 'between stages', {}, 'paused', ''],
		['run_unpaused', 'hold', { reason: 'hold' }, 'picked back up — your word landed', 'info'],
		[
			'run_unpaused',
			'outcome',
			{ reason: 'outcome' },
			'the merge request decided: the outcome took over',
			'info'
		],
		[
			'run_unpaused',
			'answered',
			{ reason: 'answered' },
			'picked back up — your answer landed',
			'info'
		],
		['run_unpaused', 'by hand', {}, 'play — continuing where it left off', 'info'],
		['run_archived', '', {}, 'archived', ''],
		['env_destroyed', '', {}, 'workspace destroyed', ''],
		[
			'service_started',
			'by a stage',
			{ service: 'db', by: 'stage', stage_id: 's' },
			'service db started by stage s',
			'info'
		],
		[
			'service_started',
			'by hand',
			{ service: 'db', by: 'human' },
			'service db started by hand',
			'info'
		],
		[
			'service_started',
			'again',
			{ service: 'db', restart: true },
			'service db restarted by hand',
			'info'
		],
		['service_ready', '', { service: 'db' }, 'service db is up', 'ok'],
		['service_stopped', 'plain', { service: 'db' }, 'service db stopped', ''],
		['service_stopped', 'why', { service: 'db', reason: 'r' }, 'service db stopped — r', ''],
		['service_failed', '', { service: 'db', reason: 'r' }, 'service db did not come up: r', 'bad'],
		['something_new', 'unknown type', {}, 'something new', '']
	];

	it.each(rows)('words %s %s', (type, _case, payload, text, tone) => {
		expect(describeEvent(anEvent(1, type, payload))).toMatchObject({ text, tone });
	});

	it.each([
		['env_requested', {}, 'provisioning workspace…', ''],
		['env_ready', { env_id: 'e1' }, 'workspace ready (e1)', 'ok'],
		['env_failed', { error: 'x' }, 'environment failed: x', 'bad']
	])('points %s at the provisioning log', (type, payload, text, tone) => {
		expect(describeEvent(anEvent(1, type, payload))).toEqual({
			text,
			tone,
			stage: PROVISION_STAGE
		});
	});

	const staged: [string, string, Record<string, unknown>, string, string][] = [
		[
			'stage_started',
			'with a model',
			{ iteration: 2, model: 'fake/m' },
			's started (attempt 2) · fake/m',
			'info'
		],
		['stage_started', 'without', { iteration: 1 }, 's started (attempt 1)', 'info'],
		[
			'stage_completed',
			'with everything',
			{ turns_used: 2, usage: { cost_usd: 0.1 }, duration_ms: 4400 },
			's completed (2 turns · $0.100 · 4s)',
			'ok'
		],
		[
			'stage_completed',
			'skipped',
			{ skipped: true, note: 'nothing' },
			's skipped — nothing',
			'info'
		],
		[
			'stage_paused',
			'on a question',
			{ reason: 'ask_human' },
			's paused — waiting on its own question',
			'warn'
		],
		[
			'stage_paused',
			'held',
			{ reason: 'held' },
			's paused — in your hands, waiting for your word',
			'warn'
		],
		['stage_paused', 'for a reason', { reason: 'timeout' }, 's paused — timeout', 'warn'],
		[
			'stage_paused',
			'having spent',
			{ reason: 'timeout', turns_used: 1, usage: { cost_usd: 0.2 } },
			's paused — timeout (1 turn · $0.200 so far)',
			'warn'
		],
		[
			'stage_resumed',
			'restart',
			{ reason: 'engine_restart' },
			's resumed — engine restarted, picking the session back up',
			'info'
		],
		['stage_resumed', 'message', { reason: 'message' }, 's resumed — your message landed', 'info'],
		['stage_resumed', 'released', { reason: 'released' }, 's resumed — handed back', 'info'],
		['stage_resumed', 'answer', {}, 's resumed — the answer landed', 'info'],
		['hold_taken', '', {}, 's: you took over the agent', 'warn'],
		[
			'hold_released',
			'by hand',
			{ duration_ms: 3000, turns: 2, messages: 1, usage: { cost_usd: 0.1 } },
			's: handed back (3s · 2 turns · 1 message · $0.100)',
			'info'
		],
		[
			'hold_released',
			'with the stage',
			{ reason: 'stage_ended' },
			's: take-over ended with the attempt (0s)',
			'info'
		],
		['stage_failed', 'plain', { reason: 'timeout' }, 's failed: timeout', 'bad'],
		[
			'stage_failed',
			'with error',
			{ reason: 'exit', error: 'boom' },
			's failed: exit — boom',
			'bad'
		]
	];

	it.each(staged)('words %s %s and names its stage', (type, _case, payload, text, tone) => {
		expect(describeEvent(anEvent(1, type, { stage_id: 's', ...payload }))).toEqual({
			text,
			tone,
			stage: 's'
		});
	});

	it('cuts a long stage error to two hundred characters', () => {
		const { text } = describeEvent(
			anEvent(1, 'stage_failed', { stage_id: 's', reason: 'r', error: 'x'.repeat(500) })
		);
		expect(text).toBe(`s failed: r — ${'x'.repeat(200)}`);
	});

	it.each([
		[{ kind: 'log', stage_id: 's', iteration: 2 }, 's (attempt 2) captured log'],
		[
			{ kind: 'output', stage_id: 's', iteration: 1, name: 'plan', path: 'plan.md' },
			's (attempt 1) captured output plan — plan.md'
		],
		[{ kind: 'output', stage_id: 's' }, 's (attempt 0) captured output'],
		[{ kind: 'ping_file', path: 'a.md' }, 'captured a.md for a ping (as shown to you)']
	])('words a capture and links its snapshot', (payload, text) => {
		const d = describeEvent(anEvent(1, 'snapshot_captured', { ...payload, snapshot_id: 'snap' }));
		expect(d).toEqual({ text, tone: '', snapshot: 'snap' });
	});
});

describe('hasLog', () => {
	const noLog = [anEvent(1, 'env_requested')];

	it.each([
		['a row with no stage', anEvent(1, 'stage_completed'), undefined, false],
		['a finished attempt', anEvent(1, 'stage_completed'), 's', true],
		['a skipped stage', anEvent(1, 'stage_completed', { skipped: true }), 's', false],
		['a failed attempt', anEvent(1, 'stage_failed'), 's', true],
		['a paused attempt', anEvent(1, 'stage_paused'), 's', true],
		['a ready workspace', anEvent(1, 'env_ready'), PROVISION_STAGE, true],
		['a failed workspace', anEvent(1, 'env_failed'), PROVISION_STAGE, true],
		['anything else', anEvent(1, 'stage_started'), 's', false]
	])('decides for %s', (_, e, st, want) => {
		expect(hasLog(e, st, [], false)).toBe(want);
	});

	it('hangs the provisioning log on the request only for a run that died building', () => {
		expect(hasLog(noLog[0], PROVISION_STAGE, noLog, true)).toBe(true);
		expect(hasLog(noLog[0], PROVISION_STAGE, noLog, false)).toBe(false);
	});

	it.each(['env_ready', 'env_failed'])('leaves the request plain once %s has it', (type) => {
		const events = [...noLog, anEvent(2, type)];
		expect(hasLog(events[0], PROVISION_STAGE, events, true)).toBe(false);
	});
});

describe('envLinks', () => {
	const ready = anEvent(1, 'env_ready', {
		links: [
			{ kind: 'app :3000', url: 'http://a' },
			{ kind: 'dead', url: '' }
		]
	});
	const service = (over: Partial<ServiceStatus>): ServiceStatus =>
		({ name: 'web', state: 'healthy', ports: [], ...over }) as ServiceStatus;

	it("uses env_ready's links until the live registry has any, skipping blank ones", () => {
		expect(envLinks([ready], [])).toEqual([{ kind: 'app :3000', url: 'http://a' }]);
	});

	it('prefers the live registry over the copies in env_ready', () => {
		expect(envLinks([ready], [{ port: 4000, url: 'http://b' }])).toEqual([
			{ kind: 'app :4000', url: 'http://b' }
		]);
	});

	it('uses the latest env_ready', () => {
		const rebuilt = anEvent(2, 'env_ready', { links: [{ kind: 'new', url: 'http://n' }] });
		expect(envLinks([ready, rebuilt], [])).toEqual([{ kind: 'new', url: 'http://n' }]);
	});

	it('never lists the code-server port as an app', () => {
		expect(envLinks([], [{ port: CODE_SERVER_PORT, url: 'http://c' }])).toEqual([]);
	});

	it("names a service's preview for the service, label included", () => {
		const svc = service({
			ports: [
				{ port: 80, label: 'site', url: 'http://s' },
				{ port: 81, url: 'http://t' },
				{ port: 82 }
			]
		} as Partial<ServiceStatus>);
		expect(envLinks([], [], [svc])).toEqual([
			{ kind: 'web · site :80', url: 'http://s' },
			{ kind: 'web :81', url: 'http://t' }
		]);
	});

	it("lets the service's name win over the same url on the exposed list", () => {
		const svc = service({ ports: [{ port: 80, url: 'http://s' }] } as Partial<ServiceStatus>);
		expect(envLinks([], [{ port: 80, url: 'http://s' }], [svc])).toEqual([
			{ kind: 'web :80', url: 'http://s' }
		]);
	});

	it('is empty for a run with nothing up', () => {
		expect(envLinks([], [])).toEqual([]);
	});
});

describe('vscodeRemoteSettings', () => {
	it('writes the two settings.json lines with the extension list', () => {
		expect(vscodeRemoteSettings(['a.b', 'c.d'])).toBe(
			'"remote.defaultExtensionsIfInstalledLocally": ["a.b","c.d"],\n' +
				'"remote.downloadExtensionsLocally": true'
		);
	});

	it('writes an empty list when the workspace names none', () => {
		expect(vscodeRemoteSettings()).toContain('"remote.defaultExtensionsIfInstalledLocally": [],');
	});
});

describe('accentSplit', () => {
	it.each([
		['', '', ''],
		['One', 'One', ''],
		['Two words', 'Two', 'words'],
		['Three small words', 'Three', 'small words'],
		['A title of six plain', 'A title of', 'six plain'],
		['one two three four five six', 'one two three four', 'five six'],
		['one two three four five six seven', 'one two three four', 'five six seven'],
		['  spaced   out  words ', 'spaced', 'out words']
	])('splits %j', (title, plain, accent) => {
		expect(accentSplit(title)).toEqual([plain, accent]);
	});
});
