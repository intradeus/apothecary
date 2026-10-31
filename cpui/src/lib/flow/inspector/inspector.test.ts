import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { SignalSpec } from '$lib/domain';
import { ABORT, DONE, END, type DocPhase, type FlowDoc } from '$lib/domain/flowdoc';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { aFlow, aStage, readDoc } from '$test/fixtures/logic';
import {
	addKinds,
	backLabelOf,
	confirmDeleteMCP,
	confirmDeletePhase,
	confirmDeleteSchema,
	confirmDeleteSequence,
	confirmDeleteStage,
	dropStage,
	kindHue,
	observedSignals,
	renamed,
	runIfOptions,
	runners,
	schemaNamesOf,
	setOperatesOn,
	viewKeyOf,
	writtenPathsOf,
	writtenTypesOf
} from './inspector';

vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const asked = () => vi.mocked(confirmDialog).mock.calls.at(-1)![0];

const flow = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters:
  gate: { type: bool, default: true }
  label: { type: string, default: x }
schemas: [{ name: report, json_schema: {} }, { name: free, json_schema: {} }]
mcp_servers: [{ name: browser, builtin: browser }, { name: docs, url: u }]
agents:
  - { name: a, prompt: p, mcp: [browser], outputs: [{ name: o, schema: report }] }
  - { name: b, prompt: p, mcp: [browser] }
sequences:
  - id: q
    stages:
      - { id: s1, type: command, run: r, output: { o: { to: seq.md, type: file } } }
      - { id: s2, type: command, run: r, on_fail: { to: s1, max: 1 } }
  - id: unused
    stages:
      - { id: u, type: command, run: r }
main:
  - { id: one, type: command, run: r, output: { o: { to: z.md, type: verdict }, p: { to: a.md }, n: {} }, on_fail: { to: two, max: 2, then: { to: three, max: 1 } } }
  - { id: two, type: command, run: r, on_success: three }
  - { id: three, sequence: q }
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: r }] }
outcomes:
  - { id: o, on: mr_merged }
