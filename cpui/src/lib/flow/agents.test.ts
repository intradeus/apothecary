import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DocAgent, FlowDoc } from '$lib/domain/flowdoc';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { readDoc } from '$test/fixtures/logic';
import { addPort, deleteAgent, removePort, renameAgentTo, renamePort, runnersOf } from './agents';

vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const flow = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters: {}
agents:
  - name: writer
    prompt: p
    inputs: [{ name: notes }, { name: other }]
    outputs: [{ name: out }]
  - { name: reader, prompt: p }
sequences:
  - id: q
    stages:
      - { id: inner, type: agent, agent: writer, input: { notes: { from: a } } }
main:
  - { id: top, type: agent, agent: writer, input: { notes: { from: b }, other: { from: c } } }
  - { id: tool, type: command, run: r, agent: writer }
`);
const agent = (doc: FlowDoc, name = 'writer') => doc.agents!.find((a) => a.name === name)!;

describe('runnersOf', () => {
	it('lists the chains’ stages, then the sequences’, labelled by where they are', () => {
		expect(runnersOf(flow(), 'writer')).toEqual([
			{ label: 'top', id: 'top' },
			{ label: 'q › inner', id: 'inner', sequence: 'q' }
		]);
	});

	it('lists nothing for an agent nothing runs', () => {
		expect(runnersOf(flow(), 'reader')).toEqual([]);
	});
});

describe('renameAgentTo', () => {
	it('renames the agent and what runs it', () => {
		const doc = flow();
		expect(renameAgentTo(doc, 'writer', 'author')).toBe(true);
		expect(doc.agents?.[0].name).toBe('author');
		expect(doc.stages[0].agent).toBe('author');
		expect(doc.sequences?.[0].stages[0].agent).toBe('author');
	});

	it('trims what was typed', () => {
		const doc = flow();
		expect(renameAgentTo(doc, 'writer', '  author  ')).toBe(true);
		expect(doc.agents?.[0].name).toBe('author');
	});

	it.each([
		['empty', ''],
		['blank', '   '],
		['the same', 'writer'],
		['taken', 'reader'],
		['not a slug', 'Not A Slug'],
		['starting with a dash', '-x']
	])('refuses a name that is %s', (_, to) => {
		const doc = flow();
		expect(renameAgentTo(doc, 'writer', to)).toBe(false);
		expect(doc.agents?.[0].name).toBe('writer');
	});
});

describe('deleteAgent', () => {
	beforeEach(() => {
		vi.mocked(confirmDialog).mockResolvedValue(true);
	});

	it('names the stages that will stop validating', async () => {
		await deleteAgent(flow(), 'writer');
		const asked = vi.mocked(confirmDialog).mock.calls[0][0];
		expect(asked.title).toBe('Delete agent writer?');
		expect(asked.body).toContain('top, inner');
		expect(asked.body).toContain('Nothing changes for runs until you save.');
		expect(asked.action).toBe('Delete agent');
	});

	it('says nothing runs it, when nothing does', async () => {
		await deleteAgent(flow(), 'reader');
		expect(vi.mocked(confirmDialog).mock.calls[0][0].body).toContain(
			'The agent <b>reader</b> is removed from this draft.'
		);
	});

	it('escapes a name in the question', async () => {
		await deleteAgent(flow(), '<b>x');
		expect(vi.mocked(confirmDialog).mock.calls[0][0].body).toContain('&#60;b&#62;x');
	});

	it('removes it when told to', async () => {
		const doc = flow();
		await expect(deleteAgent(doc, 'writer')).resolves.toBe(true);
		expect(doc.agents?.map((a) => a.name)).toEqual(['reader']);
	});

	it('removes the list with its last agent', async () => {
		const doc = flow();
		await deleteAgent(doc, 'writer');
		await deleteAgent(doc, 'reader');
		expect(doc.agents).toBeUndefined();
	});

	it('keeps it when told no', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(false);
		const doc = flow();
		await expect(deleteAgent(doc, 'writer')).resolves.toBe(false);
		expect(doc.agents).toHaveLength(2);
	});
});

describe('ports', () => {
	const a = (): DocAgent => ({ name: 'a', prompt: 'p' });

	it.each([
		['input', 'inputs', 'input_1'],
		['output', 'outputs', 'output_1']
	] as const)('declares a new %s, a plain file', (field, list, name) => {
		const x = a();
		addPort(x, field);
		expect(x[list]).toEqual([{ name, type: 'file' }]);
	});

	it('numbers around the names taken', () => {
		const x = { ...a(), inputs: [{ name: 'input_1' }, { name: 'input_3' }] };
		addPort(x, 'input');
		expect(x.inputs?.at(-1)?.name).toBe('input_2');
	});

	it('removes a port, and the list with its last', () => {
		const x = { ...a(), inputs: [{ name: 'i' }, { name: 'j' }], outputs: [{ name: 'o' }] };
		removePort(x, 'input', 'i');
		expect(x.inputs).toEqual([{ name: 'j' }]);
		removePort(x, 'input', 'j');
		expect(x.inputs).toBeUndefined();
		removePort(x, 'output', 'o');
		expect(x.outputs).toBeUndefined();
	});

	it('renames a port and what every stage states under it', () => {
		const doc = flow();
		expect(renamePort(doc, agent(doc), 'input', 'notes', 'memo')).toBe(true);
		expect(agent(doc).inputs?.[0].name).toBe('memo');
		expect(Object.keys(doc.stages[0].input!)).toEqual(['memo', 'other']);
		expect(Object.keys(doc.sequences![0].stages[0].input!)).toEqual(['memo']);
	});

	it('renames an output apart from an input', () => {
		const doc = flow();
		expect(renamePort(doc, agent(doc), 'output', 'out', 'result')).toBe(true);
		expect(agent(doc).outputs?.[0].name).toBe('result');
	});

	it('trims what was typed', () => {
		const doc = flow();
		expect(renamePort(doc, agent(doc), 'input', 'notes', ' memo ')).toBe(true);
		expect(agent(doc).inputs?.[0].name).toBe('memo');
	});

	it.each([
		['unknown', 'nope', 'memo'],
		['to nothing', 'notes', ''],
		['to itself', 'notes', 'notes'],
		['to one taken', 'notes', 'other'],
		['to a bad name', 'notes', 'Bad Name']
	])('refuses a rename %s', (_, from, to) => {
		const doc = flow();
		expect(renamePort(doc, agent(doc), 'input', from, to)).toBe(false);
		expect(agent(doc).inputs?.map((p) => p.name)).toEqual(['notes', 'other']);
		expect(Object.keys(doc.stages[0].input!)).toEqual(['notes', 'other']);
	});

	it('refuses on an agent with no ports of that kind', () => {
		const doc = flow();
		expect(renamePort(doc, agent(doc, 'reader'), 'output', 'a', 'b')).toBe(false);
	});
});