`);

describe('small questions', () => {
	it.each([
		['merge_request', 'merge_request'],
		['ticket', undefined],
		['', undefined]
	])('takes %j as what runs operate on', (v, out) => {
		const doc = flow();
		setOperatesOn(doc, v);
		expect(doc.operates_on).toBe(out);
	});

	it.each<[Partial<DocPhase>, string]>([
		[{ kind: 'main' }, 'brand'],
		[{ kind: 'watcher' }, 'accent'],
		[{ kind: 'outcome' }, 'success'],
		[{ kind: 'outcome', then: ABORT }, 'danger']
	])('colours %j %s', (ph, hue) => {
		expect(kindHue(ph as DocPhase)).toBe(hue);
	});

	it('keeps to the signals observed on merge requests', () => {
		const signals = [
			{ slug: 'a', kind: 'observed' },
			{ slug: 'b', kind: 'hand' }
		] as SignalSpec[];
		expect(observedSignals(signals).map((s) => s.slug)).toEqual(['a']);
	});

	it('resets a refused rename’s field to the name in force', () => {
		const el = document.createElement('input');
		el.value = 'typed';
		renamed(el, 'current', true);
		expect(el.value).toBe('typed');
		renamed(el, 'current', false);
		expect(el.value).toBe('current');
	});

	it('lists the schemas’ names', () => {
		expect(schemaNamesOf(flow())).toEqual(['report', 'free']);
		expect(schemaNamesOf(aFlow([]))).toEqual([]);
	});
});

describe('runIfOptions', () => {
	const values = (doc: FlowDoc, current?: string) => runIfOptions(doc, current).map((o) => o.value);

	it('starts with always, labelled', () => {
		expect(runIfOptions(flow())[0]).toEqual({ value: '', label: 'always' });
	});

	it('offers the run’s facts, each way', () => {
		const opts = runIfOptions(flow());
		expect(opts).toContainEqual({
			value: 'run.ticket.tracker',
			label: 'the ticket has a tracker issue'
		});
		expect(opts).toContainEqual({
			value: 'not run.subject',
			label: 'the chain acts on no merge request'
		});
		expect(opts).toContainEqual({
			value: 'run.ticket.change_type == fix',
			label: 'the ticket is a fix'
		});
		expect(opts).toContainEqual({
			value: 'run.ticket.change_type != chore',
			label: 'the ticket is not a chore'
		});
	});

	it('offers the boolean parameters, each way, and no other', () => {
		const v = values(flow());
		expect(v).toContain('$gate');
		expect(v).toContain('not $gate');
		expect(v).not.toContain('$label');
	});

	it.each(['$gate', 'not $gate', 'run.subject', 'run.ticket.change_type == fix'])(
		'does not repeat what it offers: %s',
		(current) => {
			const v = values(flow(), current);
			expect(v.filter((x) => x === current)).toHaveLength(1);
		}
	);

	it('keeps what a stage says already, when it is none of those', () => {
		const opts = runIfOptions(flow(), '$gone');
		expect(opts.at(-1)).toEqual({ value: '$gone', label: '$gone' });
	});
});

describe('what a stage may name', () => {
	it('finds the stages that match, in chains and sequences, each as what opens it', () => {
		expect(runners(flow(), (s) => s.id === 'one' || s.id === 's1')).toEqual([
			{ label: 'one', sel: { kind: 'stage', id: 'one' } },
			{ label: 'q › s1', sel: { kind: 'stage', id: 's1', sequence: 'q' } }
		]);
	});

	it('lists every path written, sorted, once', () => {
		const doc = flow();
		doc.stages[1].output = { again: { to: 'a.md' } };
		expect(writtenPathsOf(doc)).toEqual(['a.md', 'seq.md', 'z.md']);
	});

	it('types the paths that say what they hold', () => {
		expect(writtenTypesOf(flow())).toEqual({ 'seq.md': 'file', 'z.md': 'verdict' });
	});

	it.each<[string, { phase?: string; sequence?: string }, FlowDoc, boolean]>([
		['the main chain, with sequences', { phase: 'main' }, flow(), true],
		['a watcher, with the main chain to run', { phase: 'w' }, aFlow([aStage({ id: 'a' })]), true],
		['the main chain, with nothing to run', { phase: 'main' }, aFlow([aStage({ id: 'a' })]), false],
		['a sequence, with another', { sequence: 'q' }, flow(), true],
		[
			'the only sequence, of a flow with no main chain',
			{ sequence: 'q' },
			{ name: 'f', parameters: {}, stages: [], sequences: [{ id: 'q', stages: [] }] },
			false
		]
	])('offers a sequence stage in %s, or not', (_, where, doc, offered) => {
		const kinds = addKinds(doc, where);
		expect(kinds.slice(0, 6)).toEqual([
			'agent',
			'command',
			'human_gate',
			'action',
			'wait',
			'service'
		]);
		expect(kinds.includes('sequence')).toBe(offered);
	});
});

describe('viewKeyOf', () => {
	it.each<[Parameters<typeof viewKeyOf>[0], string]>([
		[null, 'flow'],
		[{ kind: 'stage', id: 'a' }, 's::a'],
		[{ kind: 'stage', id: 'a', sequence: 'q' }, 's:q:a'],
		[{ kind: 'edge', stage: 'a', edge: 'on_fail' }, 's::a'],
		[{ kind: 'edge', stage: 'a', edge: 'on_fail', sequence: 'q' }, 's:q:a'],
		[{ kind: 'phase', id: 'p' }, 'p:p'],
		[{ kind: 'sequence', id: 'q' }, 'q:q'],
		[{ kind: 'schema', name: 's' }, 'j:s'],
		[{ kind: 'mcp', name: 'm' }, 'm:m'],
		[{ kind: 'agent', name: 'a' }, 'flow']
	])('keys %j', (sel, key) => {
		expect(viewKeyOf(sel)).toBe(key);
	});
});

describe('backLabelOf', () => {
	it.each<[Parameters<typeof backLabelOf>[1], string]>([
		[undefined, ''],
		[null, 'The formula'],
		[{ kind: 'stage', id: 'a' }, 'stage a'],
		[{ kind: 'stage', id: 'a', sequence: 'q' }, 'stage q › a'],
		[{ kind: 'edge', stage: 'a', edge: 'on_fail' }, 'stage a'],
		[{ kind: 'edge', stage: 'a', edge: 'on_fail', sequence: 'q' }, 'stage q › a'],
		[{ kind: 'phase', id: 'main' }, 'the main chain'],
		[{ kind: 'phase', id: 'w' }, 'watcher w'],
		[{ kind: 'phase', id: 'o' }, 'outcome o'],
		[{ kind: 'agent', name: 'a' }, 'agent a'],
		[{ kind: 'sequence', id: 'q' }, 'sequence q'],
		[{ kind: 'schema', name: 's' }, 'schema s'],
		[{ kind: 'mcp', name: 'm' }, 'MCP server m']
	])('names the way back to %j', (sel, label) => {
		expect(backLabelOf(flow(), sel)).toBe(label);
	});
});

describe('dropStage', () => {
	it('takes a stage out of its chain, and the routes into it end', () => {
		const doc = flow();
		dropStage(doc, undefined, 'three');
		expect(doc.stages.map((s) => s.id)).toEqual(['one', 'two', 'x']);
		expect(doc.stages[1].on_success).toBe(END);
	});

	it('splices failure chains past it', () => {
		const doc = flow();
		dropStage(doc, undefined, 'two');
		expect(doc.stages[0].on_fail).toEqual({ to: 'three', max: 1 });
		expect(doc.stages[0].on_success).toBe(END);
	});

	it('drops a chain that led only there', () => {
		const doc = flow();
		doc.stages[0].on_fail = { to: 'two', max: 1 };
		dropStage(doc, undefined, 'two');
		expect(doc.stages[0].on_fail).toBeUndefined();
	});

	it('leaves other routes alone', () => {
		const doc = flow();
		doc.stages[0].on_success = DONE;
		doc.stages[0].on_reject = { to: ABORT };
		dropStage(doc, undefined, 'two');
		expect(doc.stages[0].on_success).toBe(DONE);
		expect(doc.stages[0].on_reject).toEqual({ to: ABORT });
	});

	it('takes a stage out of a sequence, the chains left as they are', () => {
		const doc = flow();
		const q = doc.sequences![0];
		dropStage(doc, q, 's1');
		expect(q.stages.map((s) => s.id)).toEqual(['s2']);
		expect(q.stages[0].on_fail).toBeUndefined();
		expect(doc.stages).toHaveLength(4);
	});

	it('lets a sequence stage’s success fall back on the sugar', () => {
		const doc = flow();
		const q = doc.sequences![0];
		q.stages[0].on_success = 's2';
		dropStage(doc, q, 's2');
		expect(q.stages[0].on_success).toBeUndefined();
	});
});

describe('the questions a delete asks', () => {
	beforeEach(() => {
		vi.mocked(confirmDialog).mockResolvedValue(true);
	});

	it('counts the routes that lead to a stage', async () => {
		const doc = flow();
		await confirmDeleteStage(doc.stages, 'three');
		expect(asked().title).toBe('Delete stage three?');
		expect(asked().body).toContain('<b>three</b>');
		expect(asked().body).toContain('2 ');
		expect(asked().action).toBe('Delete stage');
	});

	it('says nothing of routes when there are none', async () => {
		await confirmDeleteStage(flow().stages, 'one');
		expect(asked().body).toBe(
			'The stage <b>one</b> is removed from this draft. Nothing changes for runs until you save.'
		);
	});

	it('does not count a stage’s route to itself', async () => {
		const s = [aStage({ id: 'a', on_fail: { to: 'a', max: 1 } })];
		await confirmDeleteStage(s, 'a');
		expect(asked().body).not.toContain('routes');
	});

	it.each<[string, string, string, string]>([
		['w', 'Delete watcher w?', 'Delete watcher', 'the one stage of its chain'],
		['o', 'Delete outcome o?', 'Delete outcome', '<b>o</b> is removed from this draft.']
	])('words the question for chain %s', async (id, title, action, body) => {
		const doc = flow();
		await confirmDeletePhase(
			doc,
			doc.phases!.find((p) => p.id === id)!
		);
		expect(asked().title).toBe(title);
		expect(asked().action).toBe(action);
		expect(asked().body).toContain(body);
	});

	it('refuses at once while a stage runs the sequence', async () => {
		const doc = flow();
		vi.mocked(confirmDialog).mockClear();
		await expect(confirmDeleteSequence(doc, doc.sequences![0])).resolves.toBe(false);
		expect(confirmDialog).not.toHaveBeenCalled();
	});

	it('asks about a sequence nothing runs', async () => {
		const doc = flow();
		await expect(confirmDeleteSequence(doc, doc.sequences![1])).resolves.toBe(true);
		expect(asked().title).toBe('Delete sequence unused?');
		expect(asked().action).toBe('Delete sequence');
	});

	it('refuses at once while a port names the schema', async () => {
		vi.mocked(confirmDialog).mockClear();
		await expect(confirmDeleteSchema(flow(), 'report')).resolves.toBe(false);
		expect(confirmDialog).not.toHaveBeenCalled();
	});

	it('asks about a schema nothing names', async () => {
		await expect(confirmDeleteSchema(flow(), 'free')).resolves.toBe(true);
		expect(asked().title).toBe('Delete schema free?');
	});

	it('names the agents a server’s delete touches', async () => {
		await confirmDeleteMCP(flow(), 'browser');
		expect(asked().title).toBe('Delete MCP server browser?');
		expect(asked().body).toContain('a, b');
	});

	it('says a server is unused', async () => {
		await confirmDeleteMCP(flow(), 'docs');
		expect(asked().body).toContain('The MCP server <b>docs</b> is removed from this draft.');
	});

	it('hands the answer back', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(false);
		await expect(confirmDeleteMCP(flow(), 'docs')).resolves.toBe(false);
	});
});
